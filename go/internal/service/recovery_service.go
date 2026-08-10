package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/recovery"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

type RecoveryInspectionService struct{ reader recovery.SnapshotReader }

func NewRecoveryInspectionService(reader recovery.SnapshotReader) (*RecoveryInspectionService, error) {
	if serviceDependencyIsNil(reader) {
		return nil, fmt.Errorf("Recovery Snapshot reader is required")
	}
	return &RecoveryInspectionService{reader: reader}, nil
}

func (service *RecoveryInspectionService) Inspect(ctx context.Context) (recovery.Report, error) {
	snapshot, err := service.reader.Load(ctx)
	if err != nil {
		return recovery.Report{}, err
	}
	return inspectRecoverySnapshot(snapshot)
}

func (service *RecoveryInspectionService) Plan(ctx context.Context, request recovery.PlanRequest) (recovery.Plan, error) {
	snapshot, err := service.reader.Load(ctx)
	if err != nil {
		return recovery.Plan{}, err
	}
	request.TaskID = strings.TrimSpace(request.TaskID)
	request.Reason = strings.TrimSpace(request.Reason)
	if _, err := task.ParseTaskID(request.TaskID); err != nil || !request.Action.Valid() || request.Action == recovery.ActionNone {
		return recovery.Plan{}, recovery.ErrInvalidPlan
	}
	var current *task.Task
	for index := range snapshot.Tasks {
		if snapshot.Tasks[index].ID == request.TaskID {
			cloned := snapshot.Tasks[index].Clone()
			current = &cloned
			break
		}
	}
	if current == nil {
		return recovery.Plan{}, task.ErrTaskNotFound
	}
	plan := recovery.Plan{
		SchemaVersion: recovery.SchemaVersion, ProjectName: snapshot.ProjectName,
		TaskID: current.ID, Action: request.Action, ExpectedStatus: current.Status,
		ExpectedVersion: current.Version, Reason: request.Reason,
		BlockingReasons: []string{}, ApprovalRequired: true,
	}
	deliverables := deliverablesForTask(snapshot.Deliverables, current.ID)
	switch request.Action {
	case recovery.ActionCompleteTask:
		if current.Status != task.StatusInProgress {
			plan.BlockingReasons = append(plan.BlockingReasons, "task_not_in_progress")
		}
		if len(deliverables) != 1 || !deliverables[0].Valid || deliverables[0].Project != snapshot.ProjectName || current.AssigneeID == nil || deliverables[0].AssigneeID != *current.AssigneeID {
			plan.BlockingReasons = append(plan.BlockingReasons, "matching_deliverable_not_confirmed")
		} else {
			plan.EvidenceRef = deliverables[0].Reference
			plan.EvidenceDigest = deliverables[0].Digest
		}
	case recovery.ActionFailAndHold:
		if current.Status != task.StatusInProgress {
			plan.BlockingReasons = append(plan.BlockingReasons, "task_not_in_progress")
		}
		if len(deliverables) != 0 {
			plan.BlockingReasons = append(plan.BlockingReasons, "deliverable_present_or_invalid")
		}
		if request.Reason == "" {
			plan.BlockingReasons = append(plan.BlockingReasons, "failure_reason_required")
		}
	}
	plan.Executable = len(plan.BlockingReasons) == 0
	revisionSource := struct {
		ProjectName string                         `json:"project_name"`
		Task        task.Task                      `json:"task"`
		Action      recovery.Action                `json:"action"`
		Reason      string                         `json:"reason,omitempty"`
		Evidence    []recovery.DeliverableEvidence `json:"deliverable_evidence"`
	}{snapshot.ProjectName, current.Clone(), request.Action, request.Reason, deliverables}
	plan.SourceRevision, err = recovery.SourceRevision(revisionSource)
	if err != nil {
		return recovery.Plan{}, err
	}
	return plan, plan.Validate()
}

