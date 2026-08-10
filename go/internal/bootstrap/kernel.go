// Package bootstrap composes concrete services without coupling Kernel to them.
package bootstrap

import (
	"context"
	"errors"

	"github.com/AkiraShimizu0/workcairn/go/internal/deliverable"
	"github.com/AkiraShimizu0/workcairn/go/internal/deliverablestore"
	"github.com/AkiraShimizu0/workcairn/go/internal/kernel"
	"github.com/AkiraShimizu0/workcairn/go/internal/policy"
	"github.com/AkiraShimizu0/workcairn/go/internal/runner"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
	"github.com/AkiraShimizu0/workcairn/go/internal/taskstore"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

const DefaultKernelVersion = "v0.3.0"

var ErrWorkerRuntimeNotConfigured = errors.New("worker runtime is not configured")

// WorkerRuntime contains only composition dependencies. Provider credentials
// and configuration remain the responsibility of a future Runtime layer.
type WorkerRuntime struct {
	PromptBuilder worker.PromptBuilder
	Runners       runner.Resolver
}

// KernelDependencies contains replaceable Adapter ports needed by the
// composition root. Provider and storage configuration stay outside Kernel.
type KernelDependencies struct {
	WorkerRuntime WorkerRuntime
	TaskStore     task.Store
	Deliverables  deliverable.Store
	Readiness     service.ReadinessService
}

// NewDefaultKernel registers production services without starting the Kernel.
// Worker execution is safely unavailable until Runtime dependencies are
// supplied through NewKernelWithWorkerRuntime.
func NewDefaultKernel(version string) (*kernel.Kernel, error) {
	return NewKernelWithDependencies(version, KernelDependencies{
		WorkerRuntime: WorkerRuntime{
			PromptBuilder: unconfiguredPromptBuilder{},
			Runners:       runner.NewRegistry(),
		},
		TaskStore:    taskstore.NewInMemory(),
		Deliverables: deliverablestore.NewInMemory(),
	})
}

// NewKernelWithWorkerRuntime is the composition root used by tests today and
// by a future CLI/API Runtime when provider Adapters are available.
func NewKernelWithWorkerRuntime(version string, runtime WorkerRuntime) (*kernel.Kernel, error) {
	return NewKernelWithDependencies(version, KernelDependencies{
		WorkerRuntime: runtime,
		TaskStore:     taskstore.NewInMemory(),
		Deliverables:  deliverablestore.NewInMemory(),
	})
}

// NewKernelWithDependencies is the Runtime composition point for replaceable
// Worker and TaskStore Adapters. It does not read files, environment, or keys.
func NewKernelWithDependencies(version string, dependencies KernelDependencies) (*kernel.Kernel, error) {
	workspaceKernel, err := kernel.New(version)
	if err != nil {
		return nil, err
	}
	if err := workspaceKernel.RegisterProjectService(service.NewProjectService()); err != nil {
		return nil, err
	}
	workflowService := service.NewWorkflowService()
	if err := workspaceKernel.RegisterWorkflowService(workflowService); err != nil {
		return nil, err
	}
	eventService := service.NewEventService(nil)
	if err := workspaceKernel.RegisterEventService(eventService); err != nil {
		return nil, err
	}
	taskService, err := service.NewTaskService(dependencies.TaskStore, eventService)
	if err != nil {
		return nil, err
	}
	if err := workspaceKernel.RegisterTaskService(taskService); err != nil {
		return nil, err
	}
	workerService, err := service.NewWorkerService(
		dependencies.WorkerRuntime.PromptBuilder,
		dependencies.WorkerRuntime.Runners,
	)
	if err != nil {
		return nil, err
	}
	if err := workspaceKernel.RegisterWorkerService(workerService); err != nil {
		return nil, err
	}
	readiness := dependencies.Readiness
	if readiness == nil {
		readiness = workflowService
	}
	executionService, err := service.NewExecutionService(
		readiness,
		taskService,
		workerService,
		dependencies.Deliverables,
		policy.ExplicitApprovalPolicy{},
		policy.HoldOnFailurePolicy{},
	)
	if err != nil {
		return nil, err
	}
	if err := workspaceKernel.RegisterExecutionService(executionService); err != nil {
		return nil, err
	}
	return workspaceKernel, nil
}

type unconfiguredPromptBuilder struct{}

func (unconfiguredPromptBuilder) Build(context.Context, worker.PromptInput) (worker.Prompt, error) {
	return worker.Prompt{}, ErrWorkerRuntimeNotConfigured
}
