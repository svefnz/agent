package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// dockerStatsTimeout bounds a single stats call. The endpoint blocks for about
// one sampling interval, so this leaves room for slow or busy hosts.
const dockerStatsTimeout = 10 * time.Second

type containerStats struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Image         string  `json:"image"`
	State         string  `json:"state"`
	Status        string  `json:"status"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsedMB  uint64  `json:"memory_used_mb"`
	MemoryLimitMB uint64  `json:"memory_limit_mb"`
	MemoryPercent uint8   `json:"memory_percent"`
}

type dockerContainerJSON struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Status string            `json:"Status"`
	Labels map[string]string `json:"Labels"`
}

type dockerStatsJSON struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs     uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
}

func (s *dockerStatsJSON) cpuPercent() float64 {
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(s.CPUStats.SystemCPUUsage) - float64(s.PreCPUStats.SystemCPUUsage)

	if cpuDelta <= 0 || systemDelta <= 0 {
		return 0
	}

	cores := float64(s.CPUStats.OnlineCPUs)
	if cores == 0 {
		cores = 1
	}

	return (cpuDelta / systemDelta) * cores * 100
}

// memoryUsageBytes excludes page cache, matching what `docker stats` reports.
func (s *dockerStatsJSON) memoryUsageBytes() uint64 {
	usage := s.MemoryStats.Usage

	cache := s.MemoryStats.Stats["inactive_file"]
	if cache == 0 {
		cache = s.MemoryStats.Stats["cache"]
	}

	if cache >= usage {
		return 0
	}

	return usage - cache
}

// newDockerClient builds a client that speaks to the Docker daemon over either
// a unix socket or a TCP endpoint.
func newDockerClient(source string) (*http.Client, string, string, error) {
	if strings.HasPrefix(source, "tcp://") ||
		strings.HasPrefix(source, "http://") ||
		strings.HasPrefix(source, "https://") {

		parsed, err := url.Parse(source)
		if err != nil {
			return nil, "", "", fmt.Errorf("parsing docker source: %w", err)
		}

		scheme := parsed.Scheme
		if scheme == "tcp" {
			scheme = "http"
		}

		port := parsed.Port()
		if port == "" {
			if scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}

		return &http.Client{Timeout: dockerStatsTimeout}, scheme, parsed.Hostname() + ":" + port, nil
	}

	client := &http.Client{
		Timeout: dockerStatsTimeout,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", source)
			},
		},
	}

	return client, "http", "docker", nil
}

func fetchContainerList(client *http.Client, scheme, hostname string) ([]dockerContainerJSON, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerStatsTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(
		ctx, "GET", scheme+"://"+hostname+"/containers/json?all=false", nil,
	)
	if err != nil {
		return nil, err
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", response.Status)
	}

	var containers []dockerContainerJSON
	if err := json.NewDecoder(response.Body).Decode(&containers); err != nil {
		return nil, err
	}

	return containers, nil
}

func fetchContainerStats(client *http.Client, scheme, hostname, id string) (*dockerStatsJSON, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerStatsTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(
		ctx, "GET",
		scheme+"://"+hostname+"/containers/"+url.PathEscape(id)+"/stats?stream=false",
		nil,
	)
	if err != nil {
		return nil, err
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", response.Status)
	}

	var stats dockerStatsJSON
	if err := json.NewDecoder(response.Body).Decode(&stats); err != nil {
		return nil, err
	}

	return &stats, nil
}

// collectContainerStats gathers state plus resource usage for every running
// container reachable through the configured Docker source. Stats are fetched
// concurrently because each call blocks for roughly one sampling interval.
func collectContainerStats(source string) ([]containerStats, error) {
	client, scheme, hostname, err := newDockerClient(source)
	if err != nil {
		return nil, err
	}

	containers, err := fetchContainerList(client, scheme, hostname)
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	results := make([]containerStats, len(containers))
	for i := range containers {
		c := &containers[i]
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimLeft(c.Names[0], "/")
		}

		results[i] = containerStats{
			ID:     c.ID,
			Name:   name,
			Image:  c.Image,
			State:  strings.ToLower(c.State),
			Status: strings.ToLower(c.Status),
		}
	}

	var wg sync.WaitGroup
	for i := range containers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			stats, err := fetchContainerStats(client, scheme, hostname, containers[index].ID)
			if err != nil {
				// A container that will not report stats simply has none.
				return
			}

			results[index].CPUPercent = stats.cpuPercent()
			results[index].MemoryUsedMB = stats.memoryUsageBytes() / 1024 / 1024
			results[index].MemoryLimitMB = stats.MemoryStats.Limit / 1024 / 1024
			if results[index].MemoryLimitMB > 0 {
				results[index].MemoryPercent = uint8(min(
					100, results[index].MemoryUsedMB*100/results[index].MemoryLimitMB,
				))
			}
		}(i)
	}
	wg.Wait()

	sort.Slice(results, func(a, b int) bool {
		return strings.ToLower(results[a].Name) < strings.ToLower(results[b].Name)
	})

	return results, nil
}
