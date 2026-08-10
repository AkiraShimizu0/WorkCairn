package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/AkiraShimizu0/workcairn/go/internal/action"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
)

type ActionService struct {
	store     action.Store
	publisher action.Publisher
	events    EventPublisher
}

func NewActionService(store action.Store, publisher action.Publisher, events EventPublisher) (*ActionService, error) {
	if nilActionDependency(store) || nilActionDependency(publisher) || nilActionDependency(events) {
		return nil, errors.New("Action Store, Publisher, and Event publisher are required")
	}
	return &ActionService{store: store, publisher: publisher, events: events}, nil
}

func nilActionDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func (service *ActionService) Execute(ctx context.Context, intent action.Intent) (action.Result, error) {
	if ctx == nil || intent.Validate() != nil {
		return action.Result{}, action.ErrInvalidAction
	}
	result := action.Result{Status: "publishing"}
	intentEvidence, intentErr := service.store.SaveIntent(ctx, intent)
	result.Intent = &intentEvidence
	if !intentEvidence.Committed {
		result.Status = "failed"
		if intentErr == nil {
			intentErr = action.ErrSaveFailed
		}
		return result, intentErr
	}

	publication, publishErr := service.publisher.Publish(ctx, intent)
	if publishErr != nil {
		result.Status = "partial_failure"
		return result, errors.Join(intentErr, publishErr)
	}
	if publication.Validate() != nil {
		result.Status = "partial_failure"
		return result, errors.Join(intentErr, action.ErrInvalidAction)
	}
	result.Publication = &publication
	outcome := action.Outcome{
		SchemaVersion: action.SchemaVersion, ActionID: intent.ActionID, CompletedAt: intent.RequestedAt,
		SourceSHA256: intent.Source.SHA256, Publication: publication,
	}
	outcomeEvidence, outcomeErr := service.store.SaveOutcome(ctx, outcome)
	result.Outcome = &outcomeEvidence
	if !outcomeEvidence.Committed {
		result.Status = "partial_failure"
		if outcomeErr == nil {
			outcomeErr = action.ErrSaveFailed
		}
		return result, errors.Join(intentErr, outcomeErr)
	}

	published, eventErr := newActionCompletedEvent(intent, outcome)
	if eventErr == nil {
		result.EventID = published.ID
		eventErr = service.events.Publish(ctx, published)
		result.EventPublished = eventErr == nil
	}
	if intentErr != nil || outcomeErr != nil || eventErr != nil {
		result.Status = "partial_failure"
		return result, errors.Join(intentErr, outcomeErr, eventErr)
	}
	result.Status = "published"
	return result, nil
}

type actionCompletedPayload struct {
	ActionID     string      `json:"action_id"`
	Kind         action.Kind `json:"kind"`
	TargetID     string      `json:"target_id"`
	ProjectID    string      `json:"project_id"`
	TaskID       string      `json:"task_id"`
	SourceRef    string      `json:"source_reference"`
	SourceSHA256 string      `json:"source_sha256"`
	Provider     string      `json:"provider"`
	ExternalID   string      `json:"external_id"`
	URL          string      `json:"url"`
}

func newActionCompletedEvent(intent action.Intent, outcome action.Outcome) (event.Event, error) {
	payload, err := json.Marshal(actionCompletedPayload{
		ActionID: intent.ActionID, Kind: intent.Kind, TargetID: intent.TargetID,
		ProjectID: intent.Source.ProjectID, TaskID: intent.Source.TaskID,
		SourceRef: intent.Source.Reference, SourceSHA256: intent.Source.SHA256,
		Provider: outcome.Publication.Provider, ExternalID: outcome.Publication.ExternalID, URL: outcome.Publication.URL,
	})
	if err != nil {
		return event.Event{}, fmt.Errorf("encode Action Event: %w", err)
	}
	return event.New(event.ActionCompleted, "action", intent.ActionID, payload)
}
