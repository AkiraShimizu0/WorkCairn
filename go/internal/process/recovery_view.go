package process

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/recovery"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

// ErrUnsupportedRecoveryProjection is the single closed sentinel for every
// way a recovery.Report, or the raw input used to produce one, fails the
// public-safe projection boundary: a schema/shape mismatch, a non-canonical
// identifier, or a Finding whose enum fields or Recoverable/RecommendedAction
// pairing falls outside the closed set this file validates against
// go/internal/recovery. It never carries the rejected value or an internal
// cause, so it is always safe to return to an HTTP client verbatim.
var ErrUnsupportedRecoveryProjection = errors.New("recovery report cannot be safely projected")

const MaxRecoveryPlanPreviewReasonBytes = 16 << 10

var (
	ErrInvalidRecoveryPlanPreviewInput = errors.New("invalid recovery plan preview input")
	ErrUnsupportedRecoveryPlanPreview  = errors.New("recovery plan cannot be safely previewed")
)

type RecoveryPlanBlockingReason string

const (
	RecoveryPlanBlockingTaskNotInProgress               RecoveryPlanBlockingReason = "task_not_in_progress"
	RecoveryPlanBlockingMatchingDeliverableNotConfirmed RecoveryPlanBlockingReason = "matching_deliverable_not_confirmed"
	RecoveryPlanBlockingDeliverablePresentOrInvalid     RecoveryPlanBlockingReason = "deliverable_present_or_invalid"
)

type RecoveryPlanPreviewView struct {
	SchemaVersion   int                          `json:"schema_version"`
	ProjectName     string                       `json:"project_name"`
	TaskID          string                       `json:"task_id"`
	Action          recovery.Action              `json:"action"`
	TaskStatus      task.Status                  `json:"task_status"`
	TaskVersion     uint64                       `json:"task_version"`
	Executable      bool                         `json:"executable"`
	BlockingReasons []RecoveryPlanBlockingReason `json:"blocking_reasons"`
}

type recoveryPlanner func(context.Context, RecoveryInput, recovery.PlanRequest) (recovery.Plan, error)

var completeTaskBlockingOrder = []RecoveryPlanBlockingReason{
	RecoveryPlanBlockingTaskNotInProgress,
	RecoveryPlanBlockingMatchingDeliverableNotConfirmed,
}

var failAndHoldBlockingOrder = []RecoveryPlanBlockingReason{
	RecoveryPlanBlockingTaskNotInProgress,
	RecoveryPlanBlockingDeliverablePresentOrInvalid,
}

// RecoveryFindingView is the public-safe wire shape of one recovery.Finding:
// closed enums and minimal identifiers only. recovery.Finding's free-text
// Detail and Vault-relative References are deliberately absent from this
// struct -- never read into it in the first place, not filtered out after
// the fact -- so a future recovery.SnapshotReader that puts unsafe content
// in those fields still cannot leak through this type. RelatedID mirrors
// Finding.TaskID, renamed because that field is not always a Task ID (e.g.
// FindingCommandIncomplete carries a Command Ledger aggregate ID there).
type RecoveryFindingView struct {
	ID                string               `json:"id"`
	Kind              recovery.FindingKind `json:"kind"`
	Severity          recovery.Severity    `json:"severity"`
	Certainty         recovery.Certainty   `json:"certainty"`
	RelatedID         string               `json:"related_id,omitempty"`
	Recoverable       bool                 `json:"recoverable"`
	RecommendedAction recovery.Action      `json:"recommended_action"`
}

// RecoveryInspectionView is the public-safe wire shape of one recovery.Report.
type RecoveryInspectionView struct {
	SchemaVersion int                   `json:"schema_version"`
	ProjectName   string                `json:"project_name"`
	Healthy       bool                  `json:"healthy"`
	TaskCount     int                   `json:"task_count"`
	Findings      []RecoveryFindingView `json:"findings"`
}

// canonicalIdentifierRequired reports whether value is a non-empty canonical
// identifier: unchanged by strings.TrimSpace and free of embedded CR/LF.
func canonicalIdentifierRequired(value string) bool {
	return value != "" && canonicalIdentifierOptional(value)
}

