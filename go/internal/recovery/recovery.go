// Package recovery defines storage-neutral diagnosis and explicit recovery contracts.
package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

const SchemaVersion = 1

var (
	ErrInvalidSnapshot = errors.New("invalid recovery snapshot")
	ErrInvalidPlan     = errors.New("invalid recovery plan")
	ErrPlanStale       = errors.New("recovery plan is stale")
	ErrNotRecoverable  = errors.New("state is not safely recoverable")
)

type Action string

const (
	ActionNone         Action = "none"
	ActionCompleteTask Action = "complete_task"
	ActionFailAndHold  Action = "fail_and_hold_task"
)

func (action Action) Valid() bool {
	return action == ActionNone || action == ActionCompleteTask || action == ActionFailAndHold
}

type FindingKind string

const (
	FindingTaskCompletionPending       FindingKind = "task_completion_pending"
	FindingTaskExecutionInterrupted    FindingKind = "task_execution_interrupted"
	FindingCompletedDeliverableMissing FindingKind = "completed_task_deliverable_missing"
	FindingDeliverableConflict         FindingKind = "deliverable_task_conflict"
	FindingArtifactInvalid             FindingKind = "artifact_invalid"
	FindingReviewProjectionMissing     FindingKind = "review_projection_missing"
	FindingReviewCanonicalMissing      FindingKind = "review_canonical_missing"
	FindingRevisionTaskMissing         FindingKind = "revision_task_missing"
	FindingAuditUnverifiable           FindingKind = "audit_evidence_unverifiable"
	FindingResidualTemporaryState      FindingKind = "residual_temporary_state"
	FindingCommandIncomplete           FindingKind = "command_incomplete"
)

type Severity string

