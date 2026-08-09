// Package kernel provides the Workspace OS process-level coordination boundary.
// It intentionally contains no project, workflow, storage, or AI business logic.
package kernel

import (
	"errors"
	"fmt"
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
	lifecycleMu sync.Mutex
	mu          sync.RWMutex
	version     string
	state       LifecycleState
	services    map[ServiceKind]any
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
	kernel.lifecycleMu.Lock()
	defer kernel.lifecycleMu.Unlock()

	kernel.mu.RLock()
	if kernel.state == StateStarted {
		kernel.mu.RUnlock()
		return ErrAlreadyStarted
	}
	eventService, hasEventService := kernel.services[ServiceEvent]
	taskService, hasTaskService := kernel.services[ServiceTask]
	workerService, hasWorkerService := kernel.services[ServiceWorker]
	executionService, hasExecutionService := kernel.services[ServiceExecution]
	schedulerService, hasSchedulerService := kernel.services[ServiceScheduler]
	kernel.mu.RUnlock()

	if hasEventService {
		if err := eventService.(EventService).Start(); err != nil {
			return fmt.Errorf("start event service: %w", err)
		}
	}
	if hasTaskService {
		if err := taskService.(TaskService).Activate(); err != nil {
			if hasEventService {
				_ = eventService.(EventService).Stop()
			}
			return fmt.Errorf("activate task service: %w", err)
		}
	}
	if hasWorkerService {
		if err := workerService.(WorkerService).Activate(); err != nil {
			if hasTaskService {
				_ = taskService.(TaskService).Deactivate()
			}
			if hasEventService {
				_ = eventService.(EventService).Stop()
			}
			return fmt.Errorf("activate worker service: %w", err)
		}
	}
	if hasExecutionService {
		if err := executionService.(ExecutionService).Activate(); err != nil {
			if hasWorkerService {
				_ = workerService.(WorkerService).Deactivate()
			}
			if hasTaskService {
				_ = taskService.(TaskService).Deactivate()
			}
			if hasEventService {
				_ = eventService.(EventService).Stop()
			}
			return fmt.Errorf("activate execution service: %w", err)
		}
	}
	if hasSchedulerService {
		if err := schedulerService.(SchedulerService).Start(); err != nil {
			if hasExecutionService {
				_ = executionService.(ExecutionService).Deactivate()
			}
			if hasWorkerService {
				_ = workerService.(WorkerService).Deactivate()
			}
			if hasTaskService {
				_ = taskService.(TaskService).Deactivate()
			}
			if hasEventService {
				_ = eventService.(EventService).Stop()
			}
			return fmt.Errorf("start scheduler service: %w", err)
		}
	}

	kernel.mu.Lock()
	kernel.state = StateStarted
	kernel.mu.Unlock()
	return nil
}

// Stop transitions a started Kernel to stopped.
func (kernel *Kernel) Stop() error {
	kernel.lifecycleMu.Lock()
	defer kernel.lifecycleMu.Unlock()

	kernel.mu.RLock()
	if kernel.state != StateStarted {
		kernel.mu.RUnlock()
		return ErrNotStarted
	}
	eventService, hasEventService := kernel.services[ServiceEvent]
	taskService, hasTaskService := kernel.services[ServiceTask]
	workerService, hasWorkerService := kernel.services[ServiceWorker]
	executionService, hasExecutionService := kernel.services[ServiceExecution]
	schedulerService, hasSchedulerService := kernel.services[ServiceScheduler]
	kernel.mu.RUnlock()

	if hasSchedulerService {
		if err := schedulerService.(SchedulerService).Stop(); err != nil {
			return fmt.Errorf("stop scheduler service: %w", err)
		}
	}
	if hasExecutionService {
		if err := executionService.(ExecutionService).Deactivate(); err != nil {
			return fmt.Errorf("deactivate execution service: %w", err)
		}
	}
	if hasWorkerService {
		if err := workerService.(WorkerService).Deactivate(); err != nil {
			return fmt.Errorf("deactivate worker service: %w", err)
		}
	}
	if hasTaskService {
		if err := taskService.(TaskService).Deactivate(); err != nil {
			return fmt.Errorf("deactivate task service: %w", err)
		}
	}
	if hasEventService {
		if err := eventService.(EventService).Stop(); err != nil {
			return fmt.Errorf("stop event service: %w", err)
		}
	}

	kernel.mu.Lock()
	kernel.state = StateStopped
	kernel.mu.Unlock()
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
