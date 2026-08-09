package process

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
	"github.com/AkiraShimizu0/workspace-os/go/internal/execution"
	"github.com/AkiraShimizu0/workspace-os/go/internal/recovery"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

func TestExecuteTaskRequiresApprovalBeforeCompositionOrEffects(t *testing.T) {
	root := writePlanVault(t)
	before := planVaultSnapshot(t, root)
	result, err := ExecuteTask(context.Background(), ExecuteTaskInput{
		ExecutionPlanInput: planInput(root),
	}, ClaudeProcessConfig{}, nil)
	if !errors.Is(err, ErrExecutionApprovalRequired) || !reflect.DeepEqual(result, execution.Result{}) {
		t.Fatalf("ExecuteTask() = %#v, %v", result, err)
	}
	if after := planVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("unapproved execution changed temporary Vault")
	}
}

func TestSequentialExecutionCommandDigestRemainsCheckpointCompatible(t *testing.T) {
	root := writePlanVault(t)
	input := ExecuteTaskInput{
		ExecutionPlanInput: planInput(root), Approved: true, ApprovalSource: "process-test",
		ApprovalReference: "approval-001", ExecutionID: "EXEC-001", CommandID: "CMD-DIGEST-COMPAT-001",
	}
	provider := ClaudeProcessConfig{ProviderModel: "claude-test", MaxTokens: 128}
	claim, err := claimExecutionCommand(context.Background(), input, provider, input.ApprovalSource)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := commandledger.RequestDigest(struct {
		ProjectID         string    `json:"project_id"`
		ProjectName       string    `json:"project_name"`
		TaskID            string    `json:"task_id"`
		CurrentTime       time.Time `json:"current_time"`
		ApprovalSource    string    `json:"approval_source"`
		ApprovalReference string    `json:"approval_reference,omitempty"`
		ExecutionID       string    `json:"execution_id,omitempty"`
		ProviderModel     string    `json:"provider_model,omitempty"`
		MaxTokens         int       `json:"max_tokens,omitempty"`
	}{
		ProjectID: input.ProjectID, ProjectName: input.ProjectName, TaskID: input.TaskID,
		CurrentTime: input.CurrentTime, ApprovalSource: input.ApprovalSource,
		ApprovalReference: input.ApprovalReference, ExecutionID: input.ExecutionID,
		ProviderModel: provider.ProviderModel, MaxTokens: provider.MaxTokens,
	})
	if err != nil || claim.running.RequestDigest != expected {
		t.Fatalf("sequential digest = %s, want checkpoint digest %s, err=%v", claim.running.RequestDigest, expected, err)
	}
}

