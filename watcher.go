package caddyrelightslicervm

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"
)

var (
	watcherMu      sync.Mutex
	watcherCancels = make(map[*SlicerVM]context.CancelFunc)
)

// startIdleWatcher launches a background goroutine that periodically checks
// for idle VMs and pauses them.
func startIdleWatcher(rs *SlicerVM) {
	ctx, cancel := context.WithCancel(context.Background())

	watcherMu.Lock()
	watcherCancels[rs] = cancel
	watcherMu.Unlock()

	go runIdleWatcher(ctx, rs)
}

// stopIdleWatcher cancels the watcher goroutine for this module instance.
func stopIdleWatcher(rs *SlicerVM) {
	watcherMu.Lock()
	cancel, ok := watcherCancels[rs]
	if ok {
		delete(watcherCancels, rs)
	}
	watcherMu.Unlock()

	if ok {
		cancel()
	}
}

func runIdleWatcher(ctx context.Context, rs *SlicerVM) {
	defer func() {
		if r := recover(); r != nil {
			rs.logger.Error("idle watcher panic recovered", zap.Any("panic", r))
		}
	}()

	interval := time.Duration(rs.WatchInterval)
	idleTimeout := time.Duration(rs.IdleTimeout)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	rs.logger.Info("idle watcher started",
		zap.Duration("interval", interval),
		zap.Duration("idle_timeout", idleTimeout),
	)

	for {
		select {
		case <-ctx.Done():
			rs.logger.Info("idle watcher stopped")
			return
		case <-ticker.C:
			pauseIdleVMs(ctx, rs, idleTimeout)
		}
	}
}

func pauseIdleVMs(ctx context.Context, rs *SlicerVM, idleTimeout time.Duration) {
	idle := rs.stateMgr.idleApps(idleTimeout)
	for _, appName := range idle {
		hostname := rs.stateMgr.getHostname(appName)
		if hostname == "" {
			continue
		}

		rs.logger.Info("pausing idle VM",
			zap.String("app", appName),
			zap.String("hostname", hostname),
		)

		if err := rs.client.PauseVM(ctx, hostname); err != nil {
			rs.logger.Error("failed to pause VM",
				zap.String("app", appName),
				zap.String("hostname", hostname),
				zap.Error(err),
			)
			continue
		}

		rs.stateMgr.markPaused(appName)
		rs.logger.Info("VM paused successfully",
			zap.String("app", appName),
			zap.String("hostname", hostname),
		)
	}
}
