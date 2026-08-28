package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/execution"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/worker"
)

var ErrDependencyEvidenceMissing = errors.New("canonical dependency evidence is unavailable")

// DependencyEvidenceCollector is the storage-neutral read port used by the
// Execution Service after readiness and approval, but before Task start. It
// resolves direct dependencies into canonical Deliverables without changing
// Task state or prompting a Provider.
type DependencyEvidenceCollector interface {
	Collect(ctx context.Context, request execution.Request) ([]worker.DependencyEvidence, error)
}

// DependencyEvidenceError carries only a safe Task identity and typed reason.
// Deliverable content and storage paths are intentionally absent.
type DependencyEvidenceError struct {
	TaskID string
	Reason string
	Err    error
}

func (evidenceError *DependencyEvidenceError) Error() string {
	return fmt.Sprintf("%s for %s (%s)", ErrDependencyEvidenceMissing, strings.TrimSpace(evidenceError.TaskID), strings.TrimSpace(evidenceError.Reason))
}

func (evidenceError *DependencyEvidenceError) Unwrap() error { return evidenceError.Err }

func (evidenceError *DependencyEvidenceError) Is(target error) bool {
	return target == ErrDependencyEvidenceMissing
}
