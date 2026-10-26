package agent

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/glanceapp/glance/pkg/sysinfo"
	"gopkg.in/yaml.v3"
)

const defaultPort = 27973

type config struct {
	Server struct {
		Host  string `yaml:"host"`
		Port  int    `yaml:"port"`
		Token string `yaml:"token"`
	} `yaml:"server"`

	SystemInfoRequest *sysinfo.SystemInfoRequest `yaml:"system"`

	Docker struct {
		// Source is the Docker API endpoint, either a unix socket path or a
		// tcp:// URL. Empty disables the container endpoints.
		Source string `yaml:"source"`
	} `yaml:"docker"`
}

func loadConfig(path string) (*config, error) {
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		c := loadConfigFromEnvs()
		return c, c.validate()
	} else if err != nil {
		return nil, err
	}

	config := &config{}
	config.Server.Port = defaultPort

	err = yaml.Unmarshal(contents, &config)
	if err != nil {
		return nil, err
	}

	return config, config.validate()
}

// validate rejects a configuration that would serve data unauthenticated.
// The agent's endpoints expose host and container inventory, so an absent
// token is a misconfiguration rather than a supported open mode.
func (c *config) validate() error {
	if strings.TrimSpace(c.Server.Token) == "" {
		return fmt.Errorf("no authentication token configured: set server.token in the config file or the TOKEN environment variable")
	}

	return nil
}

func loadConfigFromEnvs() *config {
	c := &config{}

	portEnv := os.Getenv("PORT")
	port := defaultPort
	if portEnv != "" {
		var err error
		port, err = strconv.Atoi(portEnv)
		if err != nil {
			log.Panicf("Port must be a valid number, got: %v", err)
		}
	}

	c.Server.Port = port
	c.Server.Host = os.Getenv("HOST")
	c.Server.Token = os.Getenv("TOKEN")
	c.Docker.Source = os.Getenv("DOCKER_SOURCE")

	hideMountpoints := os.Getenv("HIDE_MOUNTPOINTS_BY_DEFAULT") == "true"

	c.SystemInfoRequest = &sysinfo.SystemInfoRequest{
		CPUTempSensor:            os.Getenv("TEMP_SENSOR"),
		HideMountpointsByDefault: hideMountpoints,
		Mountpoints:              make(map[string]sysinfo.MointpointRequest),
	}
	mr := c.SystemInfoRequest.Mountpoints

	if !hideMountpoints && isRunningInsideDockerContainer() {
		// Hide some common container mountpoints by default
		for _, mp := range []string{
			"/etc/hosts",
			"/etc/resolv.conf",
			"/etc/hostname",
		} {
			t := true
			mr[mp] = sysinfo.MointpointRequest{Hide: &t}
		}
	}

	mountpoints := os.Getenv("MOUNTPOINTS")
	if mountpoints != "" {
		for mp := range strings.SplitSeq(mountpoints, ",") {
			mp = strings.TrimSpace(mp)
			if mp == "" {
				continue
			}
			path, name, _ := strings.Cut(mp, ":")
			path, hide := strings.CutPrefix(path, "!")
			mr[path] = sysinfo.MointpointRequest{Name: name, Hide: &hide}
		}
	}

	return c
}

func isRunningInsideDockerContainer() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}