func inspectRecoverySnapshot(snapshot recovery.Snapshot) (recovery.Report, error) {
	if strings.TrimSpace(snapshot.ProjectName) == "" || snapshot.Tasks == nil || snapshot.Deliverables == nil || snapshot.Reviews == nil || snapshot.Revisions == nil || snapshot.Audit == nil || snapshot.Commands == nil || snapshot.Residuals == nil {
		return recovery.Report{}, recovery.ErrInvalidSnapshot
	}
	tasks := make(map[string]task.Task, len(snapshot.Tasks))
	for _, current := range snapshot.Tasks {
		if err := current.Validate(); err != nil {
			return recovery.Report{}, recovery.ErrInvalidSnapshot
		}
		tasks[current.ID] = current
	}
	findings := make([]recovery.Finding, 0)
	for _, current := range snapshot.Tasks {
		deliverables := deliverablesForTask(snapshot.Deliverables, current.ID)
		validMatching := len(deliverables) == 1 && deliverables[0].Valid && deliverables[0].Project == snapshot.ProjectName && current.AssigneeID != nil && deliverables[0].AssigneeID == *current.AssigneeID
		references := deliverableReferences(deliverables)
		switch {
		case current.Status == task.StatusInProgress && validMatching:
			findings = append(findings, finding(recovery.FindingTaskCompletionPending, recovery.SeverityWarning, recovery.CertaintyConfirmed, current.ID, references, "Deliverable is committed while Task completion is not committed", true, recovery.ActionCompleteTask))
		case current.Status == task.StatusInProgress && len(deliverables) == 0:
			findings = append(findings, finding(recovery.FindingTaskExecutionInterrupted, recovery.SeverityWarning, recovery.CertaintyConfirmed, current.ID, nil, "Task is in progress without a committed Deliverable; Provider outcome cannot be reconstructed", true, recovery.ActionFailAndHold))
		case current.Status == task.StatusCompleted && len(deliverables) == 0:
			findings = append(findings, finding(recovery.FindingCompletedDeliverableMissing, recovery.SeverityCritical, recovery.CertaintyConfirmed, current.ID, nil, "Completed Task has no immutable Deliverable", false, recovery.ActionNone))
		case current.Status == task.StatusCompleted && validMatching:
			// The canonical commit ordering is fully represented.
		case len(deliverables) > 0:
			findings = append(findings, finding(recovery.FindingDeliverableConflict, recovery.SeverityCritical, recovery.CertaintyConfirmed, current.ID, references, "Deliverable evidence conflicts with Task status, identity, or assignee", false, recovery.ActionNone))
		}
	}
	for _, evidence := range snapshot.Deliverables {
		if !evidence.Valid {
			findings = append(findings, finding(recovery.FindingArtifactInvalid, recovery.SeverityCritical, recovery.CertaintyConfirmed, evidence.TaskID, []string{evidence.Reference}, evidence.Problem, false, recovery.ActionNone))
		} else if _, exists := tasks[evidence.TaskID]; !exists {
			findings = append(findings, finding(recovery.FindingDeliverableConflict, recovery.SeverityCritical, recovery.CertaintyConfirmed, evidence.TaskID, []string{evidence.Reference}, "Deliverable references a Task that is not present", false, recovery.ActionNone))
		}
	}
	for _, evidence := range snapshot.Reviews {
		switch {
		case evidence.CanonicalExists && !evidence.CanonicalValid:
			findings = append(findings, finding(recovery.FindingArtifactInvalid, recovery.SeverityCritical, recovery.CertaintyConfirmed, evidence.TaskID, []string{evidence.CanonicalReference}, evidence.Problem, false, recovery.ActionNone))
		case evidence.CanonicalExists && !evidence.ProjectionExists:
			findings = append(findings, finding(recovery.FindingReviewProjectionMissing, recovery.SeverityWarning, recovery.CertaintyConfirmed, evidence.TaskID, []string{evidence.CanonicalReference, evidence.ProjectionReference}, "Canonical Review is committed but human-readable projection is missing; canonical JSON alone cannot reconstruct the original Markdown", false, recovery.ActionNone))
		case !evidence.CanonicalExists && evidence.ProjectionExists:
			findings = append(findings, finding(recovery.FindingReviewCanonicalMissing, recovery.SeverityCritical, recovery.CertaintyConfirmed, evidence.TaskID, []string{evidence.ProjectionReference}, "Review projection exists without canonical Review evidence", false, recovery.ActionNone))
		}
	}
	for _, evidence := range snapshot.Revisions {
		if !evidence.Valid {
			findings = append(findings, finding(recovery.FindingArtifactInvalid, recovery.SeverityCritical, recovery.CertaintyConfirmed, evidence.RevisionTaskID, []string{evidence.Reference}, evidence.Problem, false, recovery.ActionNone))
		} else if _, exists := tasks[evidence.RevisionTaskID]; !exists {
			findings = append(findings, finding(recovery.FindingRevisionTaskMissing, recovery.SeverityWarning, recovery.CertaintyConfirmed, evidence.RevisionTaskID, []string{evidence.Reference}, "Immutable Revision intent is committed but the Revision Task is not committed", false, recovery.ActionNone))
		}
	}
	if !snapshot.AuditValid {
		findings = append(findings, finding(recovery.FindingArtifactInvalid, recovery.SeverityCritical, recovery.CertaintyConfirmed, "", []string{"Audit Log.md"}, snapshot.AuditProblem, false, recovery.ActionNone))
	} else {
		findings = append(findings, missingAuditFindings(snapshot, tasks)...)
	}
	for _, evidence := range snapshot.Commands {
		if !evidence.Valid {
			findings = append(findings, finding(recovery.FindingArtifactInvalid, recovery.SeverityCritical, recovery.CertaintyConfirmed, evidence.AggregateID, []string{evidence.Reference}, evidence.Problem, false, recovery.ActionNone))
		} else if evidence.State == commandledger.StateRunning {
			findings = append(findings, finding(recovery.FindingCommandIncomplete, recovery.SeverityWarning, recovery.CertaintyConfirmed, evidence.AggregateID, []string{evidence.Reference}, "Command claim is committed without a terminal outcome; automatic resume is prohibited", false, recovery.ActionNone))
		}
	}
	for _, residual := range snapshot.Residuals {
		findings = append(findings, finding(recovery.FindingResidualTemporaryState, recovery.SeverityWarning, recovery.CertaintyConfirmed, "", []string{residual.Reference}, "Residual "+residual.Kind+" may be left by a stopped process; active ownership cannot be inferred", false, recovery.ActionNone))
	}
	recovery.SortFindings(findings)
	return recovery.Report{SchemaVersion: recovery.SchemaVersion, ProjectName: snapshot.ProjectName, Healthy: len(findings) == 0, TaskCount: len(snapshot.Tasks), Findings: findings}, nil
}

