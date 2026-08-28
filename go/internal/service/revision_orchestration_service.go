package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/revision"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

type RevisionTaskCreator interface {
	Create(ctx context.Context, input task.CreateInput) (task.Task, error)
}

type RevisionOrchestrationError struct {
	Result         revision.Result
	IntentErr      error
	TaskErr        error
	PublicationErr error
}

func (orchestrationError *RevisionOrchestrationError) Error() string {
	return fmt.Sprintf(
		"Revision orchestration partial failure (intent=%t task=%t event=%t)",
		orchestrationError.Result.Intent != nil && orchestrationError.Result.Intent.Committed,
		orchestrationError.Result.Task != nil,
		orchestrationError.Result.EventPublished,
	)
}

func (orchestrationError *RevisionOrchestrationError) Unwrap() []error {
	errors := make([]error, 0, 3)
	for _, err := range []error{orchestrationError.IntentErr, orchestrationError.TaskErr, orchestrationError.PublicationErr} {
		if err != nil {
			errors = append(errors, err)
		}
	}
	return errors
}

type RevisionOrchestrationService struct {
	store  revision.Store
	tasks  RevisionTaskCreator
	events EventPublisher
}

func NewRevisionOrchestrationService(store revision.Store, tasks RevisionTaskCreator, events EventPublisher) (*RevisionOrchestrationService, error) {
	if serviceDependencyIsNil(store) || serviceDependencyIsNil(tasks) || serviceDependencyIsNil(events) {
		return nil, fmt.Errorf("Revision Store, TaskService, and Event publisher are required")
	}
	return &RevisionOrchestrationService{store: store, tasks: tasks, events: events}, nil
}

func (service *RevisionOrchestrationService) Execute(ctx context.Context, intent revision.Intent) (revision.Result, error) {
	result := revision.Result{Status: "failed"}
	record, intentErr := service.store.Save(ctx, intent)
	result.Intent = &record
	if !record.Committed {
		if intentErr == nil {
			intentErr = fmt.Errorf("Revision intent Store returned without committing or reporting an error")
		}
		return result, intentErr
	}

	assigneeID := intent.AssigneeID
	created, taskErr := service.tasks.Create(ctx, task.CreateInput{
		ID: intent.RevisionTaskID, Title: intent.Title, AssigneeID: &assigneeID,
	})
	taskCommitted := taskErr == nil
	if taskErr != nil {
		var publicationError *EventPublicationError
		taskCommitted = errors.As(taskErr, &publicationError) && publicationError.Task.ID == created.ID
	}
	if taskCommitted && (created.ID != intent.RevisionTaskID || created.Validate() != nil) {
		taskCommitted = false
		taskErr = errors.Join(taskErr, fmt.Errorf("Revision TaskService returned an invalid Task result"))
	}
	if taskCommitted {
		cloned := created.Clone()
		result.Task = &cloned
	}
	if result.Task == nil {
		result.Status = "partial_failure"
		return result, &RevisionOrchestrationError{Result: result, IntentErr: intentErr, TaskErr: taskErr}
	}

	published, eventErr := newRevisionCreatedEvent(intent, record, created)
	if eventErr == nil {
		result.EventID = published.ID
		eventErr = service.events.Publish(ctx, published)
		result.EventPublished = eventErr == nil
	}
	if intentErr != nil || taskErr != nil || eventErr != nil {
		result.Status = "partial_failure"
		return result, &RevisionOrchestrationError{
			Result: result, IntentErr: intentErr, TaskErr: taskErr, PublicationErr: eventErr,
		}
	}
	result.Status = "created"
	return result, nil
}

type revisionCreatedPayload struct {
	ProjectID             string `json:"project_id"`
	ProjectName           string `json:"project_name"`
	SourceTaskID          string `json:"source_task_id"`
	SourceReviewCanonical string `json:"source_review_canonical"`
	RevisionTaskID        string `json:"revision_task_id"`
	AssigneeID            string `json:"assignee_id"`
	IntentPath            string `json:"intent_path"`
	IssueCount            int    `json:"issue_count"`
}

func newRevisionCreatedEvent(intent revision.Intent, record revision.Record, created task.Task) (event.Event, error) {
	payload, err := json.Marshal(revisionCreatedPayload{
		ProjectID: intent.ProjectID, ProjectName: intent.ProjectName,
		SourceTaskID: intent.SourceTaskID, SourceReviewCanonical: intent.SourceReview,
		RevisionTaskID: created.ID, AssigneeID: intent.AssigneeID,
		IntentPath: record.RelativePath, IssueCount: len(intent.ReviewDecision.Issues),
	})
	if err != nil {
		return event.Event{}, err
	}
	return event.New(event.RevisionCreated, "revision", created.ID, payload)
}
