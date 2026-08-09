package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
	"github.com/AkiraShimizu0/workspace-os/go/internal/review"
	"github.com/AkiraShimizu0/workspace-os/go/internal/revision"
	workspaceruntime "github.com/AkiraShimizu0/workspace-os/go/internal/runtime"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

const maxCanonicalReviewBytes = 1 << 20

var (
	ErrRevisionApprovalRequired = errors.New("explicit Revision approval is required")
	ErrRevisionPreflightFailed  = errors.New("Revision preflight failed")
)

type RevisionPlanInput struct {
	VaultRoot     string
	ProjectID     string
	ProjectName   string
	SourceTaskID  string
	ReviewVersion string
	CurrentTime   time.Time
}

type RevisionPlan struct {
	ProjectID              string          `json:"project_id"`
	ProjectName            string          `json:"project_name"`
	SourceTaskID           string          `json:"source_task_id"`
	SourceTaskStatus       task.Status     `json:"source_task_status"`
	SourceReviewCanonical  string          `json:"source_review_canonical"`
	SourceReviewProjection string          `json:"source_review_projection"`
	ReviewDecision         review.Decision `json:"review_decision"`
	AssigneeID             string          `json:"assignee_id"`
	Title                  string          `json:"title"`
	RevisionTaskID         string          `json:"revision_task_id"`
	IntentPath             string          `json:"intent_path"`
	Executable             bool            `json:"executable"`
	BlockingReasons        []string        `json:"blocking_reasons"`
	ApprovalRequired       bool            `json:"approval_required"`
}

type RevisionPreflightError struct{ Plan RevisionPlan }

func (*RevisionPreflightError) Error() string        { return ErrRevisionPreflightFailed.Error() }
func (*RevisionPreflightError) Is(target error) bool { return target == ErrRevisionPreflightFailed }

type ExecuteRevisionInput struct {
	RevisionPlanInput
	Approved       bool
	CommandID      string
	EventObservers []event.Observer
}

