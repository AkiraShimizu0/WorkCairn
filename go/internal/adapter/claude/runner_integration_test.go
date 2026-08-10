package claude_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	promptbuilder "github.com/AkiraShimizu0/workcairn/go/internal/prompt"
	"github.com/AkiraShimizu0/workcairn/go/internal/runner"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

type promptFixture struct {
	Input    worker.PromptInput `json:"input"`
	Expected worker.Prompt      `json:"expected"`
}

func TestWorkerServiceExecutesGoldenPromptThroughClaudeAdapter(t *testing.T) {
	fixture := loadPromptFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/messages" {
			t.Errorf("request target = %s %s", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
			return
		}
		if request.Header.Get("x-api-key") != "fake-api-key" {
			t.Error("Claude Adapter did not supply configured API key")
		}
		var payload struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			System    string `json:"system"`
			Messages  []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload.Model != "claude-sonnet-5" || payload.MaxTokens != 3000 || payload.System != fixture.Expected.System ||
			len(payload.Messages) != 1 || payload.Messages[0].Role != "user" || payload.Messages[0].Content != fixture.Expected.User {
			t.Errorf("Claude Messages request did not preserve the golden prompt")
		}
		response.Header().Set("content-type", "application/json")
		response.Header().Set("request-id", "request-integration")
		_, _ = response.Write([]byte(`{
			"model":"claude-sonnet-5",
			"content":[{"type":"text","text":"# 成果物\n\n本文"}],
			"usage":{"input_tokens":120,"output_tokens":30}
		}`))
	}))
	defer server.Close()

	claudeRunner, err := claude.New(claude.Config{
		APIKey:        "fake-api-key",
		ProviderModel: "claude-sonnet-5",
		BaseURL:       server.URL,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	registry := runner.NewRegistry()
	if err := registry.Register(claudeRunner); err != nil {
		t.Fatal(err)
	}
	if err := registry.MapModel(fixture.Input.Employee.Model, claudeRunner.Name()); err != nil {
		t.Fatal(err)
	}
	workerService, err := service.NewWorkerService(promptbuilder.NewBuilder(), registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := workerService.Activate(); err != nil {
		t.Fatal(err)
	}

	result, err := workerService.Execute(context.Background(), worker.ExecutionRequest{
		Employee:    fixture.Input.Employee,
		Task:        fixture.Input.Task,
		CurrentTime: fixture.Input.CurrentTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "# 成果物\n\n本文" || result.Runner != claude.Name || result.Model != fixture.Input.Employee.Model {
		t.Fatalf("Worker result = %#v", result)
	}
	if result.Usage.InputTokens == nil || *result.Usage.InputTokens != 120 ||
		result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 30 {
		t.Fatalf("Worker usage = %#v", result.Usage)
	}
}

func TestWorkerServiceClassifiesClaudeProviderFailure(t *testing.T) {
	fixture := loadPromptFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("content-type", "application/json")
		response.Header().Set("request-id", "request-rate-limited")
		response.WriteHeader(http.StatusTooManyRequests)
		_, _ = response.Write([]byte(`{
			"type":"error",
			"error":{"type":"rate_limit_error","message":"sensitive provider detail"}
		}`))
	}))
	defer server.Close()

	claudeRunner, err := claude.New(claude.Config{
		APIKey:        "fake-api-key",
		ProviderModel: "claude-sonnet-5",
		BaseURL:       server.URL,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	registry := runner.NewRegistry()
	if err := registry.Register(claudeRunner); err != nil {
		t.Fatal(err)
	}
	if err := registry.MapModel(fixture.Input.Employee.Model, claudeRunner.Name()); err != nil {
		t.Fatal(err)
	}
	workerService, err := service.NewWorkerService(promptbuilder.NewBuilder(), registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := workerService.Activate(); err != nil {
		t.Fatal(err)
	}

	_, err = workerService.Execute(context.Background(), worker.ExecutionRequest{
		Employee:    fixture.Input.Employee,
		Task:        fixture.Input.Task,
		CurrentTime: fixture.Input.CurrentTime,
	})
	var workerError *service.WorkerExecutionError
	if !errors.As(err, &workerError) || workerError.Kind != service.WorkerErrorRunnerFailed {
		t.Fatalf("Worker error = %#v", workerError)
	}
	if !errors.Is(err, claude.ErrProvider) {
		t.Fatalf("Worker error cause = %v", err)
	}
	if err.Error() != "worker execution failed: RUNNER_FAILED" {
		t.Fatalf("public Worker error = %q", err.Error())
	}
}

func loadPromptFixture(t *testing.T) promptFixture {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "fixtures", "prompt", "task_execution.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture promptFixture
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}
