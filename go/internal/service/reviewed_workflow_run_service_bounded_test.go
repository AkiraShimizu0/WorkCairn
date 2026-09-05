package service

import (
	"context"
	"errors"
	"testing"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/autonomy"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
)

// TestSequentialRunNeverCallsReviserWhenRevisionForbidden is the ADR-0072
// regression for the sequential Run path: a Request Changes verdict with
// revisionPermission=autonomy.PermissionForbidden must stop before ever
// calling the Reviser -- noopReviserFake fails the test immediately if it
// is invoked, so a passing test is direct proof of zero calls.
func TestSequentialRunNeverCallsReviserWhenRevisionForbidden(t *testing.T) {
	service, err := NewReviewedWorkflowRunService(
		&workflowRunPlannerFake{steps: []WorkflowStepPlan{{TaskID: "TASK-001", Ready: true, BlockingReasons: []string{}}}},
		&reviewedTaskExecutorFake{},
		&reviewedReviewerFake{verdicts: []review.Verdict{review.VerdictRequestChanges}},
		&noopReviserFake{t: t},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := service.Run(context.Background(), "CMD-BOUNDED-SEQUENTIAL", 10, autonomy.PermissionForbidden)
	if !errors.Is(runErr, ErrRevisionForbiddenByProfile) {
		t.Fatalf("Run() error = %v, want ErrRevisionForbiddenByProfile", runErr)
	}
	var typed *ReviewedWorkflowRunError
	if !errors.As(runErr, &typed) || typed.Stage != "revision_forbidden" {
		t.Fatalf("Run() stage = %#v, want revision_forbidden", typed)
	}
	// The forbidden stop is checked before any revision.execute child
	// Command ID is even derived (mirroring the revision_limit guard's own
	// shape), so this Task's evidence must carry neither a
	// RevisionCommandID nor a Revision result -- nothing was ever claimed
	// or executed for it.
	if len(result.Tasks) != 1 || result.Tasks[0].RevisionCommandID != "" || result.Tasks[0].Revision != nil {
		t.Fatalf("Run() Tasks = %#v, want no RevisionCommandID/Revision evidence at all", result.Tasks)
	}
}

// TestRunParallelNeverCallsReviserWhenRevisionForbidden is the same
// regression for the parallel runBranch path (RunParallel).
func TestRunParallelNeverCallsReviserWhenRevisionForbidden(t *testing.T) {
	executor := &concurrencyTrackingExecutorFake{}
	reviewer := &scriptedVerdictReviewerFake{verdicts: map[string]review.Verdict{"TASK-001": review.VerdictRequestChanges}}
	service, err := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, executor, reviewer, &noopReviserFake{t: t})
	if err != nil {
		t.Fatal(err)
	}
	planner := &scriptedBatchPlannerFake{plans: []WorkflowBatchPlan{
		{TaskIDs: []string{"TASK-001"}},
		{Completed: true},
	}}
	result, runErr := service.RunParallel(context.Background(), "CMD-BOUNDED-PARALLEL", "CMD-BOUNDED-PARALLEL", 10, 2, 5, autonomy.PermissionForbidden, planner)
	if !errors.Is(runErr, ErrRevisionForbiddenByProfile) {
		t.Fatalf("RunParallel() error = %v, want ErrRevisionForbiddenByProfile", runErr)
	}
	if len(result.Tasks) != 1 || result.Tasks[0].Verdict != review.VerdictRequestChanges ||
		result.Tasks[0].RevisionCommandID != "" || result.Tasks[0].Revision != nil {
		t.Fatalf("RunParallel() Tasks = %#v, want no RevisionCommandID/Revision evidence at all", result.Tasks)
	}
}

// TestResumeRevisionRejectsForbiddenPermissionBeforeAnyDispatch is the
// Service-boundary defense-in-depth layer for ResumeRevision (ADR-0072):
// even if a caller reaches this entry point despite the Process layer's own
// rejection of interaction.workflow.recover_revision for a bounded Session,
// ResumeRevision itself refuses before dispatching anything -- the
// batchPlanner here is never even consulted (nil is passed and must never
// be dereferenced).
func TestResumeRevisionRejectsForbiddenPermissionBeforeAnyDispatch(t *testing.T) {
	service, err := NewReviewedWorkflowRunService(&workflowRunPlannerFake{}, &concurrencyTrackingExecutorFake{}, &approvingReviewerFake{}, &noopReviserFake{t: t})
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := service.ResumeRevision(context.Background(), "CMD-BOUNDED-RESUME", "CMD-BOUNDED-RESUME", "TASK-002", 10, 2, 5, autonomy.PermissionForbidden, nil)
	if !errors.Is(runErr, ErrRevisionForbiddenByProfile) {
		t.Fatalf("ResumeRevision() error = %v, want ErrRevisionForbiddenByProfile", runErr)
	}
}

// TestStandardPermissionDelegatedRunPathsUnaffected pins that passing
// autonomy.PermissionDelegated (what every standard-profile Session
// resolves to) reproduces the exact pre-ADR-0072 behavior: the Reviser is
// still called, and a Revision is still created.
func TestStandardPermissionDelegatedRunPathsUnaffected(t *testing.T) {
	service, err := NewReviewedWorkflowRunService(
		&workflowRunPlannerFake{steps: []WorkflowStepPlan{{TaskID: "TASK-001", Ready: true, BlockingReasons: []string{}}}},
		&reviewedTaskExecutorFake{},
		&reviewedReviewerFake{verdicts: []review.Verdict{review.VerdictRequestChanges}},
		&reviewedReviserFake{},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := service.Run(context.Background(), "CMD-STANDARD-SEQUENTIAL", 1, autonomy.PermissionDelegated)
	if runErr != nil || result.Status != "limit_reached" || result.Tasks[0].Revision == nil || result.Tasks[0].Revision.Task == nil {
		t.Fatalf("Run() with PermissionDelegated = %#v, %v, want unchanged standard behavior", result, runErr)
	}
}
