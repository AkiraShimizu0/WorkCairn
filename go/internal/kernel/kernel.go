// Package kernel provides the Workspace OS process-level coordination boundary.
// It intentionally contains no project, workflow, storage, or AI business logic.
package kernel

import (
	"errors"
	"strings"
	"sync"
)

var (
	ErrInvalidVersion = errors.New("kernel version is required")
	ErrAlreadyStarted = errors.New("kernel is already started")
	ErrNotStarted     = errors.New("kernel is not started")
)

// Kernel coordinates service registration, lifecycle, and command dispatch.
type Kernel struct {
	mu       sync.RWMutex
	version  string
	state    LifecycleState
	services map[ServiceKind]any
}

// New creates a stopped Kernel with no registered services.
func New(version string) (*Kernel, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil, ErrInvalidVersion
	}
	return &Kernel{
		version:  version,
		state:    StateStopped,
		services: make(map[ServiceKind]any),
	}, nil
}

// Start transitions a stopped Kernel to started.
func (kernel *Kernel) Start() error {
	kernel.mu.Lock()
	defer kernel.mu.Unlock()
	if kernel.state == StateStarted {
		return ErrAlreadyStarted
	}
	kernel.state = StateStarted
	return nil
}

// Stop transitions a started Kernel to stopped.
func (kernel *Kernel) Stop() error {
	kernel.mu.Lock()
	defer kernel.mu.Unlock()
	if kernel.state != StateStarted {
		return ErrNotStarted
	}
	kernel.state = StateStopped
	return nil
}

// Status returns an immutable snapshot of Kernel state.
func (kernel *Kernel) Status() KernelStatus {
	kernel.mu.RLock()
	defer kernel.mu.RUnlock()
	return KernelStatus{
		State:              kernel.state,
		Version:            kernel.version,
		RegisteredServices: kernel.registeredServicesLocked(),
	}
}