func TestRunningCommandClaimBlocksAutomaticResumeAndIsRecoveryVisible(t *testing.T) {
	root := writePlanVault(t)
	input := ExecuteTaskInput{
		ExecutionPlanInput: planInput(root), Approved: true, ApprovalSource: "process-test",
		ApprovalReference: "approval-001", ExecutionID: "EXEC-001", CommandID: "CMD-RUNNING-001",
	}
	provider := ClaudeProcessConfig{ProviderModel: "claude-sonnet-5"}
	claim, err := claimExecutionCommand(context.Background(), input, provider, input.ApprovalSource)
	if err != nil || claim.running.State != commandledger.StateRunning {
		t.Fatalf("claimExecutionCommand() = %#v, %v", claim, err)
	}
	providerCalled := false
	client := httpDoerFunc(func(*http.Request) (*http.Response, error) {
		providerCalled = true
		return nil, errors.New("must not be called")
	})
	if _, err := ExecuteTask(context.Background(), input, provider, client); !errors.Is(err, commandledger.ErrInProgress) || providerCalled {
		t.Fatalf("running replay error = %v, providerCalled=%t", err, providerCalled)
	}
	report, err := InspectRecovery(context.Background(), RecoveryInput{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil || !hasRecoveryFinding(report, recovery.FindingCommandIncomplete, "TASK-001") {
		t.Fatalf("Recovery report = %#v, %v", report, err)
	}
	store, _ := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	stored, err := store.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusUnstarted || stored.Version != 1 {
		t.Fatalf("running replay changed Task: %#v, %v", stored, err)
	}
}

func TestExecuteTaskComposesTemporaryVaultAndMockProvider(t *testing.T) {
	root := writePlanVault(t)
	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		providerCalls++
		if request.URL.Path != "/v1/messages" || request.Header.Get("x-api-key") != "fake-api-key" {
			t.Error("unexpected mock Provider request")
		}
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{
            "model":"claude-sonnet-5",
            "content":[{"type":"text","text":"# 完成した仕様書\n\n本文"}],
            "usage":{"input_tokens":120,"output_tokens":30}
        }`))
	}))
	defer server.Close()

	input := ExecuteTaskInput{
		ExecutionPlanInput: planInput(root), Approved: true,
		ApprovalSource: "process-test", ApprovalReference: "approval-001",
		ExecutionID: "EXEC-001", CommandID: "CMD-001",
	}
	provider := ClaudeProcessConfig{
		APIKey: "fake-api-key", ProviderModel: "claude-sonnet-5", BaseURL: server.URL,
	}
	result, err := ExecuteTask(context.Background(), input, provider, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != execution.StatusCompleted || result.FinalTaskStatus != task.StatusCompleted ||
		result.Deliverable == nil || result.Deliverable.RelativePath != "Deliverables/TASK-001.md" {
		t.Fatalf("ExecuteTask() = %#v", result)
	}
	projectDirectory := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	for _, path := range []string{
		filepath.Join(projectDirectory, "Deliverables", "TASK-001.md"),
		filepath.Join(projectDirectory, "Audit Log.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected process output %s: %v", path, err)
		}
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusCompleted || stored.Version != 3 {
		t.Fatalf("stored Task = %#v, %v", stored, err)
	}
	replayed, err := ExecuteTask(context.Background(), input, provider, server.Client())
	if err != nil || !reflect.DeepEqual(replayed, result) || providerCalls != 1 {
		t.Fatalf("idempotent replay = %#v, %v, providerCalls=%d", replayed, err, providerCalls)
	}
	conflict := input
	conflict.ApprovalReference = "different-approved-request"
	if _, err := ExecuteTask(context.Background(), conflict, provider, server.Client()); !errors.Is(err, commandledger.ErrRequestConflict) || providerCalls != 1 {
		t.Fatalf("conflicting Command ID error = %v, providerCalls=%d", err, providerCalls)
	}
}

func TestExecuteTaskPreflightRejectsExistingDeliverableBeforeProvider(t *testing.T) {
	root := writePlanVault(t)
	deliverablePath := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables", "TASK-001.md")
	if err := os.MkdirAll(filepath.Dir(deliverablePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deliverablePath, []byte("immutable evidence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	providerCalled := false
	client := httpDoerFunc(func(*http.Request) (*http.Response, error) {
		providerCalled = true
		return nil, errors.New("must not be called")
	})
	result, err := ExecuteTask(context.Background(), ExecuteTaskInput{
		ExecutionPlanInput: planInput(root), Approved: true, ApprovalSource: "process-test",
	}, ClaudeProcessConfig{APIKey: "fake", ProviderModel: "fake-model"}, client)
	var preflightError *ExecutionPreflightError
	if !errors.As(err, &preflightError) || !errors.Is(err, ErrExecutionPreflightFailed) ||
		!preflightError.Plan.DeliverableExists || providerCalled || !reflect.DeepEqual(result, execution.Result{}) {
		t.Fatalf("ExecuteTask() = %#v, %v, providerCalled=%t", result, err, providerCalled)
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusUnstarted || stored.Version != 1 {
		t.Fatalf("Task changed after preflight rejection: %#v, %v", stored, err)
	}
}

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (function httpDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestPlanExecutionIsReadOnlyAndReportsExecutableTask(t *testing.T) {
	root := writePlanVault(t)
	before := planVaultSnapshot(t, root)
	plan, err := PlanExecution(context.Background(), planInput(root))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Executable || !plan.ApprovalRequired || plan.DeliverableExists ||
		plan.ProjectID != "PROJECT-001" || plan.TaskID != "TASK-001" ||
		plan.TaskTitle != "要件を整理する" || plan.TaskVersion != 1 ||
		plan.AssigneeID != "PLAN-001" || plan.EmployeeName != "田中 美咲" ||
		plan.Model != "Claude Sonnet 5" || plan.DeliverablePath != "Deliverables/TASK-001.md" ||
		!plan.Readiness.Ready || len(plan.BlockingReasons) != 0 {
		t.Fatalf("PlanExecution() = %#v", plan)
	}
	after := planVaultSnapshot(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("read-only plan changed temporary Vault\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestPlanExecutionReportsExistingDeliverableWithoutAdoptingIt(t *testing.T) {
	root := writePlanVault(t)
	path := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables", "TASK-001.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("immutable evidence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := planVaultSnapshot(t, root)
	plan, err := PlanExecution(context.Background(), planInput(root))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Executable || !plan.DeliverableExists ||
		!reflect.DeepEqual(plan.BlockingReasons, []string{"deliverable_already_exists"}) {
		t.Fatalf("PlanExecution() = %#v", plan)
	}
	if after := planVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("plan changed existing Deliverable or Vault state")
	}
}

func TestPlanExecutionRejectsUnmanagedOrCorruptTaskMetadata(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(string) string
	}{
		{"missing", func(content string) string { return strings.Split(content, "<!-- workspace-os-task-metadata:v1")[0] }},
		{"corrupt", func(content string) string { return strings.Replace(content, `"version": 1`, `"version": 0`, 1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := writePlanVault(t)
			path := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Tasks.md")
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.mutate(string(content))), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := PlanExecution(context.Background(), planInput(root)); err == nil || (!errors.Is(err, vault.ErrMetadataMissing) && !errors.Is(err, vault.ErrMetadataInvalid)) {
				t.Fatalf("PlanExecution() error = %v", err)
			}
		})
	}
}

func planInput(root string) ExecutionPlanInput {
	return ExecutionPlanInput{
		VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", TaskID: "TASK-001",
		CurrentTime: time.Date(2026, time.August, 6, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60)),
	}
}

func writePlanVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	employeeDirectory := filepath.Join(root, "社員")
	projectDirectory := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	if err := os.MkdirAll(employeeDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	writePlanFile(t, filepath.Join(employeeDirectory, "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	writePlanFile(t, filepath.Join(projectDirectory, "Project.md"), "---\ntype: project\nname: ToDoアプリ\n---\n\n# ToDoアプリ\n\n## 概要\n\nシンプルなToDo Webアプリを開発する\n")
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "vault", "tasks_managed_v1.md"))
	if err != nil {
		t.Fatal(err)
	}
	writePlanFile(t, filepath.Join(projectDirectory, "Tasks.md"), string(fixture))
	writePlanFile(t, filepath.Join(projectDirectory, "Task Dependencies.md"), "---\ntype: task-dependencies\nproject: ToDoアプリ\n---\n\n| Task ID | Proposed ID | Depends On | Rationale |\n|---|---|---|---|\n| TASK-001 | PROPOSED-001 | なし | 初期Task |\n")
	return root
}

func writePlanFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func planVaultSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot[relative+"/"] = "directory"
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[relative] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