// canonicalIdentifierOptional reports whether value is either empty or a
// canonical identifier per canonicalIdentifierRequired's rule.
func canonicalIdentifierOptional(value string) bool {
	return value == "" || (value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n"))
}

// canonicalProjectNameRequired adds the Vault path-segment constraints to the
// public identifier rule. Keeping this check in Process ensures client input
// is rejected before a Vault adapter is constructed or filesystem I/O begins.
func canonicalProjectNameRequired(value string) bool {
	return canonicalIdentifierRequired(value) && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00")
}

var validRecoveryFindingKinds = map[recovery.FindingKind]bool{
	recovery.FindingTaskCompletionPending:       true,
	recovery.FindingTaskExecutionInterrupted:    true,
	recovery.FindingCompletedDeliverableMissing: true,
	recovery.FindingDeliverableConflict:         true,
	recovery.FindingArtifactInvalid:             true,
	recovery.FindingReviewProjectionMissing:     true,
	recovery.FindingReviewCanonicalMissing:      true,
	recovery.FindingRevisionTaskMissing:         true,
	recovery.FindingAuditUnverifiable:           true,
	recovery.FindingResidualTemporaryState:      true,
	recovery.FindingCommandIncomplete:           true,
}

// ProjectRecoveryInspectionView is the pure, side-effect-free safe-projection
// boundary from an internal recovery.Report to its public wire shape. It
// never reads Finding.Detail or Finding.References, and fail-closed rejects
// -- rather than passing through unchanged, and rather than silently
// normalizing -- any Report or Finding outside the closed set validated here.
func ProjectRecoveryInspectionView(report recovery.Report) (RecoveryInspectionView, error) {
	if report.SchemaVersion != recovery.SchemaVersion ||
		!canonicalIdentifierRequired(report.ProjectName) ||
		report.TaskCount < 0 ||
		report.Healthy != (len(report.Findings) == 0) {
		return RecoveryInspectionView{}, ErrUnsupportedRecoveryProjection
	}
	seen := make(map[string]bool, len(report.Findings))
	views := make([]RecoveryFindingView, 0, len(report.Findings))
	for _, current := range report.Findings {
		if !canonicalIdentifierRequired(current.ID) || seen[current.ID] {
			return RecoveryInspectionView{}, ErrUnsupportedRecoveryProjection
		}
		seen[current.ID] = true
		view, err := projectRecoveryFinding(current)
		if err != nil {
			return RecoveryInspectionView{}, err
		}
		views = append(views, view)
	}
	return RecoveryInspectionView{
		SchemaVersion: report.SchemaVersion, ProjectName: report.ProjectName,
		Healthy: report.Healthy, TaskCount: report.TaskCount, Findings: views,
	}, nil
}

func projectRecoveryFinding(current recovery.Finding) (RecoveryFindingView, error) {
	if !validRecoveryFindingKinds[current.Kind] ||
		(current.Severity != recovery.SeverityWarning && current.Severity != recovery.SeverityCritical) ||
		(current.Certainty != recovery.CertaintyConfirmed && current.Certainty != recovery.CertaintyUnverifiable) ||
		!current.RecommendedAction.Valid() ||
		(current.Recoverable && current.RecommendedAction == recovery.ActionNone) ||
		(!current.Recoverable && current.RecommendedAction != recovery.ActionNone) ||
		!canonicalIdentifierOptional(current.TaskID) {
		return RecoveryFindingView{}, ErrUnsupportedRecoveryProjection
	}
	return RecoveryFindingView{
		ID: current.ID, Kind: current.Kind, Severity: current.Severity, Certainty: current.Certainty,
		RelatedID: current.TaskID, Recoverable: current.Recoverable, RecommendedAction: current.RecommendedAction,
	}, nil
}

// InspectRecoveryView is the public-safe process boundary for HTTP: it
// validates the raw, untrimmed input.ProjectName before InspectRecovery (and
// therefore before any Vault reader construction or filesystem read) ever
// runs, then projects the resulting Report through the safe projector.
// InspectRecovery itself -- used by the operator-facing recovery-inspect CLI
// operation -- is unchanged and keeps trimming its input for that path.
func InspectRecoveryView(ctx context.Context, input RecoveryInput) (RecoveryInspectionView, error) {
	if !canonicalIdentifierRequired(input.ProjectName) {
		return RecoveryInspectionView{}, ErrUnsupportedRecoveryProjection
	}
	report, err := InspectRecovery(ctx, input)
	if err != nil {
		return RecoveryInspectionView{}, err
	}
	return ProjectRecoveryInspectionView(report)
}

// PlanTaskRecoveryView validates the raw public input before any Vault read,
// delegates to the existing ADR-0020 planner, and projects the result into a
// closed public shape. It is a preview only: it performs no Task mutation,
// approval, Command Ledger claim, Provider call, or retry.
func PlanTaskRecoveryView(ctx context.Context, input RecoveryInput, request recovery.PlanRequest) (RecoveryPlanPreviewView, error) {
	return planTaskRecoveryView(ctx, input, request, PlanTaskRecovery)
}

func planTaskRecoveryView(ctx context.Context, input RecoveryInput, request recovery.PlanRequest, planner recoveryPlanner) (RecoveryPlanPreviewView, error) {
	if !canonicalProjectNameRequired(input.ProjectName) ||
		!canonicalIdentifierRequired(request.TaskID) {
		return RecoveryPlanPreviewView{}, ErrInvalidRecoveryPlanPreviewInput
	}
	if _, err := task.ParseTaskID(request.TaskID); err != nil {
		return RecoveryPlanPreviewView{}, ErrInvalidRecoveryPlanPreviewInput
	}
	switch request.Action {
	case recovery.ActionCompleteTask:
		if request.Reason != "" {
			return RecoveryPlanPreviewView{}, ErrInvalidRecoveryPlanPreviewInput
		}
	case recovery.ActionFailAndHold:
		if request.Reason == "" ||
			request.Reason != strings.TrimSpace(request.Reason) ||
			len(request.Reason) > MaxRecoveryPlanPreviewReasonBytes {
			return RecoveryPlanPreviewView{}, ErrInvalidRecoveryPlanPreviewInput
		}
	default:
		return RecoveryPlanPreviewView{}, ErrInvalidRecoveryPlanPreviewInput
	}

	plan, err := planner(ctx, input, request)
	if err != nil {
		return RecoveryPlanPreviewView{}, fmt.Errorf("plan task recovery preview: %w", err)
	}
	if plan.ProjectName != input.ProjectName || plan.TaskID != request.TaskID || plan.Action != request.Action || plan.Reason != request.Reason {
		return RecoveryPlanPreviewView{}, ErrUnsupportedRecoveryPlanPreview
	}
	return ProjectRecoveryPlanView(plan)
}

// ProjectRecoveryPlanView is the pure safe-projection boundary for an
// internal recovery.Plan. Hidden evidence, source revision, approval state,
// and failure reason are validated where they affect trust in the public
// result, but are never copied into the returned view.
func ProjectRecoveryPlanView(plan recovery.Plan) (RecoveryPlanPreviewView, error) {
	if plan.Validate() != nil ||
		!canonicalProjectNameRequired(plan.ProjectName) ||
		!canonicalIdentifierRequired(plan.TaskID) {
		return RecoveryPlanPreviewView{}, ErrUnsupportedRecoveryPlanPreview
	}

	blockingReasons, ok := validateOrderedBlockingReasons(plan.BlockingReasons, plan.Action)
	if !ok {
		return RecoveryPlanPreviewView{}, ErrUnsupportedRecoveryPlanPreview
	}
	switch plan.Action {
	case recovery.ActionCompleteTask:
		if plan.Reason != "" {
			return RecoveryPlanPreviewView{}, ErrUnsupportedRecoveryPlanPreview
		}
	case recovery.ActionFailAndHold:
		if plan.EvidenceRef != "" || plan.EvidenceDigest != "" ||
			plan.Reason == "" || plan.Reason != strings.TrimSpace(plan.Reason) ||
			len(plan.Reason) > MaxRecoveryPlanPreviewReasonBytes {
			return RecoveryPlanPreviewView{}, ErrUnsupportedRecoveryPlanPreview
		}
	default:
		return RecoveryPlanPreviewView{}, ErrUnsupportedRecoveryPlanPreview
	}

	return RecoveryPlanPreviewView{
		SchemaVersion:   plan.SchemaVersion,
		ProjectName:     plan.ProjectName,
		TaskID:          plan.TaskID,
		Action:          plan.Action,
		TaskStatus:      plan.ExpectedStatus,
		TaskVersion:     plan.ExpectedVersion,
		Executable:      plan.Executable,
		BlockingReasons: blockingReasons,
	}, nil
}

func validateOrderedBlockingReasons(raw []string, action recovery.Action) ([]RecoveryPlanBlockingReason, bool) {
	if raw == nil {
		return nil, false
	}
	var canonicalOrder []RecoveryPlanBlockingReason
	switch action {
	case recovery.ActionCompleteTask:
		canonicalOrder = completeTaskBlockingOrder
	case recovery.ActionFailAndHold:
		canonicalOrder = failAndHoldBlockingOrder
	default:
		return nil, false
	}

	seen := make(map[RecoveryPlanBlockingReason]bool, len(raw))
	for _, item := range raw {
		typed := RecoveryPlanBlockingReason(item)
		allowed := false
		for _, candidate := range canonicalOrder {
			if typed == candidate {
				allowed = true
				break
			}
		}
		if !allowed || seen[typed] {
			return nil, false
		}
		seen[typed] = true
	}

	expected := make([]RecoveryPlanBlockingReason, 0, len(canonicalOrder))
	for _, candidate := range canonicalOrder {
		if seen[candidate] {
			expected = append(expected, candidate)
		}
	}
	if len(expected) != len(raw) {
		return nil, false
	}
	for index, candidate := range expected {
		if raw[index] != string(candidate) {
			return nil, false
		}
	}
	return expected, true
}