func PlanRevision(ctx context.Context, input RevisionPlanInput) (RevisionPlan, error) {
	if ctx == nil {
		return RevisionPlan{}, fmt.Errorf("plan Revision: context is required")
	}
	input.VaultRoot = strings.TrimSpace(input.VaultRoot)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.ProjectName = strings.TrimSpace(input.ProjectName)
	input.SourceTaskID = strings.TrimSpace(input.SourceTaskID)
	input.ReviewVersion = strings.TrimSpace(input.ReviewVersion)
	if input.ProjectID == "" || input.CurrentTime.IsZero() {
		return RevisionPlan{}, fmt.Errorf("plan Revision: Project ID and time are required")
	}
	if err := review.ValidateVersion(input.ReviewVersion); err != nil {
		return RevisionPlan{}, err
	}
	tasks, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: input.VaultRoot, ProjectName: input.ProjectName})
	if err != nil {
		return RevisionPlan{}, fmt.Errorf("plan Revision Task snapshot: %w", err)
	}
	snapshot, err := tasks.InspectAll(ctx)
	if err != nil {
		return RevisionPlan{}, fmt.Errorf("plan Revision Task snapshot: %w", err)
	}
	var source task.Task
	ids := make([]string, 0, len(snapshot))
	for _, candidate := range snapshot {
		ids = append(ids, candidate.ID)
		if candidate.ID == input.SourceTaskID {
			source = candidate
		}
	}
	if source.ID == "" {
		return RevisionPlan{}, fmt.Errorf("plan Revision source Task: %w", task.ErrTaskNotFound)
	}
	revisionTaskID, err := task.NextTaskID(ids)
	if err != nil {
		return RevisionPlan{}, fmt.Errorf("plan Revision Task ID: %w", err)
	}
	baseName := input.SourceTaskID + ".review"
	if input.ReviewVersion != "" {
		baseName += "." + input.ReviewVersion
	}
	canonical := filepath.ToSlash(filepath.Join("Reviews", baseName+".json"))
	projection := filepath.ToSlash(filepath.Join("Reviews", baseName+".md"))
	decision, err := loadCanonicalReview(input.VaultRoot, input.ProjectName, canonical)
	if err != nil {
		return RevisionPlan{}, fmt.Errorf("plan Revision canonical Review: %w", err)
	}
	intentStore, err := vault.NewRevisionIntentStore(input.VaultRoot, input.ProjectName)
	if err != nil {
		return RevisionPlan{}, fmt.Errorf("plan Revision intent Store: %w", err)
	}
	_, sourceExists, err := intentStore.ExistingForSource(ctx, canonical, projection)
	if err != nil {
		return RevisionPlan{}, fmt.Errorf("plan Revision existing intent: %w", err)
	}
	intentPath := filepath.ToSlash(filepath.Join("Revisions", revisionTaskID+".revision.md"))
	intentExists, err := vaultPathExists(input.VaultRoot, input.ProjectName, intentPath)
	if err != nil {
		return RevisionPlan{}, fmt.Errorf("plan Revision intent path: %w", err)
	}
	blocking := make([]string, 0, 4)
	if source.Status != task.StatusCompleted {
		blocking = append(blocking, "source_task_not_completed")
	}
	assigneeID := ""
	if source.AssigneeID == nil || strings.TrimSpace(*source.AssigneeID) == "" {
		blocking = append(blocking, "source_task_assignee_missing")
	} else {
		assigneeID = *source.AssigneeID
	}
	if decision.Verdict != review.VerdictRequestChanges {
		blocking = append(blocking, "review_does_not_request_changes")
	}
	if sourceExists {
		blocking = append(blocking, "revision_for_review_already_exists")
	}
	if intentExists {
		blocking = append(blocking, "revision_intent_path_already_exists")
	}
	return RevisionPlan{
		ProjectID: input.ProjectID, ProjectName: input.ProjectName,
		SourceTaskID: input.SourceTaskID, SourceTaskStatus: source.Status,
		SourceReviewCanonical: canonical, SourceReviewProjection: projection,
		ReviewDecision: decision, AssigneeID: assigneeID,
		Title:          input.SourceTaskID + "のレビュー指摘を反映する",
		RevisionTaskID: revisionTaskID, IntentPath: intentPath,
		Executable: len(blocking) == 0, BlockingReasons: blocking, ApprovalRequired: true,
	}, nil
}

