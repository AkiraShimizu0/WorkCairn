package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/execution"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/policy"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

func TestRuntimeCompletesTemporaryVaultExecutionWithDeliverableAndAudit(t *testing.T) {
	root := writeRuntimeVault(t)
	var providerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		providerCalls.Add(1)
		if request.URL.Path != "/v1/messages" || request.Header.Get("x-api-key") != "fake-api-key" {
			t.Error("unexpected mock Claude request")
		}
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{
            "model":"claude-sonnet-5",
            "content":[{"type":"text","text":"# 完成した仕様書\n\n本文"}],
            "usage":{"input_tokens":120,"output_tokens":30}
        }`))
	}))
	defer server.Close()

	taskStore, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	deliverables, err := vault.NewDeliverableStore(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	audit, err := vault.NewAuditSubscriber(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	dependencyEvidence, err := vault.NewDependencyEvidenceCollector(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	workspaceRuntime, err := New(Config{
		ModelValue: "Claude Sonnet 5",
		Claude: claude.Config{
			APIKey: "fake-api-key", ProviderModel: "claude-sonnet-5", BaseURL: server.URL,
		},
	}, Dependencies{
		HTTPClient: server.Client(), TaskStore: taskStore, Deliverables: deliverables,
		DependencyEvidence: dependencyEvidence, AuditHandler: audit.Handler(),
	})
	if err != nil {
		t.Fatal(err)
	}
	loader, err := vault.NewLoader(root)
	if err != nil {
		t.Fatal(err)
	}
	currentTime := time.Date(2026, time.August, 6, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60))
	request, err := loader.LoadExecutionRequest(context.Background(), vault.ExecutionInput{
		ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", TaskID: "TASK-001",
		Approval:    &policy.ApprovalEvidence{Granted: true, Source: "test", Reference: "approval-001"},
		CurrentTime: currentTime, ExecutionID: "EXEC-001", CommandID: "CMD-001",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := workspaceRuntime.Start(); err != nil {
		t.Fatal(err)
	}
	defer workspaceRuntime.Stop()

	result, err := workspaceRuntime.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != execution.StatusCompleted || result.FinalTaskStatus != task.StatusCompleted ||
		result.Deliverable == nil || result.Deliverable.RelativePath != "Deliverables/TASK-001.md" ||
		providerCalls.Load() != 1 {
		t.Fatalf("Runtime result = %#v calls=%d", result, providerCalls.Load())
	}
	stored, err := taskStore.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusCompleted || stored.Version != 3 {
		t.Fatalf("stored Task = %#v, %v", stored, err)
	}

	projectDirectory := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	deliverableContent := readRuntimeFile(t, filepath.Join(projectDirectory, "Deliverables", "TASK-001.md"))
	wantDeliverable := readRuntimeFile(t, filepath.Join("..", "..", "..", "fixtures", "vault", "deliverable_task_execution.md"))
	if deliverableContent != wantDeliverable {
		t.Fatalf("Deliverable mismatch\n--- got ---\n%s\n--- want ---\n%s", deliverableContent, wantDeliverable)
	}
	auditContent := readRuntimeFile(t, filepath.Join(projectDirectory, "Audit Log.md"))
	startedIndex := strings.Index(auditContent, "task.started")
	completedIndex := strings.Index(auditContent, "task.completed")
	if startedIndex == -1 || completedIndex == -1 || startedIndex >= completedIndex {
		t.Fatalf("Audit lifecycle order =\n%s", auditContent)
	}
}

func TestRuntimeDoesNotAdoptOrOverwriteExistingDeliverable(t *testing.T) {
	root := writeRuntimeVault(t)
	projectDirectory := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	deliverablesDirectory := filepath.Join(projectDirectory, "Deliverables")
	if err := os.MkdirAll(deliverablesDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	existingPath := filepath.Join(deliverablesDirectory, "TASK-001.md")
	existingEvidence := "immutable evidence from an earlier attempt\n"
	writeRuntimeFile(t, existingPath, existingEvidence)

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{
            "model":"claude-sonnet-5",
            "content":[{"type":"text","text":"different retry output"}],
            "usage":{"input_tokens":10,"output_tokens":5}
        }`))
	}))
	defer server.Close()
	taskStore, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	deliverables, err := vault.NewDeliverableStore(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	audit, err := vault.NewAuditSubscriber(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	dependencyEvidence, err := vault.NewDependencyEvidenceCollector(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	workspaceRuntime, err := New(Config{
		ModelValue: "Claude Sonnet 5",
		Claude: claude.Config{
			APIKey: "fake-api-key", ProviderModel: "claude-sonnet-5", BaseURL: server.URL,
		},
	}, Dependencies{
		HTTPClient: server.Client(), TaskStore: taskStore, Deliverables: deliverables,
		DependencyEvidence: dependencyEvidence, AuditHandler: audit.Handler(),
	})
	if err != nil {
		t.Fatal(err)
	}
	loader, err := vault.NewLoader(root)
	if err != nil {
		t.Fatal(err)
	}
	request, err := loader.LoadExecutionRequest(context.Background(), vault.ExecutionInput{
		ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", TaskID: "TASK-001",
		Approval:    &policy.ApprovalEvidence{Granted: true, Source: "test"},
		CurrentTime: time.Date(2026, time.August, 6, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := workspaceRuntime.Start(); err != nil {
		t.Fatal(err)
	}
	defer workspaceRuntime.Stop()

	result, err := workspaceRuntime.Execute(context.Background(), request)
	var executionError *execution.ExecutionError
	if !errors.As(err, &executionError) || executionError.Stage != execution.StageDeliverable ||
		executionError.Kind != execution.ErrorDeliverableSaveFailed || result.Status != execution.StatusHeld || !result.Held {
		t.Fatalf("Runtime result = %#v, %v", result, err)
	}
	if after := readRuntimeFile(t, existingPath); after != existingEvidence {
		t.Fatalf("existing Deliverable was changed: %q", after)
	}
	stored, err := taskStore.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusOnHold || stored.Version != 4 ||
		stored.LastFailureReason != "deliverable_save_failed" || stored.HoldReason != "hold_after_execution_failure" {
		t.Fatalf("stored Task = %#v, %v", stored, err)
	}
	auditContent := readRuntimeFile(t, filepath.Join(projectDirectory, "Audit Log.md"))
	for _, eventType := range []string{"task.started", "task.failed", "task.held"} {
		if !strings.Contains(auditContent, eventType) {
			t.Fatalf("Audit is missing %s:\n%s", eventType, auditContent)
		}
	}
}

func writeRuntimeVault(t *testing.T) string {
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
	writeRuntimeFile(t, filepath.Join(employeeDirectory, "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	writeRuntimeFile(t, filepath.Join(projectDirectory, "Project.md"), "---\ntype: project\nname: ToDoアプリ\n---\n\n# ToDoアプリ\n\n## 概要\n\nシンプルなToDo Webアプリを開発する\n")
	managedTasks := readRuntimeFile(t, filepath.Join("..", "..", "..", "fixtures", "vault", "tasks_managed_v1.md"))
	writeRuntimeFile(t, filepath.Join(projectDirectory, "Tasks.md"), managedTasks)
	writeRuntimeFile(t, filepath.Join(projectDirectory, "Task Dependencies.md"), "---\ntype: task-dependencies\nproject: ToDoアプリ\n---\n\n| Task ID | Proposed ID | Depends On | Rationale |\n|---|---|---|---|\n| TASK-001 | PROPOSED-001 | なし | 初期Task |\n")
	return root
}

func writeRuntimeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRuntimeFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
