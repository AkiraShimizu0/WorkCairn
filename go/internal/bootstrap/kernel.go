// Package bootstrap composes concrete services without coupling Kernel to them.
package bootstrap

import (
	"github.com/AkiraShimizu0/workspace-os/go/internal/kernel"
	"github.com/AkiraShimizu0/workspace-os/go/internal/service"
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
	if err := workspaceKernel.RegisterEventService(service.NewEventService(nil)); err != nil {
		return nil, err
	}
	return workspaceKernel, nil
}
