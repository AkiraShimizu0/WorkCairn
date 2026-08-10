package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/revision"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

type orchestrationRevisionStore struct {
	order  *[]string
	record revision.Record
	err    error
}

func (fake orchestrationRevisionStore) Save(context.Context, revision.Intent) (revision.Record, error) {
	*fake.order = append(*fake.order, "intent")
	return fake.record, fake.err
}

type orchestrationTaskCreator struct {
	order *[]string
	task  task.Task
	err   error
}

func (fake orchestrationTaskCreator) Create(context.Context, task.CreateInput) (task.Task, error) {
	*fake.order = append(*fake.order, "task")
	return fake.task, fake.err
}

func TestRevisionOrchestrationCommitsIntentThenTaskThenEvent(t *testing.T) {
	order := []string{}
	publisher := &orchestrationEventPublisher{order: &order}
	service := newTestRevisionOrchestration(t, &order, revision.Record{
		RevisionTaskID: "TASK-002", RelativePath: "Revisions/TASK-002.revision.md", Committed: true,
	}, nil, testRevisionTask(), nil, publisher)

	result, err := service.Execute(context.Background(), testRevisionIntent())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "created" || result.Task == nil || result.Task.ID != "TASK-002" ||
		!result.EventPublished || result.EventID == "" || !reflect.DeepEqual(order, []string{"intent", "task", "publish"}) {
		t.Fatalf("result=%#v order=%#v", result, order)
	}
	if len(publisher.published) != 1 || publisher.published[0].Type != event.RevisionCreated ||
		publisher.published[0].AggregateType != "revision" || publisher.published[0].AggregateID != "TASK-002" {
		t.Fatalf("published=%#v", publisher.published)
	}
}

func TestRevisionOrchestrationDoesNotCreateTaskWithoutIntentCommit(t *testing.T) {
	order := []string{}
	intentErr := &revision.SaveError{Err: errors.New("write failed")}
	publisher := &orchestrationEventPublisher{order: &order}
	service := newTestRevisionOrchestration(t, &order, revision.Record{}, intentErr, testRevisionTask(), nil, publisher)

	result, err := service.Execute(context.Background(), testRevisionIntent())
	if !errors.Is(err, revision.ErrSaveFailed) || result.Status != "failed" ||
		!reflect.DeepEqual(order, []string{"intent"}) || result.Task != nil || result.EventPublished {
		t.Fatalf("result=%#v err=%v order=%#v", result, err, order)
	}
}

func TestRevisionOrchestrationRejectsAmbiguousUncommittedStoreResult(t *testing.T) {
	order := []string{}
	publisher := &orchestrationEventPublisher{order: &order}
	service := newTestRevisionOrchestration(t, &order, revision.Record{}, nil, testRevisionTask(), nil, publisher)

	result, err := service.Execute(context.Background(), testRevisionIntent())
	if err == nil || result.Status != "failed" || !reflect.DeepEqual(order, []string{"intent"}) || result.Task != nil {
		t.Fatalf("result=%#v err=%v order=%#v", result, err, order)
	}
}

func TestRevisionOrchestrationKeepsCommittedIntentWhenTaskCreateFails(t *testing.T) {
	order := []string{}
	taskErr := errors.New("Task Store failed")
	publisher := &orchestrationEventPublisher{order: &order}
	service := newTestRevisionOrchestration(t, &order, revision.Record{
		RevisionTaskID: "TASK-002", RelativePath: "Revisions/TASK-002.revision.md", Committed: true,
	}, nil, testRevisionTask(), taskErr, publisher)

	result, err := service.Execute(context.Background(), testRevisionIntent())
	var orchestrationError *RevisionOrchestrationError
	if !errors.As(err, &orchestrationError) || !errors.Is(err, taskErr) || result.Status != "partial_failure" ||
		result.Intent == nil || !result.Intent.Committed || result.Task != nil || result.EventPublished ||
		!reflect.DeepEqual(order, []string{"intent", "task"}) {
		t.Fatalf("result=%#v err=%v order=%#v", result, err, order)
	}
}

func TestRevisionOrchestrationPublishesRevisionAfterTaskPublicationFailure(t *testing.T) {
	order := []string{}
	taskPublishErr := &EventPublicationError{Task: testRevisionTask(), EventType: event.TaskCreated, EventID: "task-event", Err: errors.New("Task audit failed")}
	publisher := &orchestrationEventPublisher{order: &order}
	service := newTestRevisionOrchestration(t, &order, revision.Record{
		RevisionTaskID: "TASK-002", RelativePath: "Revisions/TASK-002.revision.md", Committed: true,
	}, nil, testRevisionTask(), taskPublishErr, publisher)

	result, err := service.Execute(context.Background(), testRevisionIntent())
	var orchestrationError *RevisionOrchestrationError
	if !errors.As(err, &orchestrationError) || !errors.Is(err, taskPublishErr.Err) ||
		result.Status != "partial_failure" || result.Task == nil || !result.EventPublished ||
		!reflect.DeepEqual(order, []string{"intent", "task", "publish"}) {
		t.Fatalf("result=%#v err=%v order=%#v", result, err, order)
	}
}

func TestRevisionOrchestrationKeepsIntentAndTaskOnRevisionEventFailure(t *testing.T) {
	order := []string{}
	publishErr := errors.New("Revision audit failed")
	publisher := &orchestrationEventPublisher{order: &order, err: publishErr}
	service := newTestRevisionOrchestration(t, &order, revision.Record{
		RevisionTaskID: "TASK-002", RelativePath: "Revisions/TASK-002.revision.md", Committed: true,
	}, nil, testRevisionTask(), nil, publisher)

	result, err := service.Execute(context.Background(), testRevisionIntent())
	if !errors.Is(err, publishErr) || result.Status != "partial_failure" || result.Task == nil ||
		result.EventPublished || result.EventID == "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func newTestRevisionOrchestration(
	t *testing.T,
	order *[]string,
	record revision.Record,
	intentErr error,
	created task.Task,
	taskErr error,
	publisher *orchestrationEventPublisher,
) *RevisionOrchestrationService {
	t.Helper()
	service, err := NewRevisionOrchestrationService(
		orchestrationRevisionStore{order: order, record: record, err: intentErr},
		orchestrationTaskCreator{order: order, task: created, err: taskErr},
		publisher,
	)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testRevisionTask() task.Task {
	assigneeID := "PLAN-001"
	created, _ := task.New(task.CreateInput{ID: "TASK-002", Title: "TASK-001のレビュー指摘を反映する", AssigneeID: &assigneeID})
	return created
}

func testRevisionIntent() revision.Intent {
	return revision.Intent{
		ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", SourceTaskID: "TASK-001",
		SourceReview: "Reviews/TASK-001.review.json", SourceProjection: "Reviews/TASK-001.review.md",
		ReviewDecision: testRequestChangesDecision(), AssigneeID: "PLAN-001",
		RevisionTaskID: "TASK-002", Title: "TASK-001のレビュー指摘を反映する",
		CreatedAt: testReviewOrchestrationRequest().ReviewedAt,
	}
}

func testRequestChangesDecision() review.Decision {
	return review.Decision{Verdict: review.VerdictRequestChanges, Issues: []review.Issue{{
		Category: "requirements", Severity: "medium", Description: "要件が不足", SuggestedAction: "要件を追加する",
	}}}
}