const (
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type Certainty string

const (
	CertaintyConfirmed    Certainty = "confirmed"
	CertaintyUnverifiable Certainty = "unverifiable"
)

type DeliverableEvidence struct {
	TaskID     string `json:"task_id"`
	Reference  string `json:"reference"`
	Digest     string `json:"digest"`
	Project    string `json:"project"`
	AssigneeID string `json:"assignee_id"`
	Valid      bool   `json:"valid"`
	Problem    string `json:"problem,omitempty"`
}

type ReviewEvidence struct {
	TaskID              string `json:"task_id"`
	ReviewVersion       string `json:"review_version,omitempty"`
	CanonicalReference  string `json:"canonical_reference"`
	ProjectionReference string `json:"projection_reference"`
	CanonicalExists     bool   `json:"canonical_exists"`
	ProjectionExists    bool   `json:"projection_exists"`
	CanonicalValid      bool   `json:"canonical_valid"`
	Problem             string `json:"problem,omitempty"`
}

type RevisionEvidence struct {
	RevisionTaskID string `json:"revision_task_id"`
	Reference      string `json:"reference"`
	Valid          bool   `json:"valid"`
	Problem        string `json:"problem,omitempty"`
}

type AuditEvidence struct {
	Type        event.Type `json:"type"`
	AggregateID string     `json:"aggregate_id"`
}

type ResidualEvidence struct {
	Reference string `json:"reference"`
	Kind      string `json:"kind"`
}

type CommandEvidence struct {
	CommandID   string              `json:"command_id"`
	Operation   string              `json:"operation,omitempty"`
	AggregateID string              `json:"aggregate_id,omitempty"`
	State       commandledger.State `json:"state,omitempty"`
	Reference   string              `json:"reference"`
	Valid       bool                `json:"valid"`
	Problem     string              `json:"problem,omitempty"`
}

type Snapshot struct {
	ProjectName  string                `json:"project_name"`
	Tasks        []task.Task           `json:"tasks"`
	Deliverables []DeliverableEvidence `json:"deliverables"`
	Reviews      []ReviewEvidence      `json:"reviews"`
	Revisions    []RevisionEvidence    `json:"revisions"`
	Audit        []AuditEvidence       `json:"audit"`
	AuditValid   bool                  `json:"audit_valid"`
	AuditProblem string                `json:"audit_problem,omitempty"`
	Commands     []CommandEvidence     `json:"commands"`
	Residuals    []ResidualEvidence    `json:"residuals"`
}

type SnapshotReader interface {
	Load(ctx context.Context) (Snapshot, error)
}

type Finding struct {
	ID                string      `json:"id"`
	Kind              FindingKind `json:"kind"`
	Severity          Severity    `json:"severity"`
	Certainty         Certainty   `json:"certainty"`
	TaskID            string      `json:"task_id,omitempty"`
	References        []string    `json:"references"`
	Detail            string      `json:"detail"`
	Recoverable       bool        `json:"recoverable"`
	RecommendedAction Action      `json:"recommended_action"`
}

type Report struct {
	SchemaVersion int       `json:"schema_version"`
	ProjectName   string    `json:"project_name"`
	Healthy       bool      `json:"healthy"`
	TaskCount     int       `json:"task_count"`
	Findings      []Finding `json:"findings"`
}

type PlanRequest struct {
	TaskID string
	Action Action
	Reason string
}

type Plan struct {
	SchemaVersion    int         `json:"schema_version"`
	ProjectName      string      `json:"project_name"`
	TaskID           string      `json:"task_id"`
	Action           Action      `json:"action"`
	ExpectedStatus   task.Status `json:"expected_status"`
	ExpectedVersion  uint64      `json:"expected_version"`
	EvidenceRef      string      `json:"evidence_reference,omitempty"`
	EvidenceDigest   string      `json:"evidence_digest,omitempty"`
	Reason           string      `json:"reason,omitempty"`
	SourceRevision   string      `json:"source_revision"`
	Executable       bool        `json:"executable"`
	BlockingReasons  []string    `json:"blocking_reasons"`
	ApprovalRequired bool        `json:"approval_required"`
}

func (plan Plan) Validate() error {
	if plan.SchemaVersion != SchemaVersion || strings.TrimSpace(plan.ProjectName) == "" || !plan.Action.Valid() || plan.Action == ActionNone {
		return ErrInvalidPlan
	}
	if _, err := task.ParseTaskID(plan.TaskID); err != nil || !plan.ExpectedStatus.Valid() || plan.ExpectedVersion == 0 {
		return ErrInvalidPlan
	}
	if !validSHA256Reference(plan.SourceRevision) || !plan.ApprovalRequired {
		return ErrInvalidPlan
	}
	if plan.Action == ActionCompleteTask && plan.Executable && (strings.TrimSpace(plan.EvidenceRef) == "" || !validSHA256Reference(plan.EvidenceDigest)) {
		return ErrInvalidPlan
	}
	if plan.Action == ActionFailAndHold && strings.TrimSpace(plan.Reason) == "" {
		return ErrInvalidPlan
	}
	if plan.BlockingReasons == nil {
		return ErrInvalidPlan
	}
	if plan.Executable != (len(plan.BlockingReasons) == 0) {
		return ErrInvalidPlan
	}
	return nil
}

func validSHA256Reference(reference string) bool {
	if !strings.HasPrefix(reference, "sha256:") || len(reference) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(reference, "sha256:"))
	return err == nil
}

func SamePlan(left, right Plan) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func SourceRevision(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func SortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].TaskID != findings[j].TaskID {
			return findings[i].TaskID < findings[j].TaskID
		}
		if findings[i].Kind != findings[j].Kind {
			return findings[i].Kind < findings[j].Kind
		}
		return strings.Join(findings[i].References, "\x00") < strings.Join(findings[j].References, "\x00")
	})
	for index := range findings {
		findings[index].ID = fmt.Sprintf("RECOVERY-%03d", index+1)
		if findings[index].References == nil {
			findings[index].References = []string{}
		}
	}
}

type Result struct {
	Status           string     `json:"status"`
	Action           Action     `json:"action"`
	Task             *task.Task `json:"task,omitempty"`
	FailureCommitted bool       `json:"failure_committed"`
	HoldCommitted    bool       `json:"hold_committed"`
}
