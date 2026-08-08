package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
	"github.com/AkiraShimizu0/workspace-os/go/internal/review"
)

type ReviewExecutor interface {
	Execute(ctx context.Context, input review.PromptInput) (review.ExecutionResult, error)
}

type ReviewOrchestrationError struct {
	Result         review.OrchestrationResult
	ArtifactErr    error
	PublicationErr error
}

func (orchestrationError *ReviewOrchestrationError) Error() string {
	return fmt.Sprintf(
		"Review orchestration partial failure (canonical=%t projection=%t event=%t)",
		orchestrationError.Result.Artifact != nil && orchestrationError.Result.Artifact.CanonicalCommitted,
		orchestrationError.Result.Artifact != nil && orchestrationError.Result.Artifact.ProjectionCommitted,
		orchestrationError.Result.EventPublished,
	)
}

func (orchestrationError *ReviewOrchestrationError) Unwrap() []error {
	errors := make([]error, 0, 2)
	if orchestrationError.ArtifactErr != nil {
		errors = append(errors, orchestrationError.ArtifactErr)
	}
	if orchestrationError.PublicationErr != nil {
		errors = append(errors, orchestrationError.PublicationErr)
	}
	return errors
}

type ReviewOrchestrationService struct {
	executor ReviewExecutor
	store    review.Store
	events   EventPublisher
}

func NewReviewOrchestrationService(executor ReviewExecutor, store review.Store, events EventPublisher) (*ReviewOrchestrationService, error) {
	if serviceDependencyIsNil(executor) || serviceDependencyIsNil(store) || serviceDependencyIsNil(events) {
		return nil, fmt.Errorf("Review executor, Store, and Event publisher are required")
	}
	return &ReviewOrchestrationService{executor: executor, store: store, events: events}, nil
}

func (service *ReviewOrchestrationService) Execute(ctx context.Context, request review.OrchestrationRequest) (review.OrchestrationResult, error) {
	executionResult, err := service.executor.Execute(ctx, request.PromptInput)
	if err != nil {
		return review.OrchestrationResult{Status: "failed"}, err
	}
	result := review.OrchestrationResult{Status: "executed", Execution: &executionResult}
	record, artifactErr := service.store.Save(ctx, review.Document{
		ProjectID: request.ProjectID, ProjectName: request.ProjectName, TaskTitle: request.TaskTitle,
		ReviewedAt: request.ReviewedAt, ReviewVersion: request.ReviewVersion, Execution: executionResult,
	})
	result.Artifact = &record
	if !record.CanonicalCommitted {
		result.Status = "failed"
		return result, artifactErr
	}

	published, eventErr := newReviewCompletedEvent(request, executionResult, record)
	if eventErr == nil {
		result.EventID = published.ID
		eventErr = service.events.Publish(ctx, published)
		result.EventPublished = eventErr == nil
	}
	if artifactErr != nil || eventErr != nil {
		result.Status = "partial_failure"
		return result, &ReviewOrchestrationError{
			Result: result, ArtifactErr: artifactErr, PublicationErr: eventErr,
		}
	}
	result.Status = "reviewed"
	return result, nil
}

type reviewCompletedPayload struct {
	ProjectID           string         `json:"project_id"`
	ProjectName         string         `json:"project_name"`
	TaskID              string         `json:"task_id"`
	ReviewerID          string         `json:"reviewer_id"`
	Verdict             review.Verdict `json:"verdict"`
	ReviewVersion       string         `json:"review_version,omitempty"`
	CanonicalPath       string         `json:"canonical_path"`
	ProjectionPath      string         `json:"projection_path"`
	ProjectionCommitted bool           `json:"projection_committed"`
}

func newReviewCompletedEvent(request review.OrchestrationRequest, execution review.ExecutionResult, record review.Record) (event.Event, error) {
	payload, err := json.Marshal(reviewCompletedPayload{
		ProjectID: request.ProjectID, ProjectName: request.ProjectName,
		TaskID: execution.TaskID, ReviewerID: execution.ReviewerID, Verdict: execution.Decision.Verdict,
		ReviewVersion: request.ReviewVersion, CanonicalPath: record.CanonicalPath,
		ProjectionPath: record.ProjectionPath, ProjectionCommitted: record.ProjectionCommitted,
	})
	if err != nil {
		return event.Event{}, err
	}
	return event.New(event.ReviewCompleted, "review", execution.TaskID, payload)
}
