package vault

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/deliverable"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/execution"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/revision"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/service"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/worker"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/workflow"
)

func TestDependencyEvidenceCollectorUsesDirectDependenciesInCanonicalOrderAndLatestRevision(t *testing.T) {
	root, _ := emptyManagedVault(t)
	store := newTestTaskStore(t, root)
	deliverables := newTestDeliverableStore(t, root)
	at := time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC)
	createCompletedEvidenceTask(t, store, deliverables, "TASK-001", "市場調査", "RESEARCH-001", "市場A")
	createCompletedEvidenceTask(t, store, deliverables, "TASK-002", "競合調査", "RESEARCH-002", "古い競合B")
	createCompletedEvidenceTask(t, store, deliverables, "TASK-003", "顧客調査", "RESEARCH-003", "顧客C")
	createCompletedEvidenceTask(t, store, deliverables, "TASK-004", "統合", "PLAN-001", "既存統合")
	createCompletedEvidenceTask(t, store, deliverables, "TASK-005", "競合調査を修正", "RESEARCH-002", "最新版の競合B")
	createCompletedEvidenceTask(t, store, deliverables, "TASK-006", "非依存の調査", "RESEARCH-004", "含めてはいけないD")
	saveRevisionEvidenceReference(t, root, "TASK-002", "TASK-005", at)

	collector, err := NewDependencyEvidenceCollector(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	request := dependencyEvidenceRequest([]string{"TASK-003", "TASK-001", "TASK-002"})
	evidence, err := collector.Collect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{evidence[0].SourceTaskID, evidence[1].SourceTaskID, evidence[2].SourceTaskID}; !reflect.DeepEqual(got, []string{"TASK-003", "TASK-001", "TASK-002"}) {
		t.Fatalf("canonical dependency order = %v", got)
	}
	if evidence[2].TaskID != "TASK-005" || evidence[2].Content != "最新版の競合B" ||
		evidence[2].SourceTitle != "競合調査" || evidence[2].Title != "競合調査を修正" {
		t.Fatalf("latest revision evidence = %#v", evidence[2])
	}
	for _, current := range evidence {
		if current.TaskID == "TASK-006" || current.Content == "含めてはいけないD" || current.Content == "古い競合B" {
			t.Fatalf("collector included stale/non-dependent evidence: %#v", current)
		}
	}
}

func TestDependencyEvidenceCollectorFailsClosedForPendingRevisionOrMissingDeliverable(t *testing.T) {
	for _, test := range []struct {
		name   string
		setup  func(*testing.T, string, *TaskStore, *DeliverableStore)
		reason string
	}{
		{
			name: "pending revision",
			setup: func(t *testing.T, root string, store *TaskStore, deliverables *DeliverableStore) {
				createCompletedEvidenceTask(t, store, deliverables, "TASK-001", "市場調査", "RESEARCH-001", "市場A")
				createUnstartedEvidenceTask(t, store, "TASK-005", "市場調査を修正", "RESEARCH-001")
				saveRevisionEvidenceReference(t, root, "TASK-001", "TASK-005", time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC))
			},
			reason: "revision_task_incomplete",
		},
		{
			name: "missing deliverable",
			setup: func(t *testing.T, _ string, store *TaskStore, _ *DeliverableStore) {
				createCompletedTaskWithoutDeliverable(t, store, "TASK-001", "市場調査", "RESEARCH-001")
			},
			reason: "deliverable_missing",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, _ := emptyManagedVault(t)
			store := newTestTaskStore(t, root)
			deliverables := newTestDeliverableStore(t, root)
			test.setup(t, root, store, deliverables)
			createCompletedEvidenceTask(t, store, deliverables, "TASK-004", "統合", "PLAN-001", "既存統合")
			collector, err := NewDependencyEvidenceCollector(root, "ToDoアプリ")
			if err != nil {
				t.Fatal(err)
			}
			request := dependencyEvidenceRequest([]string{"TASK-001"})
			if test.name == "pending revision" {
				for index := range request.Tasks {
					if request.Tasks[index].ID == "TASK-005" {
						request.Tasks[index].Status = workflow.StatusUnstarted
					}
				}
			}
			_, err = collector.Collect(context.Background(), request)
			var evidenceErr *service.DependencyEvidenceError
			if !errors.As(err, &evidenceErr) || evidenceErr.TaskID != "TASK-001" || evidenceErr.Reason != test.reason {
				t.Fatalf("Collect() error = %#v, want reason %s", err, test.reason)
			}
		})
	}
}

