package caddyrelightslicervm

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"
)

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (rs *SlicerVM) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	hostname := extractHostname(r)
	if hostname == "" {
		http.Error(w, "could not determine hostname", http.StatusBadRequest)
		return nil
	}

	// Block until VM is running (fast - SlicerVM resume is sub-second)
	ip, err := rs.stateMgr.ensureRunning(r.Context(), hostname, time.Duration(rs.WakeTimeout))
	if err != nil {
		rs.logger.Error("failed to ensure VM running", zap.String("domain", hostname), zap.Error(err))
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, fmt.Sprintf("app for %q not found", hostname), http.StatusNotFound)
			return nil
		}
		w.Header().Set("Retry-After", "5")
		http.Error(w, fmt.Sprintf("app for %q is starting up, please retry", hostname), http.StatusServiceUnavailable)
		return nil
	}

	// VM is running - record activity and set upstream for reverse_proxy
	rs.stateMgr.touchLastSeen(hostname)

	upstream := fmt.Sprintf("%s:%d", ip, rs.AppPort)
	caddyhttp.SetVar(r.Context(), "relight_slicervm_upstream", upstream)

	rs.logger.Debug("proxying request",
		zap.String("domain", hostname),
		zap.String("upstream", upstream),
		zap.String("path", r.URL.Path),
	)

	return next.ServeHTTP(w, r)
}

// extractHostname returns the hostname from the request, stripped of port.
// Used as the lookup key for VM tag matching.
func extractHostname(r *http.Request) string {
	host := r.Host

	// Strip port if present
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		// Make sure this isn't part of an IPv6 address
		if !strings.Contains(host, "]") || strings.LastIndex(host, "]") < idx {
			host = host[:idx]
		}
	}

	return host
}
