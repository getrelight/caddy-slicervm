package caddyslicervm

import (
	"fmt"
	"time"

	"github.com/caddyserver/caddy/v2"
	sdk "github.com/slicervm/sdk"
	"go.uber.org/zap"
)

// SlicerVM is a Caddy HTTP middleware that routes subdomains to
// Slicer VMs and implements scale-to-zero by pausing idle VMs and
// resuming them on incoming requests.
type SlicerVM struct {
	// SlicerURL is the base URL of the Slicer API (e.g. http://127.0.0.1:8080).
	SlicerURL string `json:"slicer_url"`

	// SlicerToken is the API token for authenticating with Slicer.
	SlicerToken string `json:"slicer_token"`

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

func (rs *SlicerVM) Provision(ctx caddy.Context) error {
	rs.logger = ctx.Logger()

	// Apply defaults
	if rs.IdleTimeout == 0 {
		rs.IdleTimeout = caddy.Duration(5 * time.Minute)
	}
	if rs.WakeTimeout == 0 {
		rs.WakeTimeout = caddy.Duration(30 * time.Second)
	}
	if rs.AppPort == 0 {
		rs.AppPort = 8080
	}
	if rs.WatchInterval == 0 {
		rs.WatchInterval = caddy.Duration(30 * time.Second)
	}

	rs.client = sdk.NewSlicerClient(rs.SlicerURL, rs.SlicerToken, "caddy-slicervm", nil)
	rs.stateMgr = newVMStateManager(rs.client, rs.logger)

	startIdleWatcher(rs)

	return nil
}

func (rs *SlicerVM) Validate() error {
	if rs.SlicerURL == "" {
		return fmt.Errorf("slicer_url is required")
	}
	if rs.SlicerToken == "" {
		return fmt.Errorf("slicer_token is required")
	}
	if time.Duration(rs.IdleTimeout) < 30*time.Second {
		return fmt.Errorf("idle_timeout must be at least 30s")
	}
	if rs.AppPort < 1 || rs.AppPort > 65535 {
		return fmt.Errorf("app_port must be between 1 and 65535")
	}
	return nil
}

func (rs *SlicerVM) Cleanup() error {
	stopIdleWatcher(rs)
	return nil
}
