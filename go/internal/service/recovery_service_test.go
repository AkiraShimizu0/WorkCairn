package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/recovery"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

type recoverySnapshotReader struct{ snapshot recovery.Snapshot }

func (reader recoverySnapshotReader) Load(context.Context) (recovery.Snapshot, error) {
	return reader.snapshot, nil
}

func TestRecoveryInventoryFixture(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "vault", "recovery_inventory_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SchemaVersion int `json:"schema_version"`
		Cases         []struct {
			Name        string `json:"name"`
			TaskStatus  string `json:"task_status"`
			Deliverable string `json:"deliverable"`
			FindingKind string `json:"finding_kind"`
			Certainty   string `json:"certainty"`
			Action      string `json:"action"`
			Recoverable bool   `json:"recoverable"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(content, &fixture); err != nil || fixture.SchemaVersion != recovery.SchemaVersion {
		t.Fatalf("fixture = %#v, %v", fixture, err)
	}
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			assignee := "PLAN-001"
			current := task.Task{ID: "TASK-001", Title: "要件整理", AssigneeID: &assignee, Status: task.Status(test.TaskStatus), Version: 2}
			deliverables := []recovery.DeliverableEvidence{}
			if test.Deliverable == "matching" {
				deliverables = append(deliverables, recovery.DeliverableEvidence{TaskID: current.ID, Reference: "Deliverables/TASK-001.md", Digest: testRecoveryDigest("a"), Project: "P", AssigneeID: assignee, Valid: true})
			}
			snapshot := recovery.Snapshot{ProjectName: "P", Tasks: []task.Task{current}, Deliverables: deliverables, Reviews: []recovery.ReviewEvidence{}, Revisions: []recovery.RevisionEvidence{}, Audit: expectedAudit(current), AuditValid: true, Commands: []recovery.CommandEvidence{}, Residuals: []recovery.ResidualEvidence{}}
			report, err := NewRecoveryInspectionServiceMust(t, snapshot).Inspect(context.Background())
			if err != nil || len(report.Findings) != 1 {
				t.Fatalf("report = %#v, %v", report, err)
			}
			finding := report.Findings[0]
			if string(finding.Kind) != test.FindingKind || string(finding.Certainty) != test.Certainty || finding.Recoverable != test.Recoverable || string(finding.RecommendedAction) != test.Action {
				t.Fatalf("finding = %#v", finding)
			}
		})
	}
}

func TestRecoveryInspectionDiagnosesProjectionIntentAuditAndResidualWithoutGuessing(t *testing.T) {
	assignee := "PLAN-001"
	snapshot := recovery.Snapshot{
		ProjectName:  "P",
		Tasks:        []task.Task{{ID: "TASK-001", Title: "T", AssigneeID: &assignee, Status: task.StatusCompleted, Version: 3}},
		Deliverables: []recovery.DeliverableEvidence{{TaskID: "TASK-001", Reference: "Deliverables/TASK-001.md", Project: "P", AssigneeID: assignee, Valid: true, Digest: testRecoveryDigest("a")}},
		Reviews:      []recovery.ReviewEvidence{{TaskID: "TASK-001", CanonicalReference: "Reviews/TASK-001.review.json", ProjectionReference: "Reviews/TASK-001.review.md", CanonicalExists: true, CanonicalValid: true}},
		Revisions:    []recovery.RevisionEvidence{{RevisionTaskID: "TASK-002", Reference: "Revisions/TASK-002.revision.md", Valid: true}},
		Audit:        []recovery.AuditEvidence{}, AuditValid: true,
		Commands: []recovery.CommandEvidence{{
			CommandID: "CMD-001", Operation: "task.execute", AggregateID: "TASK-001",
			State: commandledger.StateRunning, Reference: ".workspace-os/commands/record.json", Valid: true,
		}},
		Residuals: []recovery.ResidualEvidence{{Reference: "プロジェクト/.workspace-os-project-X.tmp", Kind: "project_staging_directory"}},
	}
	report, err := NewRecoveryInspectionServiceMust(t, snapshot).Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[recovery.FindingKind]bool{recovery.FindingReviewProjectionMissing: false, recovery.FindingRevisionTaskMissing: false, recovery.FindingAuditUnverifiable: false, recovery.FindingResidualTemporaryState: false, recovery.FindingCommandIncomplete: false}
	for _, finding := range report.Findings {
		if _, exists := wanted[finding.Kind]; exists {
			wanted[finding.Kind] = true
			if finding.Recoverable || finding.RecommendedAction != recovery.ActionNone {
				t.Fatalf("unsafe automatic recovery offered: %#v", finding)
			}
		}
	}
	for kind, found := range wanted {
		if !found {
			t.Errorf("missing finding %s: %#v", kind, report.Findings)
		}
	}
}

func TestRecoveryInspectionRejectsDeliverableForTaskThatWasNotExecuting(t *testing.T) {
	assignee := "PLAN-001"
	for _, status := range []task.Status{task.StatusUnstarted, task.StatusOnHold} {
		t.Run(string(status), func(t *testing.T) {
			current := task.Task{ID: "TASK-001", Title: "T", AssigneeID: &assignee, Status: status, Version: 2}
			snapshot := recovery.Snapshot{
				ProjectName: "P", Tasks: []task.Task{current},
				Deliverables: []recovery.DeliverableEvidence{{TaskID: current.ID, Reference: "Deliverables/TASK-001.md", Digest: testRecoveryDigest("a"), Project: "P", AssigneeID: assignee, Valid: true}},
				Reviews:      []recovery.ReviewEvidence{}, Revisions: []recovery.RevisionEvidence{}, Audit: expectedAudit(current), AuditValid: true, Commands: []recovery.CommandEvidence{}, Residuals: []recovery.ResidualEvidence{},
			}
			report, err := NewRecoveryInspectionServiceMust(t, snapshot).Inspect(context.Background())
			if err != nil || len(report.Findings) != 1 || report.Findings[0].Kind != recovery.FindingDeliverableConflict || report.Findings[0].Recoverable {
				t.Fatalf("Inspect() = %#v, %v", report, err)
			}
		})
	}
}

func TestRecoveryPlanIsDeterministicAndEvidenceBound(t *testing.T) {
	assignee := "PLAN-001"
	snapshot := recovery.Snapshot{ProjectName: "P", Tasks: []task.Task{{ID: "TASK-001", Title: "T", AssigneeID: &assignee, Status: task.StatusInProgress, Version: 2}}, Deliverables: []recovery.DeliverableEvidence{{TaskID: "TASK-001", Reference: "Deliverables/TASK-001.md", Digest: testRecoveryDigest("a"), Project: "P", AssigneeID: assignee, Valid: true}}, Reviews: []recovery.ReviewEvidence{}, Revisions: []recovery.RevisionEvidence{}, Audit: []recovery.AuditEvidence{}, AuditValid: true, Commands: []recovery.CommandEvidence{}, Residuals: []recovery.ResidualEvidence{}}
	service := NewRecoveryInspectionServiceMust(t, snapshot)
	first, err := service.Plan(context.Background(), recovery.PlanRequest{TaskID: "TASK-001", Action: recovery.ActionCompleteTask})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Plan(context.Background(), recovery.PlanRequest{TaskID: "TASK-001", Action: recovery.ActionCompleteTask})
	if err != nil || !recovery.SamePlan(first, second) || !first.Executable || first.ExpectedVersion != 2 {
		t.Fatalf("plans = %#v %#v, %v", first, second, err)
	}
	snapshot.Deliverables[0].Digest = testRecoveryDigest("b")
	changed := NewRecoveryInspectionServiceMust(t, snapshot)
	third, err := changed.Plan(context.Background(), recovery.PlanRequest{TaskID: "TASK-001", Action: recovery.ActionCompleteTask})
	if err != nil || first.SourceRevision == third.SourceRevision {
		t.Fatalf("changed plan = %#v, %v", third, err)
	}
}

type recoveryLifecycle struct {
	current                                   task.Task
	completeVersion, failVersion, holdVersion uint64
	failPublication                           bool
}

func (lifecycle *recoveryLifecycle) CompleteExpected(_ context.Context, _ string, expected uint64) (task.Task, error) {
	lifecycle.completeVersion = expected
	next, err := lifecycle.current.Complete()
	lifecycle.current = next
	return next, err
}
func (lifecycle *recoveryLifecycle) FailExpected(_ context.Context, _ string, reason string, expected uint64) (task.Task, error) {
	lifecycle.failVersion = expected
	next, err := lifecycle.current.Fail(reason)
	lifecycle.current = next
	if err == nil && lifecycle.failPublication {
		return next, &EventPublicationError{Task: next, EventType: event.TaskFailed, EventID: "event-fail", Err: errors.New("audit unavailable")}
	}
	return next, err
}
func (lifecycle *recoveryLifecycle) HoldExpected(_ context.Context, _ string, reason string, expected uint64) (task.Task, error) {
	lifecycle.holdVersion = expected
	next, err := lifecycle.current.Hold(reason)
	lifecycle.current = next
	return next, err
}

func TestTaskRecoveryContinuesToHoldAfterCommittedFailurePublicationError(t *testing.T) {
	assignee := "PLAN-001"
	lifecycle := &recoveryLifecycle{current: task.Task{ID: "TASK-001", Title: "T", AssigneeID: &assignee, Status: task.StatusInProgress, Version: 2}, failPublication: true}
	service, _ := NewTaskRecoveryService(lifecycle)
	plan := recovery.Plan{SchemaVersion: 1, ProjectName: "P", TaskID: "TASK-001", Action: recovery.ActionFailAndHold, ExpectedStatus: task.StatusInProgress, ExpectedVersion: 2, Reason: "process interrupted", SourceRevision: testRecoveryDigest("0"), Executable: true, BlockingReasons: []string{}, ApprovalRequired: true}
	result, err := service.Execute(context.Background(), plan)
	if err == nil || !result.FailureCommitted || !result.HoldCommitted || result.Task == nil || result.Task.Status != task.StatusOnHold || lifecycle.failVersion != 2 || lifecycle.holdVersion != 3 {
		t.Fatalf("result=%#v err=%v lifecycle=%#v", result, err, lifecycle)
	}
}

func testRecoveryDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func NewRecoveryInspectionServiceMust(t *testing.T, snapshot recovery.Snapshot) *RecoveryInspectionService {
	t.Helper()
	service, err := NewRecoveryInspectionService(recoverySnapshotReader{snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func expectedAudit(current task.Task) []recovery.AuditEvidence {
	var eventType event.Type
	switch current.Status {
	case task.StatusCompleted:
		eventType = event.TaskCompleted
	case task.StatusInProgress:
		if current.LastFailureReason != "" {
			eventType = event.TaskFailed
		} else {
			eventType = event.TaskStarted
		}
	case task.StatusOnHold:
		eventType = event.TaskHeld
	case task.StatusUnstarted:
		if current.Version == 1 {
			eventType = event.TaskCreated
		} else {
			eventType = event.TaskResumed
		}
	default:
		eventType = event.TaskCreated
	}
	return []recovery.AuditEvidence{{Type: eventType, AggregateID: current.ID}}
}
