package caddyslicervm

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
	sdk "github.com/slicervm/sdk"
	"go.uber.org/zap"
)

// SlicerVM is a Caddy HTTP middleware that routes subdomains to
// Slicer VMs and implements scale-to-zero by pausing idle VMs and
// resuming them on incoming requests.
type SlicerVM struct {
	// SlicerURL is the Slicer API address. Can be an HTTP URL
	// (e.g. http://127.0.0.1:8080) or a Unix socket path
	// (e.g. ~/slicer-mac/slicer.sock or /var/run/slicer.sock).
	SlicerURL string `json:"slicer_url"`

	// SlicerToken is the API token for authenticating with Slicer.
	SlicerToken string `json:"slicer_token"`

	// HostGroup is the Slicer host group containing app VMs.
	// Apps are identified by node tags matching the subdomain.
	HostGroup string `json:"host_group"`

	// IdleTimeout is how long a VM can be idle before being paused.
	// Default: 5m. Minimum: 30s.
	IdleTimeout caddy.Duration `json:"idle_timeout,omitempty"`

	// WakeTimeout is the maximum time to wait for a paused VM to resume.
	// Default: 30s.
	WakeTimeout caddy.Duration `json:"wake_timeout,omitempty"`

	// AppPort is the port on the VM to proxy to. Default: 8080.
	AppPort int `json:"app_port,omitempty"`

	// WatchInterval is how often the idle watcher checks for idle VMs.
	// Default: 30s.
	WatchInterval caddy.Duration `json:"watch_interval,omitempty"`

	logger   *zap.Logger
	client   *sdk.SlicerClient
	stateMgr *vmStateManager
}

func (s *SlicerVM) Provision(ctx caddy.Context) error {
	s.logger = ctx.Logger()

	if s.IdleTimeout == 0 {
		s.IdleTimeout = caddy.Duration(5 * time.Minute)
	}
	if s.WakeTimeout == 0 {
		s.WakeTimeout = caddy.Duration(30 * time.Second)
	}
	if s.AppPort == 0 {
		s.AppPort = 8080
	}
	if s.WatchInterval == 0 {
		s.WatchInterval = caddy.Duration(30 * time.Second)
	}

	httpClient, baseURL := buildHTTPClient(s.SlicerURL)
	s.client = sdk.NewSlicerClient(baseURL, s.SlicerToken, "caddy-slicervm", httpClient)
	s.stateMgr = newVMStateManager(s.client, s.HostGroup, s.logger)

	startIdleWatcher(s)

	return nil
}

func (s *SlicerVM) Validate() error {
	if s.SlicerURL == "" {
		return fmt.Errorf("slicer_url is required")
	}
	if s.SlicerToken == "" {
		return fmt.Errorf("slicer_token is required")
	}
	if s.HostGroup == "" {
		return fmt.Errorf("host_group is required")
	}
	if time.Duration(s.IdleTimeout) < 30*time.Second {
		return fmt.Errorf("idle_timeout must be at least 30s")
	}
	if s.AppPort < 1 || s.AppPort > 65535 {
		return fmt.Errorf("app_port must be between 1 and 65535")
	}
	return nil
}

func (s *SlicerVM) Cleanup() error {
	stopIdleWatcher(s)
	return nil
}

// buildHTTPClient returns an HTTP client and base URL for the Slicer API.
// If the URL looks like a Unix socket path, it returns a client that dials
// the socket and a dummy HTTP base URL.
func buildHTTPClient(rawURL string) (*http.Client, string) {
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		return nil, rawURL
	}

	// Treat as Unix socket path
	sockPath := rawURL
	if strings.HasPrefix(sockPath, "~/") {
		home, _ := os.UserHomeDir()
		sockPath = home + sockPath[1:]
	}

	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}, "http://localhost"
}
