package kernel

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
)

// ServiceKind identifies a stable Kernel service boundary.
type ServiceKind string

const (
	ServiceProject      ServiceKind = "project"
	ServiceWorkflow     ServiceKind = "workflow"
	ServiceOrganization ServiceKind = "organization"
	ServiceTask         ServiceKind = "task"
)

var (
	ErrInvalidService           = errors.New("kernel service is invalid")
	ErrServiceAlreadyRegistered = errors.New("kernel service is already registered")
	ErrServiceNotRegistered     = errors.New("kernel service is not registered")
)

// Marker interfaces keep service boundaries distinct without defining domain
// operations before their Go services are integrated into the Kernel.
type ProjectService interface {
	IsProjectService()
}

type WorkflowService interface {
	IsWorkflowService()
}

type OrganizationService interface {
	IsOrganizationService()
}

type TaskService interface {
	IsTaskService()
}

func (kernel *Kernel) RegisterProjectService(service ProjectService) error {
	return kernel.registerService(ServiceProject, service)
}

func (kernel *Kernel) RegisterWorkflowService(service WorkflowService) error {
	return kernel.registerService(ServiceWorkflow, service)
}

func (kernel *Kernel) RegisterOrganizationService(service OrganizationService) error {
	return kernel.registerService(ServiceOrganization, service)
}

func (kernel *Kernel) RegisterTaskService(service TaskService) error {
	return kernel.registerService(ServiceTask, service)
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

func (kernel *Kernel) registerService(kind ServiceKind, service any) error {
	if isNil(service) {
		return fmt.Errorf("%w: %s", ErrInvalidService, kind)
	}
	kernel.mu.Lock()
	defer kernel.mu.Unlock()
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
