package caddyrelightslicervm

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// askServer is a small internal HTTP server that validates domains for
// Caddy's on-demand TLS. It checks whether a VM exists with a tag
// matching the requested domain and returns 200 (approved) or 404.
//
// Configure it in the global options block:
//
//	{
//	    on_demand_tls {
//	        ask http://127.0.0.1:5555/check
//	    }
//	}
type askServer struct {
	listener net.Listener
	server   *http.Server
	stateMgr *vmStateManager
	logger   *zap.Logger
}

func newAskServer(addr string, stateMgr *vmStateManager, logger *zap.Logger) (*askServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("ask server listen on %s: %w", addr, err)
	}

	as := &askServer{
		listener: ln,
		stateMgr: stateMgr,
		logger:   logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", as.handleAsk)

	as.server = &http.Server{Handler: mux}
	go as.server.Serve(ln)

	logger.Info("ask server started", zap.String("addr", ln.Addr().String()))
	return as, nil
}

func (as *askServer) handleAsk(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		http.Error(w, "missing domain parameter", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	info, err := as.stateMgr.lookup(ctx, domain)
	if err != nil {
		as.logger.Error("ask lookup failed", zap.String("domain", domain), zap.Error(err))
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}

	if info.status == statusNotFound {
		as.logger.Debug("ask: domain not found", zap.String("domain", domain))
		http.NotFound(w, r)
		return
	}

	as.logger.Info("ask: domain approved", zap.String("domain", domain))
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
}

func (as *askServer) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return as.server.Shutdown(ctx)
}
