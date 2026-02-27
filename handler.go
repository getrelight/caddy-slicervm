package caddyslicervm

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
	appName := extractAppName(r)
	if appName == "" {
		http.Error(w, "could not determine app name from hostname", http.StatusBadRequest)
		return nil
	}

	// Block until VM is running (fast - SlicerVM resume is sub-second)
	ip, err := rs.stateMgr.ensureRunning(r.Context(), appName, time.Duration(rs.WakeTimeout))
	if err != nil {
		rs.logger.Error("failed to ensure VM running", zap.String("app", appName), zap.Error(err))
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, fmt.Sprintf("app %q not found", appName), http.StatusNotFound)
			return nil
		}
		w.Header().Set("Retry-After", "5")
		http.Error(w, fmt.Sprintf("app %q is starting up, please retry", appName), http.StatusServiceUnavailable)
		return nil
	}

	// VM is running - record activity and set upstream for reverse_proxy
	rs.stateMgr.touchLastSeen(appName)

	upstream := fmt.Sprintf("%s:%d", ip, rs.AppPort)
	caddyhttp.SetVar(r.Context(), "slicervm_upstream", upstream)

	rs.logger.Debug("proxying request",
		zap.String("app", appName),
		zap.String("upstream", upstream),
		zap.String("path", r.URL.Path),
	)

	return next.ServeHTTP(w, r)
}

// extractAppName extracts the app name from the first subdomain label.
// For example, "myapp.apps.example.com" returns "myapp".
// Returns empty string if the hostname doesn't have enough parts.
func extractAppName(r *http.Request) string {
	host := r.Host

	// Strip port if present
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		// Make sure this isn't part of an IPv6 address
		if !strings.Contains(host, "]") || strings.LastIndex(host, "]") < idx {
			host = host[:idx]
		}
	}

	parts := strings.Split(host, ".")
	if len(parts) < 3 {
		return ""
	}

	return parts[0]
}
