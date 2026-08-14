package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/prompt"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/runner"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

type reviewServiceFixture struct {
	Input    review.PromptInput `json:"input"`
	Expected worker.Prompt      `json:"expected"`
}

type capturingReviewRunner struct {
	request  worker.RunRequest
	content  string
	err      error
	presence map[string]bool
}

func (*capturingReviewRunner) Name() string { return "ClaudeRunner" }

func (fake *capturingReviewRunner) Run(_ context.Context, request worker.RunRequest) (worker.RunResult, error) {
	fake.request = request
	if fake.err != nil {
		return worker.RunResult{}, fake.err
	}
	inputTokens, outputTokens := 12, 8
	return worker.RunResult{
		Content:                  fake.content,
		Runner:                   fake.Name(),
		Model:                    request.Model,
		Usage:                    worker.TokenUsage{InputTokens: &inputTokens, OutputTokens: &outputTokens},
		Duration:                 10 * time.Millisecond,
		Metadata:                 request.Metadata,
		StructuredOutputPresence: fake.presence,
	}, nil
}

type failingReviewBuilder struct{ err error }

func (builder failingReviewBuilder) BuildReview(context.Context, review.PromptInput) (worker.Prompt, error) {
	return worker.Prompt{}, builder.err
}

func TestReviewServiceUsesConcreteBuilderAndRunnerRegistry(t *testing.T) {
	fixture := loadReviewServiceFixture(t)
	fake := &capturingReviewRunner{content: `{"verdict":"Approve","issues":[],"summary":"問題ありません。"}`}
	registry := runner.NewRegistry()
	if err := registry.Register(fake); err != nil {
		t.Fatal(err)
	}
	if err := registry.MapModel(fixture.Input.Reviewer.Model, fake.Name()); err != nil {
		t.Fatal(err)
	}
	service, err := NewReviewService(prompt.NewBuilder(), registry)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Input.Metadata = map[string]string{"correlation_id": "review-001"}

	result, err := service.Execute(context.Background(), fixture.Input)
	if err != nil {
		t.Fatal(err)
	}
	if fake.request.SystemPrompt != fixture.Expected.System || fake.request.UserPrompt != fixture.Expected.User {
		t.Fatalf("Runner prompt did not match golden fixture")
	}
	if fake.request.StructuredOutput == nil ||
		fake.request.StructuredOutput.ContentField != "" ||
		!reflect.DeepEqual(fake.request.StructuredOutput.Schema, review.TypedDecisionJSONSchema()) {
		t.Fatalf("Runner request did not carry the Review Structured Output contract: %#v", fake.request.StructuredOutput)
	}
	if result.Decision.Verdict != review.VerdictApprove || result.Decision.Summary != "問題ありません。" {
		t.Fatalf("result = %#v", result)
	}
	if result.ReviewerID != "QA-001" || result.TaskID != "TASK-001" {
		t.Fatalf("result identity = %#v", result)
	}
	if !reflect.DeepEqual(result.Metadata, fixture.Input.Metadata) {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
}

func TestReviewServiceMapsBuilderAndStructuredResultFailures(t *testing.T) {
	fixture := loadReviewServiceFixture(t)
	registry := runner.NewRegistry()
	fake := &capturingReviewRunner{content: "invalid review"}
	if err := registry.Register(fake); err != nil {
		t.Fatal(err)
	}
	if err := registry.MapModel(fixture.Input.Reviewer.Model, fake.Name()); err != nil {
		t.Fatal(err)
	}

	buildCause := errors.New("build failed")
	buildService, err := NewReviewService(failingReviewBuilder{err: buildCause}, registry)
	if err != nil {
		t.Fatal(err)
	}
	result, executeErr := buildService.Execute(context.Background(), fixture.Input)
	assertReviewWorkerErrorKind(t, result, executeErr, WorkerErrorPromptBuildFailed)

	parseService, err := NewReviewService(prompt.NewBuilder(), registry)
	if err != nil {
		t.Fatal(err)
	}
	result, executeErr = parseService.Execute(context.Background(), fixture.Input)
	assertReviewWorkerErrorKind(t, result, executeErr, WorkerErrorInvalidReviewResult)
}

// TestReviewServiceAttachesRunnerPresenceOnlyToParseFailures proves the one
// wiring point this package owns: ReviewService.Execute is where the
// Runner's already-captured worker.RunResult.StructuredOutputPresence
// diagnostic and the *review.ParseError it caused meet, since
// review.ParseTypedDecision itself never observes Provider response
// shape. Presence must be attached exactly when ParseTypedDecision fails,
// and must be exactly what the Runner returned — never recomputed here.
func TestReviewServiceAttachesRunnerPresenceOnlyToParseFailures(t *testing.T) {
	fixture := loadReviewServiceFixture(t)
	registry := runner.NewRegistry()
	presence := map[string]bool{"verdict": true, "issues": true, "summary": false}
	fake := &capturingReviewRunner{content: `{"verdict":"Approve","issues":[]}`, presence: presence}
	if err := registry.Register(fake); err != nil {
		t.Fatal(err)
	}
	if err := registry.MapModel(fixture.Input.Reviewer.Model, fake.Name()); err != nil {
		t.Fatal(err)
	}
	service, err := NewReviewService(prompt.NewBuilder(), registry)
	if err != nil {
		t.Fatal(err)
	}
	_, executeErr := service.Execute(context.Background(), fixture.Input)
	var executionError *WorkerExecutionError
	if !errors.As(executeErr, &executionError) || executionError.Kind != WorkerErrorInvalidReviewResult {
		t.Fatalf("error = %v, want WorkerExecutionError %s", executeErr, WorkerErrorInvalidReviewResult)
	}
	var parseErr *review.ParseError
	if !errors.As(executeErr, &parseErr) || parseErr.Reason != review.ParseFailureMissingRequiredField ||
		parseErr.Field != "summary" || !reflect.DeepEqual(parseErr.Presence, presence) {
		t.Fatalf("ParseError = %#v, want Presence = %#v", parseErr, presence)
	}
}

// TestReviewServiceLeavesPresenceNilWhenRunnerDoesNotSupplyIt proves the
// success-path Decision itself never carries a presence diagnostic (only
// the failure path does) and that a Runner returning no presence (e.g. a
// pre-migration fake, or a non-Structured-Output Runner) leaves the parse
// error's Presence nil rather than a guessed value.
func TestReviewServiceLeavesPresenceNilWhenRunnerDoesNotSupplyIt(t *testing.T) {
	fixture := loadReviewServiceFixture(t)
	registry := runner.NewRegistry()
	fake := &capturingReviewRunner{content: `{"verdict":"Approve","issues":[]}`}
	if err := registry.Register(fake); err != nil {
		t.Fatal(err)
	}
	if err := registry.MapModel(fixture.Input.Reviewer.Model, fake.Name()); err != nil {
		t.Fatal(err)
	}
	service, err := NewReviewService(prompt.NewBuilder(), registry)
	if err != nil {
		t.Fatal(err)
	}
	_, executeErr := service.Execute(context.Background(), fixture.Input)
	var parseErr *review.ParseError
	if !errors.As(executeErr, &parseErr) || parseErr.Presence != nil {
		t.Fatalf("ParseError.Presence = %#v, want nil", parseErr.Presence)
	}
}

func assertReviewWorkerErrorKind(t *testing.T, _ review.ExecutionResult, err error, want WorkerErrorKind) {
	t.Helper()
	var executionError *WorkerExecutionError
	if !errors.As(err, &executionError) || executionError.Kind != want {
		t.Fatalf("error = %v, want WorkerExecutionError %s", err, want)
	}
}

func loadReviewServiceFixture(t *testing.T) reviewServiceFixture {
	t.Helper()
	path := filepath.Join("..", "..", "..", "fixtures", "prompt", "review_execution.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture reviewServiceFixture
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}
