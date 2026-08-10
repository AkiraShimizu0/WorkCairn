package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/deliverablestore"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/execution"
	"github.com/AkiraShimizu0/workcairn/go/internal/kernel"
	"github.com/AkiraShimizu0/workcairn/go/internal/metrics"
	"github.com/AkiraShimizu0/workcairn/go/internal/policy"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
	"github.com/AkiraShimizu0/workcairn/go/internal/taskstore"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
	"github.com/AkiraShimizu0/workcairn/go/internal/workflow"
)

func TestRuntimeExecutesApprovedTaskThroughComposedClaudeAdapter(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.URL.Path != "/v1/messages" || request.Header.Get("x-api-key") != "fake-api-key" {
			t.Error("unexpected Claude request target or headers")
		}
		var payload struct {
			Model    string `json:"model"`
			System   string `json:"system"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, expected := range []string{
			"プロジェクト概要: シンプルなToDo Webアプリを開発する",
			"担当社員ID: PLAN-001",
		} {
			if !strings.Contains(payload.System, expected) {
				t.Errorf("system prompt is missing %q", expected)
			}
		}
		if payload.Model != "claude-sonnet-5" || len(payload.Messages) != 1 ||
			!strings.Contains(payload.Messages[0].Content, "タスクID: TASK-001") {
			t.Error("Runtime did not translate logical model and prompt correctly")
		}
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{
			"model":"claude-sonnet-5",
			"content":[{"type":"text","text":"# 成果物\n\n本文"}],
			"usage":{"input_tokens":120,"output_tokens":30}
		}`))
	}))
	defer server.Close()

	store := seededTaskStore(t)
	workspaceRuntime := configuredRuntime(t, store, server)
	if err := workspaceRuntime.Start(); err != nil {
		t.Fatal(err)
	}
	defer workspaceRuntime.Stop()
	if workspaceRuntime.Status().State != kernel.StateStarted {
		t.Fatalf("Runtime status = %#v", workspaceRuntime.Status())
	}

	result, err := workspaceRuntime.Execute(context.Background(), executionRequest(true))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != execution.StatusCompleted || result.FinalTaskStatus != task.StatusCompleted ||
		result.WorkerResult == nil || result.WorkerResult.Content != "# 成果物\n\n本文" ||
		result.Runner != claude.Name || result.Model != "Claude Sonnet 5" {
		t.Fatalf("Runtime Execute() = %#v", result)
	}
	if calls.Load() != 1 {
		t.Fatalf("Claude calls = %d", calls.Load())
	}
	stored, err := store.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusCompleted || stored.Version != 3 {
		t.Fatalf("stored Task = %#v, %v", stored, err)
	}
}

