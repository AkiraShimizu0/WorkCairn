package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
)

type orchestrationReviewExecutor struct {
	order  *[]string
	result review.ExecutionResult
	err    error
}

func (fake orchestrationReviewExecutor) Execute(context.Context, review.PromptInput) (review.ExecutionResult, error) {
	*fake.order = append(*fake.order, "execute")
	return fake.result, fake.err
}

type orchestrationReviewStore struct {
	order  *[]string
	record review.Record
	err    error
}

func (fake orchestrationReviewStore) Save(context.Context, review.Document) (review.Record, error) {
	*fake.order = append(*fake.order, "save")
	return fake.record, fake.err
}

type orchestrationEventPublisher struct {
	order     *[]string
	published []event.Event
	err       error
}

func (fake *orchestrationEventPublisher) Publish(_ context.Context, published event.Event) error {
	*fake.order = append(*fake.order, "publish")
	fake.published = append(fake.published, published)
	return fake.err
}

func TestReviewOrchestrationPublishesOnlyAfterCanonicalCommit(t *testing.T) {
	order := []string{}
	publisher := &orchestrationEventPublisher{order: &order}
	service := newTestReviewOrchestration(t, &order, review.Record{
		TaskID: "TASK-001", CanonicalPath: "Reviews/TASK-001.review.json", ProjectionPath: "Reviews/TASK-001.review.md",
		CanonicalCommitted: true, ProjectionCommitted: true,
	}, nil, publisher)

	result, err := service.Execute(context.Background(), testReviewOrchestrationRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "reviewed" || !result.EventPublished || result.EventID == "" ||
		!reflect.DeepEqual(order, []string{"execute", "save", "publish"}) {
		t.Fatalf("result=%#v order=%#v", result, order)
	}
	if len(publisher.published) != 1 || publisher.published[0].Type != event.ReviewCompleted ||
		publisher.published[0].AggregateType != "review" || publisher.published[0].AggregateID != "TASK-001" {
		t.Fatalf("published = %#v", publisher.published)
	}
}

func TestReviewOrchestrationDoesNotPublishWithoutCanonicalEvidence(t *testing.T) {
	order := []string{}
	publisher := &orchestrationEventPublisher{order: &order}
	saveErr := &review.SaveError{Stage: "canonical", Err: errors.New("write failed")}
	service := newTestReviewOrchestration(t, &order, review.Record{
		TaskID: "TASK-001", CanonicalPath: "Reviews/TASK-001.review.json", ProjectionPath: "Reviews/TASK-001.review.md",
	}, saveErr, publisher)

	result, err := service.Execute(context.Background(), testReviewOrchestrationRequest())
	if !errors.Is(err, review.ErrSaveFailed) || result.Status != "failed" || result.EventPublished ||
		!reflect.DeepEqual(order, []string{"execute", "save"}) || len(publisher.published) != 0 {
		t.Fatalf("result=%#v err=%v order=%#v events=%d", result, err, order, len(publisher.published))
	}
}

func TestReviewOrchestrationPublishesFactAfterProjectionFailure(t *testing.T) {
	order := []string{}
	record := review.Record{
		TaskID: "TASK-001", CanonicalPath: "Reviews/TASK-001.review.json", ProjectionPath: "Reviews/TASK-001.review.md",
		CanonicalCommitted: true,
	}
	saveErr := &review.SaveError{Record: record, Stage: "projection", Err: errors.New("projection failed")}
	publisher := &orchestrationEventPublisher{order: &order}
	service := newTestReviewOrchestration(t, &order, record, saveErr, publisher)

	result, err := service.Execute(context.Background(), testReviewOrchestrationRequest())
	var orchestrationError *ReviewOrchestrationError
	if !errors.As(err, &orchestrationError) || !errors.Is(err, review.ErrSaveFailed) ||
		result.Status != "partial_failure" || !result.EventPublished || result.Artifact.ProjectionCommitted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if !reflect.DeepEqual(order, []string{"execute", "save", "publish"}) {
		t.Fatalf("order = %#v", order)
	}
}

func TestReviewOrchestrationKeepsArtifactOnPublicationFailure(t *testing.T) {
	order := []string{}
	record := review.Record{
		TaskID: "TASK-001", CanonicalPath: "Reviews/TASK-001.review.json", ProjectionPath: "Reviews/TASK-001.review.md",
		CanonicalCommitted: true, ProjectionCommitted: true,
	}
	publishErr := errors.New("audit unavailable")
	publisher := &orchestrationEventPublisher{order: &order, err: publishErr}
	service := newTestReviewOrchestration(t, &order, record, nil, publisher)

	result, err := service.Execute(context.Background(), testReviewOrchestrationRequest())
	var orchestrationError *ReviewOrchestrationError
	if !errors.As(err, &orchestrationError) || !errors.Is(err, publishErr) || result.Status != "partial_failure" ||
		result.EventPublished || result.EventID == "" || !result.Artifact.CanonicalCommitted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func newTestReviewOrchestration(t *testing.T, order *[]string, record review.Record, saveErr error, publisher *orchestrationEventPublisher) *ReviewOrchestrationService {
	t.Helper()
	service, err := NewReviewOrchestrationService(
		orchestrationReviewExecutor{order: order, result: testReviewExecutionResult()},
		orchestrationReviewStore{order: order, record: record, err: saveErr},
		publisher,
	)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testReviewExecutionResult() review.ExecutionResult {
	return review.ExecutionResult{
		HumanMarkdown: "## レビュー\n\n問題ありません。",
		Decision:      review.Decision{Verdict: review.VerdictApprove, Issues: []review.Issue{}},
		ReviewerID:    "QA-001", TaskID: "TASK-001", Runner: "ClaudeRunner", Model: "Claude Sonnet 5",
	}
}

func testReviewOrchestrationRequest() review.OrchestrationRequest {
	return review.OrchestrationRequest{
		ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", TaskTitle: "要件を整理する",
		ReviewedAt: time.Date(2026, time.August, 6, 17, 0, 0, 0, time.UTC),
	}
}
