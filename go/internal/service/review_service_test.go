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

	"github.com/AkiraShimizu0/WorkCairn/go/internal/failure"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/prompt"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/runner"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/worker"
)

type reviewServiceFixture struct {
	Input    review.PromptInput `json:"input"`
	Expected worker.Prompt      `json:"expected"`
}

type capturingReviewRunner struct {
	request    worker.RunRequest
	content    string
	err        error
	presence   map[string]bool
	fieldShape map[string]failure.StructuredOutputFieldShape
	stopReason worker.StopReason
	calls      int
}

func (*capturingReviewRunner) Name() string { return "ClaudeRunner" }

func (fake *capturingReviewRunner) Run(_ context.Context, request worker.RunRequest) (worker.RunResult, error) {
	fake.calls++
	fake.request = request
	if fake.err != nil {
		return worker.RunResult{}, fake.err
	}
	inputTokens, outputTokens := 12, 8
	stopReason := fake.stopReason
	if stopReason == "" {
		stopReason = worker.StopReasonCompleted
	}
	return worker.RunResult{
		Content:                    fake.content,
		Runner:                     fake.Name(),
		Model:                      request.Model,
		Usage:                      worker.TokenUsage{InputTokens: &inputTokens, OutputTokens: &outputTokens},
		Duration:                   10 * time.Millisecond,
		Metadata:                   request.Metadata,
		StructuredOutputPresence:   fake.presence,
		StructuredOutputFieldShape: fake.fieldShape,
		StopReason:                 stopReason,
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

// TestReviewServiceClassifiesMaxTokensAsOutputIncompleteBeforeParsing is the
// Review-side counterpart to CEOPlanService.Generate's existing
// StopReasonMaxTokens check (ADR-0058): a Runner call that succeeds but was
// cut off by the Provider's own output ceiling must never reach
// review.ParseTypedDecision -- even when, as here, Content still happens to
// satisfy worker.RunResult.Validate() -- because parsing it would
// misclassify a truncation as an ordinary Review parse failure
// (REVIEW_RESULT_INVALID) instead of the typed OUTPUT_INCOMPLETE this
// checks for. Also confirms exactly one Provider call: this classification
// never retries or falls back.
func TestReviewServiceClassifiesMaxTokensAsOutputIncompleteBeforeParsing(t *testing.T) {
	fixture := loadReviewServiceFixture(t)
	registry := runner.NewRegistry()
	fake := &capturingReviewRunner{
		content:    `{"verdict":"Approve","issues":[],"summary":"truncated`,
		stopReason: worker.StopReasonMaxTokens,
	}
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
	result, executeErr := service.Execute(context.Background(), fixture.Input)
	assertReviewWorkerErrorKind(t, result, executeErr, WorkerErrorOutputIncomplete)
	if !errors.Is(executeErr, ErrProviderOutputIncomplete) {
		t.Fatalf("error = %v, want wrapped ErrProviderOutputIncomplete", executeErr)
	}
	var parseErr *review.ParseError
	if errors.As(executeErr, &parseErr) {
		t.Fatalf("max_tokens output must never reach review.ParseTypedDecision, got *review.ParseError: %v", parseErr)
	}
	if fake.calls != 1 {
		t.Fatalf("Provider calls = %d, want exactly 1 (no retry/fallback)", fake.calls)
	}
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
	presence := map[string]bool{"verdict": true, "issues": true, "summary": true}
	fieldShape := map[string]failure.StructuredOutputFieldShape{
		"summary": {Present: true, JSONType: "string", NonBlank: boolPtr(false)},
	}
	fake := &capturingReviewRunner{content: `{"verdict":"Approve","issues":[],"summary":""}`, presence: presence, fieldShape: fieldShape}
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
		parseErr.Field != "summary" || !reflect.DeepEqual(parseErr.Presence, presence) ||
		!reflect.DeepEqual(parseErr.FieldShape, fieldShape) {
		t.Fatalf("ParseError = %#v, want Presence = %#v FieldShape = %#v", parseErr, presence, fieldShape)
	}
}

func boolPtr(value bool) *bool { return &value }

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