func ExecuteRevision(ctx context.Context, input ExecuteRevisionInput) (revision.Result, error) {
	if ctx == nil {
		return revision.Result{}, fmt.Errorf("execute Revision: context is required")
	}
	if !input.Approved {
		return revision.Result{}, ErrRevisionApprovalRequired
	}
	claim, err := claimProjectCommand(ctx, input.VaultRoot, input.ProjectName, input.CommandID, "revision.execute", input.SourceTaskID, struct {
		ProjectID     string    `json:"project_id"`
		ProjectName   string    `json:"project_name"`
		SourceTaskID  string    `json:"source_task_id"`
		ReviewVersion string    `json:"review_version,omitempty"`
		CurrentTime   time.Time `json:"current_time"`
	}{input.ProjectID, input.ProjectName, input.SourceTaskID, input.ReviewVersion, input.CurrentTime})
	if err != nil {
		return revision.Result{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[revision.Result](claim); ok {
		return replayed, replayErr
	}
	result, revisionErr := executeClaimedRevision(ctx, input)
	failureCode, failureStage := revisionCommandFailure(revisionErr, result)
	partial := result.Intent != nil && result.Intent.Committed
	return result, finishDurableCommand(ctx, claim, result, revisionErr, failureCode, failureStage, partial)
}

func executeClaimedRevision(ctx context.Context, input ExecuteRevisionInput) (revision.Result, error) {
	plan, err := PlanRevision(ctx, input.RevisionPlanInput)
	if err != nil {
		return revision.Result{}, fmt.Errorf("execute Revision preflight: %w", err)
	}
	if !plan.Executable {
		return revision.Result{}, &RevisionPreflightError{Plan: plan}
	}
	taskStore, err := vault.NewTaskStore(vault.TaskStoreConfig{
		VaultRoot: input.VaultRoot, ProjectName: input.ProjectName, Clock: func() time.Time { return input.CurrentTime },
	})
	if err != nil {
		return revision.Result{}, fmt.Errorf("execute Revision Task Store: %w", err)
	}
	intentStore, err := vault.NewRevisionIntentStore(input.VaultRoot, input.ProjectName)
	if err != nil {
		return revision.Result{}, fmt.Errorf("execute Revision intent Store: %w", err)
	}
	audit, err := vault.NewAuditSubscriber(input.VaultRoot, input.ProjectName)
	if err != nil {
		return revision.Result{}, fmt.Errorf("execute Revision Audit: %w", err)
	}
	runtime, err := workspaceruntime.NewRevisionRuntime(workspaceruntime.RevisionDependencies{
		TaskStore: taskStore, IntentStore: intentStore, AuditHandler: audit.Handler(), Observers: input.EventObservers,
	})
	if err != nil {
		return revision.Result{}, fmt.Errorf("execute Revision Runtime composition: %w", err)
	}
	if err := runtime.Start(); err != nil {
		return revision.Result{}, fmt.Errorf("execute Revision Runtime start: %w", err)
	}
	result, executionErr := runtime.Execute(ctx, revision.Intent{
		ProjectID: plan.ProjectID, ProjectName: plan.ProjectName,
		SourceTaskID: plan.SourceTaskID, SourceReview: plan.SourceReviewCanonical,
		SourceProjection: plan.SourceReviewProjection, ReviewDecision: plan.ReviewDecision,
		AssigneeID: plan.AssigneeID, RevisionTaskID: plan.RevisionTaskID,
		Title: plan.Title, CreatedAt: input.CurrentTime,
	})
	stopErr := runtime.Stop()
	if executionErr != nil {
		return result, executionErr
	}
	if stopErr != nil {
		return result, fmt.Errorf("execute Revision Runtime stop: %w", stopErr)
	}
	return result, nil
}

func revisionCommandFailure(err error, result revision.Result) (string, string) {
	if err == nil {
		return "", ""
	}
	switch {
	case errors.Is(err, ErrRevisionPreflightFailed):
		return "REVISION_PREFLIGHT_FAILED", "preflight"
	case errors.Is(err, revision.ErrSaveFailed):
		return "REVISION_INTENT_SAVE_FAILED", "revision_intent_save"
	case result.Intent != nil && result.Intent.Committed && result.Task == nil:
		return "REVISION_TASK_CREATE_FAILED", "revision_task_create"
	case result.Task != nil && !result.EventPublished:
		return "REVISION_EVENT_PUBLISH_FAILED", "revision_event_publish"
	default:
		return "REVISION_EXECUTION_FAILED", "process"
	}
}

func loadCanonicalReview(root, projectName, relativePath string) (review.Decision, error) {
	path := filepath.Join(root, "プロジェクト", projectName, filepath.FromSlash(relativePath))
	file, err := os.Open(path)
	if err != nil {
		return review.Decision{}, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxCanonicalReviewBytes+1))
	if err != nil {
		return review.Decision{}, err
	}
	if len(content) > maxCanonicalReviewBytes {
		return review.Decision{}, fmt.Errorf("canonical Review is too large")
	}
	return review.DecodeDecision(content)
}

func vaultPathExists(root, projectName, relativePath string) (bool, error) {
	path := filepath.Join(root, "プロジェクト", projectName, filepath.FromSlash(relativePath))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("path is not a regular file")
	}
	return true, nil
}