func dependencyEvidenceRequest(direct []string) execution.Request {
	assignees := map[string]string{
		"TASK-001": "RESEARCH-001", "TASK-002": "RESEARCH-002", "TASK-003": "RESEARCH-003",
		"TASK-004": "PLAN-001", "TASK-005": "RESEARCH-002", "TASK-006": "RESEARCH-004",
	}
	titles := map[string]string{
		"TASK-001": "市場調査", "TASK-002": "競合調査", "TASK-003": "顧客調査", "TASK-004": "統合",
		"TASK-005": "競合調査を修正", "TASK-006": "非依存の調査",
	}
	tasks := make([]workflow.Task, 0, len(titles))
	for _, taskID := range []string{"TASK-001", "TASK-002", "TASK-003", "TASK-004", "TASK-005", "TASK-006"} {
		assignee := assignees[taskID]
		tasks = append(tasks, workflow.Task{ID: taskID, Title: titles[taskID], AssigneeID: &assignee, Status: workflow.StatusCompleted})
	}
	return execution.Request{
		ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", TaskID: "TASK-004", Tasks: tasks,
		Dependencies: []workflow.Dependency{{TaskID: "TASK-004", DependsOn: append([]string(nil), direct...)}},
	}
}

func createCompletedEvidenceTask(t *testing.T, store *TaskStore, deliverables *DeliverableStore, taskID, title, assignee, content string) {
	t.Helper()
	createCompletedTaskWithoutDeliverable(t, store, taskID, title, assignee)
	_, err := deliverables.Save(context.Background(), deliverable.Document{
		ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", TaskTitle: title,
		ExecutedAt: time.Date(2026, time.August, 21, 10, 0, 0, 0, time.UTC),
		Execution: worker.ExecutionResult{
			Content: content, EmployeeID: assignee, TaskID: taskID, Runner: "MockRunner", Model: "mock",
			Status: worker.StatusCompleted,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func createCompletedTaskWithoutDeliverable(t *testing.T, store *TaskStore, taskID, title, assignee string) {
	t.Helper()
	created := createUnstartedEvidenceTask(t, store, taskID, title, assignee)
	started, err := created.Start()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), started, created.Version); err != nil {
		t.Fatal(err)
	}
	completed, err := started.Complete()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), completed, started.Version); err != nil {
		t.Fatal(err)
	}
}

func createUnstartedEvidenceTask(t *testing.T, store *TaskStore, taskID, title, assignee string) task.Task {
	t.Helper()
	created, err := task.New(task.CreateInput{ID: taskID, Title: title, AssigneeID: &assignee})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), created); err != nil {
		t.Fatal(err)
	}
	return created
}

func saveRevisionEvidenceReference(t *testing.T, root, sourceTaskID, revisionTaskID string, at time.Time) {
	t.Helper()
	store, err := NewRevisionIntentStore(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	decision := review.Decision{Verdict: review.VerdictRequestChanges, Summary: "修正が必要", Issues: []review.Issue{{
		Category: "requirements", Severity: "medium", Description: "根拠を更新する", SuggestedAction: "最新情報へ直す",
	}}}
	if _, err := store.Save(context.Background(), revision.Intent{
		ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", SourceTaskID: sourceTaskID,
		SourceReview: "Reviews/" + sourceTaskID + ".review.json", SourceProjection: "Reviews/" + sourceTaskID + ".md",
		ReviewDecision: decision, AssigneeID: "RESEARCH-002", RevisionTaskID: revisionTaskID,
		Title: sourceTaskID + "を修正する", CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
}
