// Package bootstrap composes concrete services without coupling Kernel to them.
package bootstrap

import (
	"github.com/AkiraShimizu0/workspace-os/go/internal/kernel"
	"github.com/AkiraShimizu0/workspace-os/go/internal/service"
	"github.com/AkiraShimizu0/workspace-os/go/internal/taskstore"
)

const DefaultKernelVersion = "v0.3.0"

// NewDefaultKernel registers production services without starting the Kernel.
func NewDefaultKernel(version string) (*kernel.Kernel, error) {
	workspaceKernel, err := kernel.New(version)
	if err != nil {
		return nil, err
	}
	if err := workspaceKernel.RegisterProjectService(service.NewProjectService()); err != nil {
		return nil, err
	}
	if err := workspaceKernel.RegisterWorkflowService(service.NewWorkflowService()); err != nil {
		return nil, err
	}
	eventService := service.NewEventService(nil)
	if err := workspaceKernel.RegisterEventService(eventService); err != nil {
		return nil, err
	}
	taskService, err := service.NewTaskService(taskstore.NewInMemory(), eventService)
	if err != nil {
		return nil, err
	}
	if err := workspaceKernel.RegisterTaskService(taskService); err != nil {
		return nil, err
	}
	return workspaceKernel, nil
}
