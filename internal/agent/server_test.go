package agent

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The agent exposes host and container inventory, so an unauthenticated
// request must never be served. These cases pin the three outcomes that
// matter: no header, wrong token, correct token.
func TestAuthorization(t *testing.T) {
	const token = "s3cret"

	newServer := func(t *testing.T) *httptest.Server {
		t.Helper()

		config := &config{}
		config.Server.Port = 0
		config.Server.Token = token

		handler, err := buildHandler(config)
		if err != nil {
			t.Fatalf("buildHandler: %v", err)
		}

		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)

		return server
	}

	cases := []struct {
		name     string
		auth     string
		wantCode int
	}{
		{name: "no header", auth: "", wantCode: http.StatusUnauthorized},
		{name: "wrong token", auth: "Bearer wrong", wantCode: http.StatusUnauthorized},
		{name: "correct token", auth: "Bearer " + token, wantCode: http.StatusOK},
	}

	for _, endpoint := range []string{"/api/healthz", "/api/sysinfo/all"} {
		for _, tc := range cases {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				server := newServer(t)

				req, err := http.NewRequest("GET", server.URL+endpoint, nil)
				if err != nil {
					t.Fatalf("new request: %v", err)
				}
				if tc.auth != "" {
					req.Header.Set("Authorization", tc.auth)
				}

				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("request: %v", err)
				}
				defer resp.Body.Close()

				if resp.StatusCode != tc.wantCode {
					t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantCode)
				}
			})
		}
	}
}

// A token is mandatory: an empty one is a configuration error, not an
// unauthenticated mode.
func TestValidateRejectsEmptyToken(t *testing.T) {
	for _, token := range []string{"", "   "} {
		config := &config{}
		config.Server.Token = token

		if err := config.validate(); err == nil {
			t.Errorf("validate() with token %q = nil, want error", token)
		}
	}

	config := &config{}
	config.Server.Token = "set"

	if err := config.validate(); err != nil {
		t.Errorf("validate() with a token = %v, want nil", err)
	}
}
