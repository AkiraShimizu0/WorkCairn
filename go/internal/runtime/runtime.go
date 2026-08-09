// Package runtime composes Workspace Kernel services with concrete Adapters.
// It owns dependency wiring, not Domain rules or environment loading.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workspace-os/go/internal/bootstrap"
	"github.com/AkiraShimizu0/workspace-os/go/internal/deliverable"
	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
	"github.com/AkiraShimizu0/workspace-os/go/internal/execution"
	"github.com/AkiraShimizu0/workspace-os/go/internal/kernel"
	promptbuilder "github.com/AkiraShimizu0/workspace-os/go/internal/prompt"
	"github.com/AkiraShimizu0/workspace-os/go/internal/runner"
	"github.com/AkiraShimizu0/workspace-os/go/internal/service"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

var (
	ErrInvalidConfig       = errors.New("invalid Runtime configuration")
	ErrInvalidDependencies = errors.New("invalid Runtime dependencies")
)

// Config is supplied by a future process Adapter. It contains no logic for
// reading .env, files, Vaults, or secret stores.
type Config struct {
	KernelVersion string
	ModelValue    string
	Claude        claude.Config
}

// Dependencies are effectful ports supplied by the process composition root.
type Dependencies struct {
	HTTPClient   claude.HTTPDoer
	TaskStore    task.Store
	Deliverables deliverable.Store
	AuditHandler event.Handler
	Readiness    service.ReadinessService
}

// Runtime owns one composed Kernel lifecycle and exposes the typed single-Task
// execution entry. It does not create Tasks or infer approval.
type Runtime struct {
	kernel *kernel.Kernel
}

func New(config Config, dependencies Dependencies) (*Runtime, error) {
	config.KernelVersion = strings.TrimSpace(config.KernelVersion)
	config.ModelValue = strings.TrimSpace(config.ModelValue)
	if config.KernelVersion == "" {
		config.KernelVersion = bootstrap.DefaultKernelVersion
	}
	if config.ModelValue == "" {
		return nil, fmt.Errorf("%w: logical model value is required", ErrInvalidConfig)
	}
	if isNilDependency(dependencies.TaskStore) {
		return nil, fmt.Errorf("%w: TaskStore is required", ErrInvalidDependencies)
	}
	if isNilDependency(dependencies.Deliverables) {
		return nil, fmt.Errorf("%w: Deliverable Store is required", ErrInvalidDependencies)
	}
	if dependencies.AuditHandler == nil {
		return nil, fmt.Errorf("%w: Audit handler is required", ErrInvalidDependencies)
	}

	claudeRunner, err := claude.New(config.Claude, dependencies.HTTPClient)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	registry := runner.NewRegistry()
	if err := registry.Register(claudeRunner); err != nil {
		return nil, fmt.Errorf("%w: register Claude Runner: %v", ErrInvalidConfig, err)
	}
	if err := registry.MapModel(config.ModelValue, claudeRunner.Name()); err != nil {
		return nil, fmt.Errorf("%w: map logical model: %v", ErrInvalidConfig, err)
	}

	workspaceKernel, err := bootstrap.NewKernelWithDependencies(
		config.KernelVersion,
		bootstrap.KernelDependencies{
			WorkerRuntime: bootstrap.WorkerRuntime{
				PromptBuilder: promptbuilder.NewBuilder(),
				Runners:       registry,
			},
			TaskStore:    dependencies.TaskStore,
			Deliverables: dependencies.Deliverables,
			Readiness:    dependencies.Readiness,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("compose Runtime: %w", err)
	}
	eventService, err := workspaceKernel.EventService()
	if err != nil {
		return nil, fmt.Errorf("compose Runtime Audit: %w", err)
	}
	for _, eventType := range []event.Type{
		event.TaskCreated,
		event.TaskStarted,
		event.TaskCompleted,
		event.TaskFailed,
		event.TaskHeld,
		event.TaskResumed,
	} {
		if _, err := eventService.Subscribe(eventType, dependencies.AuditHandler); err != nil {
			return nil, fmt.Errorf("compose Runtime Audit: %w", err)
		}
	}
	return &Runtime{kernel: workspaceKernel}, nil
}

func (runtime *Runtime) Start() error {
	return runtime.kernel.Start()
}

func (runtime *Runtime) Stop() error {
	return runtime.kernel.Stop()
}

func (runtime *Runtime) Status() kernel.KernelStatus {
	return runtime.kernel.Status()
}

func (runtime *Runtime) Execute(ctx context.Context, request execution.Request) (execution.Result, error) {
	service, err := runtime.kernel.ExecutionService()
	if err != nil {
		return execution.Result{}, err
	}
	return service.Execute(ctx, request)
}

func isNilDependency(value any) bool {
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
