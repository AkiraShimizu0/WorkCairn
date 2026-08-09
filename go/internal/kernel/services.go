package kernel

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
	"github.com/AkiraShimizu0/workspace-os/go/internal/execution"
	"github.com/AkiraShimizu0/workspace-os/go/internal/project"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
	"github.com/AkiraShimizu0/workspace-os/go/internal/workflow"
)

// ServiceKind identifies a stable Kernel service boundary.
type ServiceKind string

const (
	ServiceProject      ServiceKind = "project"
	ServiceWorkflow     ServiceKind = "workflow"
	ServiceEvent        ServiceKind = "event"
	ServiceExecution    ServiceKind = "execution"
	ServiceOrganization ServiceKind = "organization"
	ServiceTask         ServiceKind = "task"
	ServiceWorker       ServiceKind = "worker"
	ServiceScheduler    ServiceKind = "scheduler"
)

var (
	ErrInvalidService            = errors.New("kernel service is invalid")
	ErrServiceAlreadyRegistered  = errors.New("kernel service is already registered")
	ErrServiceNotRegistered      = errors.New("kernel service is not registered")
	ErrServiceRegistrationClosed = errors.New("kernel service registration requires stopped kernel")
)

// ProjectService is the Kernel-facing facade for the Project Domain.
type ProjectService interface {
	NextTaskID(existingIDs []string) (string, error)
	ValidateTask(task project.Task) error
	CanTransition(current, target project.Status) error
}

// WorkflowService is the Kernel-facing facade for the Workflow Domain.
type WorkflowService interface {
	Readiness(
		tasks []workflow.Task,
		dependencies []workflow.Dependency,
		existingEmployees map[string]bool,
	) (workflow.ReadinessResult, error)
}

// EventService is the Kernel-facing business event boundary. The concrete Bus,
// persistence, and subscriber implementations remain outside Kernel.
type EventService interface {
	Start() error
	Stop() error
	Publish(ctx context.Context, published event.Event) error
	Subscribe(eventType event.Type, handler event.Handler) (event.Subscription, error)
	Unsubscribe(subscription event.Subscription) error
}

// OrganizationService remains a marker until its Go Domain is integrated.
type OrganizationService interface {
	IsOrganizationService()
}

// TaskService owns Task lifecycle while Worker execution and storage Adapters
// remain separate boundaries.
type TaskService interface {
	Activate() error
	Deactivate() error
	Create(ctx context.Context, input task.CreateInput) (task.Task, error)
	Start(ctx context.Context, taskID string) (task.Task, error)
	Complete(ctx context.Context, taskID string) (task.Task, error)
	Fail(ctx context.Context, taskID, reason string) (task.Task, error)
	Hold(ctx context.Context, taskID, reason string) (task.Task, error)
	Resume(ctx context.Context, taskID string) (task.Task, error)
	Get(ctx context.Context, taskID string) (task.Task, error)
}

// WorkerService is the provider-neutral AI employee execution boundary. The
// Kernel does not know its PromptBuilder, Runner Registry, or Provider.
type WorkerService interface {
	Activate() error
	Deactivate() error
	Execute(ctx context.Context, request worker.ExecutionRequest) (worker.ExecutionResult, error)
}

// ExecutionService coordinates one ready, approved Task through TaskService
// and WorkerService. Kernel knows only its lifecycle and typed contract.
type ExecutionService interface {
	Activate() error
	Deactivate() error
	Execute(ctx context.Context, request execution.Request) (execution.Result, error)
}

// SchedulerService is the Kernel-owned timing lifecycle. Command validation,
// persistence, and dispatch remain behind the concrete Service ports.
type SchedulerService interface {
	Start() error
	Stop() error
}

func (kernel *Kernel) RegisterProjectService(service ProjectService) error {
	return kernel.registerService(ServiceProject, service)
}

func (kernel *Kernel) RegisterWorkflowService(service WorkflowService) error {
	return kernel.registerService(ServiceWorkflow, service)
}

func (kernel *Kernel) RegisterEventService(service EventService) error {
	return kernel.registerService(ServiceEvent, service)
}

func (kernel *Kernel) RegisterOrganizationService(service OrganizationService) error {
	return kernel.registerService(ServiceOrganization, service)
}

func (kernel *Kernel) RegisterTaskService(service TaskService) error {
	return kernel.registerService(ServiceTask, service)
}

func (kernel *Kernel) RegisterWorkerService(service WorkerService) error {
	return kernel.registerService(ServiceWorker, service)
}

func (kernel *Kernel) RegisterExecutionService(service ExecutionService) error {
	return kernel.registerService(ServiceExecution, service)
}

func (kernel *Kernel) RegisterSchedulerService(service SchedulerService) error {
	return kernel.registerService(ServiceScheduler, service)
}

func (kernel *Kernel) ProjectService() (ProjectService, error) {
	service, err := kernel.service(ServiceProject)
	if err != nil {
		return nil, err
	}
	return service.(ProjectService), nil
}

func (kernel *Kernel) WorkflowService() (WorkflowService, error) {
	service, err := kernel.service(ServiceWorkflow)
	if err != nil {
		return nil, err
	}
	return service.(WorkflowService), nil
}

func (kernel *Kernel) EventService() (EventService, error) {
	service, err := kernel.service(ServiceEvent)
	if err != nil {
		return nil, err
	}
	return service.(EventService), nil
}

func (kernel *Kernel) OrganizationService() (OrganizationService, error) {
	service, err := kernel.service(ServiceOrganization)
	if err != nil {
		return nil, err
	}
	return service.(OrganizationService), nil
}

func (kernel *Kernel) TaskService() (TaskService, error) {
	service, err := kernel.service(ServiceTask)
	if err != nil {
		return nil, err
	}
	return service.(TaskService), nil
}

func (kernel *Kernel) WorkerService() (WorkerService, error) {
	service, err := kernel.service(ServiceWorker)
	if err != nil {
		return nil, err
	}
	return service.(WorkerService), nil
}

func (kernel *Kernel) ExecutionService() (ExecutionService, error) {
	service, err := kernel.service(ServiceExecution)
	if err != nil {
		return nil, err
	}
	return service.(ExecutionService), nil
}

func (kernel *Kernel) SchedulerService() (SchedulerService, error) {
	service, err := kernel.service(ServiceScheduler)
	if err != nil {
		return nil, err
	}
	return service.(SchedulerService), nil
}

func (kernel *Kernel) registerService(kind ServiceKind, service any) error {
	if isNil(service) {
		return fmt.Errorf("%w: %s", ErrInvalidService, kind)
	}
	kernel.lifecycleMu.Lock()
	defer kernel.lifecycleMu.Unlock()
	kernel.mu.Lock()
	defer kernel.mu.Unlock()
	if kernel.state != StateStopped {
		return fmt.Errorf("%w: %s", ErrServiceRegistrationClosed, kind)
	}
	if _, exists := kernel.services[kind]; exists {
		return fmt.Errorf("%w: %s", ErrServiceAlreadyRegistered, kind)
	}
	kernel.services[kind] = service
	return nil
}

func (kernel *Kernel) service(kind ServiceKind) (any, error) {
	kernel.mu.RLock()
	defer kernel.mu.RUnlock()
	service, exists := kernel.services[kind]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrServiceNotRegistered, kind)
	}
	return service, nil
}

func (kernel *Kernel) registeredServicesLocked() []ServiceKind {
	registered := make([]ServiceKind, 0, len(kernel.services))
	for kind := range kernel.services {
		registered = append(registered, kind)
	}
	sort.Slice(registered, func(left, right int) bool {
		return registered[left] < registered[right]
	})
	return registered
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
