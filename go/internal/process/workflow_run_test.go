package process

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/project"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

func TestWorkflowRunPlansExecutesReplaysAndStopsAtCommandBoundary(t *testing.T) {
	root := writePlanVault(t)
	at := planInput(root).CurrentTime
	assignee := "PLAN-001"
	created, err := ExecuteTaskCreation(context.Background(), TaskCreationInput{
		VaultRoot: root, ProjectName: "ToDoアプリ", Title: "実装する", AssigneeID: &assignee, CurrentTime: at,
	}, true)
	if err != nil || created.Task.ID != "TASK-002" {
		t.Fatalf("second Task = %#v, %v", created, err)
	}
	dependencyPath := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Task Dependencies.md")
	if err := os.Remove(dependencyPath); err != nil {
		t.Fatal(err)
	}
	_, err = ExecuteProjectDependencies(context.Background(), ProjectDependenciesInput{
		VaultRoot: root, ProjectName: "ToDoアプリ", CurrentTime: at,
		Rows: []project.TaskDependency{
			{TaskID: "TASK-001", ProposalID: "PROPOSED-001", DependsOn: []string{}, Rationale: "first"},
			{TaskID: "TASK-002", ProposalID: "PROPOSED-002", DependsOn: []string{"TASK-001"}, Rationale: "after first"},
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	beforePlan := planVaultSnapshot(t, root)
	plan, err := PlanWorkflow(context.Background(), WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at})
	if err != nil || !plan.Next.Ready || plan.Next.TaskID != "TASK-001" {
		t.Fatalf("Workflow plan = %#v, %v", plan, err)
	}
	if !reflect.DeepEqual(beforePlan, planVaultSnapshot(t, root)) {
		t.Fatal("Workflow plan changed Vault")
	}
	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		providerCalls++
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{"model":"claude-test","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()
	input := ExecuteWorkflowInput{
		WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: at},
		Approved:          true, ApprovalReference: "approval-workflow", CommandID: "CMD-WORKFLOW-001", MaxTasks: 10,
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}
	first, err := ExecuteWorkflow(context.Background(), input, provider, server.Client())
	if err != nil || first.Status != "completed" || len(first.Executions) != 2 || providerCalls != 2 {
		t.Fatalf("Workflow result = %#v, %v, calls=%d", first, err, providerCalls)
	}
	store, _ := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	for _, taskID := range []string{"TASK-001", "TASK-002"} {
		stored, getErr := store.Get(context.Background(), taskID)
		if getErr != nil || stored.Status != task.StatusCompleted {
			t.Fatalf("Task %s = %#v, %v", taskID, stored, getErr)
		}
	}
	beforeReplay := planVaultSnapshot(t, root)
	replayed, err := ExecuteWorkflow(context.Background(), input, provider, server.Client())
	if err != nil || !reflect.DeepEqual(first, replayed) || providerCalls != 2 || !reflect.DeepEqual(beforeReplay, planVaultSnapshot(t, root)) {
		t.Fatalf("Workflow replay = %#v, %v, calls=%d", replayed, err, providerCalls)
	}
	input.MaxTasks = 1
	if _, err := ExecuteWorkflow(context.Background(), input, provider, server.Client()); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("Workflow conflict error = %v", err)
	}
}

func TestWorkflowRunRequiresApprovalAndCommandIDBeforeEffects(t *testing.T) {
	root := writePlanVault(t)
	base := ExecuteWorkflowInput{WorkflowPlanInput: WorkflowPlanInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", CurrentTime: time.Now()}, MaxTasks: 1}
	before := planVaultSnapshot(t, root)
	if _, err := ExecuteWorkflow(context.Background(), base, ClaudeProcessConfig{}, nil); !errors.Is(err, ErrWorkflowApprovalRequired) {
		t.Fatalf("unapproved error = %v", err)
	}
	base.Approved = true
	if _, err := ExecuteWorkflow(context.Background(), base, ClaudeProcessConfig{}, nil); !errors.Is(err, ErrWorkflowCommandIDRequired) {
		t.Fatalf("missing Command ID error = %v", err)
	}
	if !reflect.DeepEqual(before, planVaultSnapshot(t, root)) {
		t.Fatal("rejected Workflow changed Vault")
	}
}
