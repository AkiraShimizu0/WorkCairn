package process

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/recovery"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

const (
	CompleteTaskPlanCommitmentDomain = "workcairn.recovery.complete-task-plan.v1"
	CompleteTaskRecoveryOperation    = "recovery.complete_task.apply"
)

var (
	ErrInvalidCompleteTaskRecoveryApply = errors.New("invalid complete-task recovery apply request")
	ErrRecoveryCommitmentMismatch       = errors.New("complete-task recovery commitment does not match current plan")
)

// CompleteTaskPlanCommitmentV1 is the closed, versioned hash input. Every
// recovery.Plan field is copied explicitly so future Plan additions cannot
// silently enter or bypass the approval contract.
type CompleteTaskPlanCommitmentV1 struct {
	SchemaVersion    int             `json:"schema_version"`
	ProjectName      string          `json:"project_name"`
	TaskID           string          `json:"task_id"`
	Action           recovery.Action `json:"action"`
	ExpectedStatus   task.Status     `json:"expected_status"`
	ExpectedVersion  uint64          `json:"expected_version"`
	EvidenceRef      string          `json:"evidence_reference,omitempty"`
	EvidenceDigest   string          `json:"evidence_digest,omitempty"`
	Reason           string          `json:"reason,omitempty"`
	SourceRevision   string          `json:"source_revision"`
	Executable       bool            `json:"executable"`
	BlockingReasons  []string        `json:"blocking_reasons"`
	ApprovalRequired bool            `json:"approval_required"`
}

type CompleteTaskRecoveryApprovalV1 struct {
	SchemaVersion  int    `json:"schema_version"`
	Domain         string `json:"domain"`
	PlanCommitment string `json:"plan_commitment"`
}

type CompleteTaskRecoveryPrepareView struct {
	SchemaVersion    int                            `json:"schema_version"`
	ProjectName      string                         `json:"project_name"`
	TaskID           string                         `json:"task_id"`
	Action           recovery.Action                `json:"action"`
	TaskStatus       task.Status                    `json:"task_status"`
	TaskVersion      uint64                         `json:"task_version"`
	ApprovalRequired bool                           `json:"approval_required"`
	Approval         CompleteTaskRecoveryApprovalV1 `json:"approval"`
}

type CompleteTaskRecoveryApplyInput struct {
	VaultRoot      string
	ProjectName    string
	TaskID         string
	CommandID      string
	Approval       CompleteTaskRecoveryApprovalV1
	EventObservers []event.Observer
}

// CompleteTaskRecoveryResult is the only Apply result stored in the Command
// Ledger or returned over HTTP. Keep this wire shape at exactly seven fields.
type CompleteTaskRecoveryResult struct {
	SchemaVersion      int             `json:"schema_version"`
	ProjectName        string          `json:"project_name"`
	TaskID             string          `json:"task_id"`
	Action             recovery.Action `json:"action"`
	Status             string          `json:"status"`
	TaskStateCommitted bool            `json:"task_state_committed"`
	TaskVersion        uint64          `json:"task_version"`
}

func NewCompleteTaskPlanCommitmentV1(plan recovery.Plan) (CompleteTaskPlanCommitmentV1, error) {
	if plan.Validate() != nil || plan.Action != recovery.ActionCompleteTask || plan.Reason != "" {
		return CompleteTaskPlanCommitmentV1{}, recovery.ErrNotRecoverable
	}
	return CompleteTaskPlanCommitmentV1{
		SchemaVersion: plan.SchemaVersion, ProjectName: plan.ProjectName, TaskID: plan.TaskID,
		Action: plan.Action, ExpectedStatus: plan.ExpectedStatus, ExpectedVersion: plan.ExpectedVersion,
		EvidenceRef: plan.EvidenceRef, EvidenceDigest: plan.EvidenceDigest, Reason: plan.Reason,
		SourceRevision: plan.SourceRevision, Executable: plan.Executable,
		BlockingReasons: append([]string(nil), plan.BlockingReasons...), ApprovalRequired: plan.ApprovalRequired,
	}, nil
}

