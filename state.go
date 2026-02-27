package caddyslicervm

import (
	"context"
	"fmt"
	"sync"
	"time"

	sdk "github.com/slicervm/sdk"
	"go.uber.org/zap"
)

// vmStatus represents the known state of a VM.
type vmStatus int

const (
	statusUnknown  vmStatus = iota
	statusRunning
	statusPaused
	statusWaking
	statusNotFound
)

// vmInfo holds cached state for a single VM (identified by app/host group name).
type vmInfo struct {
	hostname string
	ip       string
	status   vmStatus
	lastSeen time.Time // last time a request was proxied to this VM

	// wakeCh is closed when a wake operation completes (success or failure).
	// Multiple goroutines block on the same channel for coalesced wake.
	wakeCh  chan struct{}
	wakeErr error
}

// vmStateManager manages VM state and provides coalesced wake operations.
type vmStateManager struct {
	mu     sync.Mutex
	vms    map[string]*vmInfo
	client *sdk.SlicerClient
	logger *zap.Logger
}

func newVMStateManager(client *sdk.SlicerClient, logger *zap.Logger) *vmStateManager {
	return &vmStateManager{
		vms:    make(map[string]*vmInfo),
		client: client,
		logger: logger,
	}
}

// lookup returns cached VM info, or fetches from the API on first access.
func (m *vmStateManager) lookup(ctx context.Context, appName string) (*vmInfo, error) {
	m.mu.Lock()
	info, ok := m.vms[appName]
	if ok {
		m.mu.Unlock()
		return info, nil
	}
	m.mu.Unlock()

	// First time seeing this app - fetch from Slicer
	nodes, err := m.client.GetHostGroupNodes(ctx, appName)
	if err != nil {
		return nil, fmt.Errorf("fetching host group %q: %w", appName, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check again under lock
	if info, ok := m.vms[appName]; ok {
		return info, nil
	}

	if len(nodes) == 0 {
		info := &vmInfo{status: statusNotFound}
		m.vms[appName] = info
		return info, nil
	}

	node := nodes[0]
	info = &vmInfo{
		hostname: node.Hostname,
		ip:       node.IP,
		lastSeen: time.Now(),
	}
	switch node.Status {
	case "Running":
		info.status = statusRunning
	case "Paused":
		info.status = statusPaused
	default:
		info.status = statusUnknown
	}
	m.vms[appName] = info
	return info, nil
}

// ensureRunning makes sure the VM for appName is running. If paused, it
// initiates a resume and blocks until done.
// Concurrent callers are coalesced - only one ResumeVM call is made.
func (m *vmStateManager) ensureRunning(ctx context.Context, appName string, timeout time.Duration) (string, error) {
	info, err := m.lookup(ctx, appName)
	if err != nil {
		return "", err
	}

	switch info.status {
	case statusNotFound:
		return "", fmt.Errorf("app %q: not found", appName)
	case statusRunning:
		return info.ip, nil
	case statusWaking:
		return m.waitForWake(ctx, appName, info, timeout)
	case statusPaused, statusUnknown:
		return m.initiateWake(ctx, appName, info, timeout)
	}

	return "", fmt.Errorf("app %q: unexpected status", appName)
}

func (m *vmStateManager) initiateWake(ctx context.Context, appName string, info *vmInfo, timeout time.Duration) (string, error) {
	m.mu.Lock()
	// Double-check under lock
	if info.status == statusWaking {
		m.mu.Unlock()
		return m.waitForWake(ctx, appName, info, timeout)
	}
	if info.status == statusRunning {
		m.mu.Unlock()
		return info.ip, nil
	}

	info.status = statusWaking
	info.wakeCh = make(chan struct{})
	info.wakeErr = nil
	hostname := info.hostname
	m.mu.Unlock()

	m.logger.Info("waking VM", zap.String("app", appName), zap.String("hostname", hostname))
	go m.doWake(appName, hostname)

	return m.waitForWake(ctx, appName, info, timeout)
}

func (m *vmStateManager) waitForWake(ctx context.Context, appName string, info *vmInfo, timeout time.Duration) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-info.wakeCh:
		if info.wakeErr != nil {
			return "", fmt.Errorf("app %q: wake failed: %w", appName, info.wakeErr)
		}
		return info.ip, nil
	case <-timer.C:
		return "", fmt.Errorf("app %q: wake timed out after %s", appName, timeout)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// doWake calls ResumeVM and trusts it's ready immediately (sub-second resume).
func (m *vmStateManager) doWake(appName, hostname string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := m.client.ResumeVM(ctx, hostname)
	m.finishWake(appName, err)
}

func (m *vmStateManager) finishWake(appName string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	info, ok := m.vms[appName]
	if !ok {
		return
	}

	info.wakeErr = err
	if err == nil {
		info.status = statusRunning
		m.logger.Info("VM resumed", zap.String("app", appName))
	} else {
		info.status = statusPaused
		m.logger.Error("VM wake failed", zap.String("app", appName), zap.Error(err))
	}

	if info.wakeCh != nil {
		close(info.wakeCh)
	}
}

func (m *vmStateManager) touchLastSeen(appName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if info, ok := m.vms[appName]; ok {
		info.lastSeen = time.Now()
	}
}

func (m *vmStateManager) idleApps(timeout time.Duration) []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var idle []string
	for name, info := range m.vms {
		if info.status == statusRunning && now.Sub(info.lastSeen) > timeout {
			idle = append(idle, name)
		}
	}
	return idle
}

func (m *vmStateManager) markPaused(appName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if info, ok := m.vms[appName]; ok {
		info.status = statusPaused
	}
}

func (m *vmStateManager) getHostname(appName string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if info, ok := m.vms[appName]; ok {
		return info.hostname
	}
	return ""
}