func missingAuditFindings(snapshot recovery.Snapshot, tasks map[string]task.Task) []recovery.Finding {
	observed := make(map[string]bool, len(snapshot.Audit))
	for _, evidence := range snapshot.Audit {
		observed[string(evidence.Type)+"\x00"+evidence.AggregateID] = true
	}
	findings := make([]recovery.Finding, 0)
	for _, current := range tasks {
		var expected event.Type
		switch current.Status {
		case task.StatusInProgress:
			if current.LastFailureReason != "" {
				expected = event.TaskFailed
			} else {
				expected = event.TaskStarted
			}
		case task.StatusCompleted:
			expected = event.TaskCompleted
		case task.StatusOnHold:
			expected = event.TaskHeld
		case task.StatusUnstarted:
			if current.Version == 1 {
				expected = event.TaskCreated
			} else {
				expected = event.TaskResumed
			}
		}
		if !observed[string(expected)+"\x00"+current.ID] {
			findings = append(findings, finding(recovery.FindingAuditUnverifiable, recovery.SeverityWarning, recovery.CertaintyUnverifiable, current.ID, []string{"Audit Log.md"}, "Expected lifecycle Audit evidence is absent; Event publication failure and Audit subscriber failure cannot be distinguished", false, recovery.ActionNone))
		}
	}
	for _, evidence := range snapshot.Reviews {
		if evidence.CanonicalExists && evidence.CanonicalValid && !observed[string(event.ReviewCompleted)+"\x00"+evidence.TaskID] {
			findings = append(findings, finding(recovery.FindingAuditUnverifiable, recovery.SeverityWarning, recovery.CertaintyUnverifiable, evidence.TaskID, []string{evidence.CanonicalReference, "Audit Log.md"}, "Canonical Review exists without observable Review Audit evidence; publication state cannot be inferred", false, recovery.ActionNone))
		}
	}
	for _, evidence := range snapshot.Revisions {
		if evidence.Valid {
			if _, exists := tasks[evidence.RevisionTaskID]; exists && !observed[string(event.RevisionCreated)+"\x00"+evidence.RevisionTaskID] {
				findings = append(findings, finding(recovery.FindingAuditUnverifiable, recovery.SeverityWarning, recovery.CertaintyUnverifiable, evidence.RevisionTaskID, []string{evidence.Reference, "Audit Log.md"}, "Revision intent and Task exist without observable Revision Audit evidence; publication state cannot be inferred", false, recovery.ActionNone))
			}
		}
	}
	return findings
}