func (commitment CompleteTaskPlanCommitmentV1) Digest() (string, error) {
	return commandledger.RequestDigest(struct {
		Domain string                       `json:"domain"`
		Plan   CompleteTaskPlanCommitmentV1 `json:"plan"`
	}{Domain: CompleteTaskPlanCommitmentDomain, Plan: commitment})
}

func PrepareCompleteTaskRecovery(ctx context.Context, input RecoveryInput, taskID string) (CompleteTaskRecoveryPrepareView, error) {
	if !canonicalProjectNameRequired(input.ProjectName) || !canonicalIdentifierRequired(taskID) {
		return CompleteTaskRecoveryPrepareView{}, ErrInvalidCompleteTaskRecoveryApply
	}
	if _, err := task.ParseTaskID(taskID); err != nil {
		return CompleteTaskRecoveryPrepareView{}, ErrInvalidCompleteTaskRecoveryApply
	}
	plan, err := PlanTaskRecovery(ctx, input, recovery.PlanRequest{TaskID: taskID, Action: recovery.ActionCompleteTask})
	if err != nil {
		return CompleteTaskRecoveryPrepareView{}, fmt.Errorf("prepare complete-task recovery: %w", err)
	}
	if !plan.Executable {
		return CompleteTaskRecoveryPrepareView{}, recovery.ErrNotRecoverable
	}
	commitment, err := NewCompleteTaskPlanCommitmentV1(plan)
	if err != nil {
		return CompleteTaskRecoveryPrepareView{}, err
	}
	digest, err := commitment.Digest()
	if err != nil {
		return CompleteTaskRecoveryPrepareView{}, err
	}
	return CompleteTaskRecoveryPrepareView{
		SchemaVersion: plan.SchemaVersion, ProjectName: plan.ProjectName, TaskID: plan.TaskID,
		Action: plan.Action, TaskStatus: plan.ExpectedStatus, TaskVersion: plan.ExpectedVersion,
		ApprovalRequired: true,
		Approval:         CompleteTaskRecoveryApprovalV1{SchemaVersion: 1, Domain: CompleteTaskPlanCommitmentDomain, PlanCommitment: digest},
	}, nil
}