func TestRuntimeRejectsMissingApprovalBeforeProviderOrTaskEffects(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	store := seededTaskStore(t)
	workspaceRuntime := configuredRuntime(t, store, server)
	if err := workspaceRuntime.Start(); err != nil {
		t.Fatal(err)
	}
	defer workspaceRuntime.Stop()

	request := executionRequest(false)
	request.Approval = nil
	result, err := workspaceRuntime.Execute(context.Background(), request)
	var executionError *execution.ExecutionError
	if !errors.As(err, &executionError) || executionError.Stage != execution.StageApproval ||
		executionError.Kind != execution.ErrorApprovalRejected || result.Status != execution.StatusRejected {
		t.Fatalf("Runtime rejection = %#v, %v", result, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("Claude calls before approval = %d", calls.Load())
	}
	stored, err := store.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusUnstarted || stored.Version != 1 {
		t.Fatalf("Task changed before approval: %#v, %v", stored, err)
	}
}

func TestRuntimeObserverFailurePreservesCompletedTaskAndReturnsPartialFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write([]byte(`{"model":"claude-sonnet-5","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()
	store := seededTaskStore(t)
	observerFailure := errors.New("notification commit failed")
	metricSubscriber := metrics.NewSubscriber()
	workspaceRuntime, err := New(Config{
		ModelValue: "Claude Sonnet 5",
		Claude:     claude.Config{APIKey: "fake-api-key", ProviderModel: "claude-sonnet-5", BaseURL: server.URL},
	}, Dependencies{
		HTTPClient: server.Client(), TaskStore: store, Deliverables: deliverablestore.NewInMemory(), AuditHandler: discardAudit,
		Observers: []event.Observer{
			{Types: []event.Type{event.TaskCompleted}, Handler: func(context.Context, event.Event) error { return observerFailure }},
			{Types: []event.Type{event.TaskCompleted}, Handler: metricSubscriber.Handler()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := workspaceRuntime.Start(); err != nil {
		t.Fatal(err)
	}
	defer workspaceRuntime.Stop()
	result, executeErr := workspaceRuntime.Execute(context.Background(), executionRequest(true))
	if !errors.Is(executeErr, observerFailure) || result.Status != execution.StatusPartialFailure || result.FinalTaskStatus != task.StatusCompleted || result.Deliverable == nil {
		t.Fatalf("Execute() = %#v, %v", result, executeErr)
	}
	stored, err := store.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusCompleted || stored.Version != 3 {
		t.Fatalf("committed Task = %#v, %v", stored, err)
	}
	if snapshot := metricSubscriber.Snapshot(); snapshot.Total != 1 || snapshot.ByEventType[event.TaskCompleted] != 1 {
		t.Fatalf("later observer was suppressed: %#v", snapshot)
	}
}

func TestRuntimeRequiresExplicitConfigAndDependencies(t *testing.T) {
	store := taskstore.NewInMemory()
	client := http.DefaultClient
	valid := Config{
		ModelValue: "Claude Sonnet 5",
		Claude: claude.Config{
			APIKey:        "fake-api-key",
			ProviderModel: "claude-sonnet-5",
		},
	}
	validDependencies := Dependencies{
		HTTPClient: client, TaskStore: store, Deliverables: deliverablestore.NewInMemory(), AuditHandler: discardAudit,
	}
	if _, err := New(Config{}, validDependencies); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing model error = %v", err)
	}
	if _, err := New(valid, Dependencies{HTTPClient: client}); !errors.Is(err, ErrInvalidDependencies) {
		t.Fatalf("missing TaskStore error = %v", err)
	}
	missingDeliverables := validDependencies
	missingDeliverables.Deliverables = nil
	if _, err := New(valid, missingDeliverables); !errors.Is(err, ErrInvalidDependencies) {
		t.Fatalf("missing Deliverable Store error = %v", err)
	}
	missingAudit := validDependencies
	missingAudit.AuditHandler = nil
	if _, err := New(valid, missingAudit); !errors.Is(err, ErrInvalidDependencies) {
		t.Fatalf("missing Audit handler error = %v", err)
	}
	missingHTTP := validDependencies
	missingHTTP.HTTPClient = nil
	if _, err := New(valid, missingHTTP); !errors.Is(err, ErrInvalidConfig) || !errors.Is(err, claude.ErrInvalidConfig) {
		t.Fatalf("missing HTTP client error = %v", err)
	}
	invalidObserver := validDependencies
	invalidObserver.Observers = []event.Observer{{Types: []event.Type{event.TaskCompleted}}}
	if _, err := New(valid, invalidObserver); !errors.Is(err, ErrInvalidDependencies) {
		t.Fatalf("invalid observer error = %v", err)
	}
}

func TestRuntimeRequiresStartAndStopsKernelServices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	workspaceRuntime := configuredRuntime(t, seededTaskStore(t), server)
	if _, err := workspaceRuntime.Execute(context.Background(), executionRequest(true)); !errors.Is(err, service.ErrExecutionServiceNotActive) {
		t.Fatalf("Execute() before Start error = %v", err)
	}
	if err := workspaceRuntime.Start(); err != nil {
		t.Fatal(err)
	}
	if err := workspaceRuntime.Stop(); err != nil {
		t.Fatal(err)
	}
	if workspaceRuntime.Status().State != kernel.StateStopped {
		t.Fatalf("Runtime status = %#v", workspaceRuntime.Status())
	}
}

func configuredRuntime(t *testing.T, store task.Store, server *httptest.Server) *Runtime {
	t.Helper()
	workspaceRuntime, err := New(Config{
		ModelValue: "Claude Sonnet 5",
		Claude: claude.Config{
			APIKey:        "fake-api-key",
			ProviderModel: "claude-sonnet-5",
			BaseURL:       server.URL,
		},
	}, Dependencies{
		HTTPClient: server.Client(), TaskStore: store,
		Deliverables: deliverablestore.NewInMemory(), AuditHandler: discardAudit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return workspaceRuntime
}

func discardAudit(context.Context, event.Event) error { return nil }

func seededTaskStore(t *testing.T) *taskstore.InMemory {
	t.Helper()
	store := taskstore.NewInMemory()
	assigneeID := "PLAN-001"
	stored, err := task.New(task.CreateInput{ID: "TASK-001", Title: "要件を整理する", AssigneeID: &assigneeID})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	return store
}

func executionRequest(approved bool) execution.Request {
	assigneeID := "PLAN-001"
	return execution.Request{
		ProjectID:       "PROJECT-001",
		ProjectName:     "ToDoアプリ",
		ProjectOverview: "シンプルなToDo Webアプリを開発する",
		TaskID:          "TASK-001",
		Employee: worker.EmployeeContext{
			EmployeeID: assigneeID,
			Name:       "田中 美咲",
			Department: "企画部",
			Role:       "Product Manager",
			Model:      "Claude Sonnet 5",
		},
		Tasks: []workflow.Task{{
			ID: "TASK-001", Title: "要件を整理する",
			AssigneeID: &assigneeID, Status: workflow.StatusUnstarted,
		}},
		ExistingEmployees: map[string]bool{assigneeID: true},
		Approval:          &policy.ApprovalEvidence{Granted: approved, Source: "test", Reference: "approval-001"},
		CurrentTime:       time.Date(2026, time.August, 6, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60)),
	}
}