func deliverablesForTask(all []recovery.DeliverableEvidence, taskID string) []recovery.DeliverableEvidence {
	result := make([]recovery.DeliverableEvidence, 0, 1)
	for _, evidence := range all {
		if evidence.TaskID == taskID {
			result = append(result, evidence)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Reference < result[j].Reference })
	return result
}

func deliverableReferences(evidence []recovery.DeliverableEvidence) []string {
	references := make([]string, 0, len(evidence))
	for _, item := range evidence {
		references = append(references, item.Reference)
	}
	return references
}

func finding(kind recovery.FindingKind, severity recovery.Severity, certainty recovery.Certainty, taskID string, references []string, detail string, recoverable bool, action recovery.Action) recovery.Finding {
	if references == nil {
		references = []string{}
	}
	return recovery.Finding{Kind: kind, Severity: severity, Certainty: certainty, TaskID: taskID, References: references, Detail: detail, Recoverable: recoverable, RecommendedAction: action}
}

type ExpectedTaskRecovery interface {
	CompleteExpected(ctx context.Context, taskID string, expectedVersion uint64) (task.Task, error)
	FailExpected(ctx context.Context, taskID, reason string, expectedVersion uint64) (task.Task, error)
	HoldExpected(ctx context.Context, taskID, reason string, expectedVersion uint64) (task.Task, error)
}

type TaskRecoveryService struct{ tasks ExpectedTaskRecovery }

func NewTaskRecoveryService(tasks ExpectedTaskRecovery) (*TaskRecoveryService, error) {
	if serviceDependencyIsNil(tasks) {
		return nil, fmt.Errorf("Task recovery lifecycle is required")
	}
	return &TaskRecoveryService{tasks: tasks}, nil
}

type RecoveryExecutionError struct {
	Result recovery.Result
	Errors []error
}

func (executionError *RecoveryExecutionError) Error() string {
	return "explicit Task recovery did not fully complete"
}
func (executionError *RecoveryExecutionError) Unwrap() []error { return executionError.Errors }

func (service *TaskRecoveryService) Execute(ctx context.Context, plan recovery.Plan) (recovery.Result, error) {
	result := recovery.Result{Status: "failed", Action: plan.Action}
	if err := plan.Validate(); err != nil || !plan.Executable {
		if err == nil {
			err = recovery.ErrNotRecoverable
		}
		return result, err
	}
	switch plan.Action {
	case recovery.ActionCompleteTask:
		completed, err := service.tasks.CompleteExpected(ctx, plan.TaskID, plan.ExpectedVersion)
		if committedTask(completed, err) {
			cloned := completed.Clone()
			result.Task = &cloned
		}
		if err != nil {
			if result.Task != nil {
				result.Status = "partial_failure"
			}
			return result, &RecoveryExecutionError{Result: result, Errors: []error{err}}
		}
		result.Status = "completed"
		return result, nil
	case recovery.ActionFailAndHold:
		failed, failErr := service.tasks.FailExpected(ctx, plan.TaskID, plan.Reason, plan.ExpectedVersion)
		if !committedTask(failed, failErr) {
			return result, &RecoveryExecutionError{Result: result, Errors: nonNilErrors(failErr)}
		}
		result.FailureCommitted = true
		held, holdErr := service.tasks.HoldExpected(ctx, plan.TaskID, "manual recovery: "+plan.Reason, failed.Version)
		if committedTask(held, holdErr) {
			result.HoldCommitted = true
			cloned := held.Clone()
			result.Task = &cloned
		}
		errorsFound := nonNilErrors(failErr, holdErr)
		if len(errorsFound) > 0 || !result.HoldCommitted {
			result.Status = "partial_failure"
			if len(errorsFound) == 0 {
				errorsFound = append(errorsFound, errors.New("Task hold was not committed"))
			}
			return result, &RecoveryExecutionError{Result: result, Errors: errorsFound}
		}
		result.Status = "held"
		return result, nil
	default:
		return result, recovery.ErrInvalidPlan
	}
}

func committedTask(current task.Task, err error) bool {
	if err == nil {
		return current.ID != ""
	}
	var publicationError *EventPublicationError
	return errors.As(err, &publicationError) && publicationError.Task.ID == current.ID && current.ID != ""
}

func nonNilErrors(candidates ...error) []error {
	result := make([]error, 0, len(candidates))
	for _, err := range candidates {
		if err != nil {
			result = append(result, err)
		}
	}
	return result
}
