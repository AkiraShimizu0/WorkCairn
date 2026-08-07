// Package runner defines provider Adapter boundaries and deterministic model
// routing for WorkerService.
package runner

import (
	"context"

	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
)

// Runner is implemented by provider-specific Adapters. It has no knowledge of
// Task lifecycle, Vault storage, approval policy, or Kernel configuration.
type Runner interface {
	Name() string
	Run(ctx context.Context, request worker.RunRequest) (worker.RunResult, error)
}

// Resolver is the smallest routing dependency required by WorkerService.
type Resolver interface {
	Resolve(model string) (Runner, error)
}