func ExecuteCompleteTaskRecoveryApply(ctx context.Context, input CompleteTaskRecoveryApplyInput, approved bool) (CompleteTaskRecoveryResult, error) {
	if !approved {
		return CompleteTaskRecoveryResult{}, ErrRecoveryApprovalRequired
	}
	if err := validateCompleteTaskRecoveryApplyInput(input); err != nil {
		return CompleteTaskRecoveryResult{}, err
	}
	request := struct {
		ProjectName string                         `json:"project_name"`
		TaskID      string                         `json:"task_id"`
		Approval    CompleteTaskRecoveryApprovalV1 `json:"approval"`
	}{input.ProjectName, input.TaskID, input.Approval}
	claim, err := claimProjectCommand(ctx, input.VaultRoot, input.ProjectName, input.CommandID, CompleteTaskRecoveryOperation, input.TaskID, request)
	if err != nil {
		return CompleteTaskRecoveryResult{}, err
	}
	if replay, ok, err := replayDurableCommand[CompleteTaskRecoveryResult](claim); ok {
		return replay, err
	}

	result := CompleteTaskRecoveryResult{
		SchemaVersion: recovery.SchemaVersion, ProjectName: input.ProjectName, TaskID: input.TaskID,
		Action: recovery.ActionCompleteTask, Status: "failed",
	}
	plan, planErr := PlanTaskRecovery(ctx, RecoveryInput{VaultRoot: input.VaultRoot, ProjectName: input.ProjectName}, recovery.PlanRequest{
		TaskID: input.TaskID, Action: recovery.ActionCompleteTask,
	})
	if planErr != nil {
		return result, finishDurableCommand(ctx, claim, result, planErr, "RECOVERY_APPLY_PRECONDITION_FAILED", "recovery_plan", false)
	}
	result.TaskVersion = plan.ExpectedVersion
	commitment, commitmentErr := NewCompleteTaskPlanCommitmentV1(plan)
	if commitmentErr != nil {
		return result, finishDurableCommand(ctx, claim, result, commitmentErr, "RECOVERY_NOT_APPLICABLE", "recovery_plan", false)
	}
	digest, digestErr := commitment.Digest()
	if digestErr != nil {
		return result, finishDurableCommand(ctx, claim, result, digestErr, "RECOVERY_APPLY_PRECONDITION_FAILED", "recovery_plan", false)
	}
	if subtle.ConstantTimeCompare([]byte(digest), []byte(input.Approval.PlanCommitment)) != 1 {
		staleErr := fmt.Errorf("%w: %w", ErrRecoveryCommitmentMismatch, ErrRecoveryPlanStale)
		return result, finishDurableCommand(ctx, claim, result, staleErr, "RECOVERY_PLAN_STALE", "recovery_plan", false)
	}
	if !plan.Executable {
		return result, finishDurableCommand(ctx, claim, result, recovery.ErrNotRecoverable, "RECOVERY_NOT_APPLICABLE", "recovery_plan", false)
	}

	internal, applyErr := executeTaskRecovery(ctx, RecoveryInput{VaultRoot: input.VaultRoot, ProjectName: input.ProjectName}, plan, true, input.EventObservers)
	result = projectCompleteTaskRecoveryResult(input.ProjectName, input.TaskID, plan.ExpectedVersion, internal, applyErr)
	if applyErr == nil {
		return result, finishDurableCommand(ctx, claim, result, nil, "", "", false)
	}
	partial := result.TaskStateCommitted
	code, stage := "RECOVERY_APPLY_FAILED", "recovery_apply"
	if partial {
		code, stage = "RECOVERY_APPLY_PARTIAL", "recovery_event_publish"
	} else if errors.Is(applyErr, ErrRecoveryPlanStale) || errors.Is(applyErr, task.ErrVersionConflict) {
		code, stage = "RECOVERY_PLAN_STALE", "recovery_apply"
	}
	return result, finishDurableCommand(ctx, claim, result, applyErr, code, stage, partial)
}

func validateCompleteTaskRecoveryApplyInput(input CompleteTaskRecoveryApplyInput) error {
	approval := input.Approval
	if strings.TrimSpace(input.VaultRoot) == "" || !canonicalProjectNameRequired(input.ProjectName) ||
		!canonicalIdentifierRequired(input.TaskID) || taskIDInvalid(input.TaskID) ||
		commandledger.ValidateCommandID(input.CommandID) != nil || approval.SchemaVersion != 1 ||
		approval.Domain != CompleteTaskPlanCommitmentDomain || !validSHA256Digest(approval.PlanCommitment) {
		return ErrInvalidCompleteTaskRecoveryApply
	}
	return nil
}

func taskIDInvalid(taskID string) bool {
	_, err := task.ParseTaskID(taskID)
	return err != nil
}

func validSHA256Digest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, current := range strings.TrimPrefix(value, "sha256:") {
		if !(current >= '0' && current <= '9') && !(current >= 'a' && current <= 'f') {
			return false
		}
	}
	return true
}

func projectCompleteTaskRecoveryResult(projectName, taskID string, expectedVersion uint64, internal recovery.Result, applyErr error) CompleteTaskRecoveryResult {
	result := CompleteTaskRecoveryResult{
		SchemaVersion: recovery.SchemaVersion, ProjectName: projectName, TaskID: taskID,
		Action: recovery.ActionCompleteTask, Status: "failed", TaskVersion: expectedVersion,
	}
	if internal.Task != nil && internal.Task.ID == taskID && internal.Task.Status == task.StatusCompleted && internal.Task.Version == expectedVersion+1 {
		result.TaskStateCommitted = true
		result.TaskVersion = internal.Task.Version
		if applyErr != nil {
			result.Status = "partial_failure"
		} else {
			result.Status = "completed"
		}
	}
	return result
}
