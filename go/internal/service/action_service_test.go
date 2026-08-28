package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/action"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
)

type fakeActionStore struct {
	order      *[]string
	intent     action.Evidence
	intentErr  error
	outcome    action.Evidence
	outcomeErr error
}

func (store *fakeActionStore) SaveIntent(context.Context, action.Intent) (action.Evidence, error) {
	*store.order = append(*store.order, "intent")
	return store.intent, store.intentErr
}
func (store *fakeActionStore) SaveOutcome(context.Context, action.Outcome) (action.Evidence, error) {
	*store.order = append(*store.order, "outcome")
	return store.outcome, store.outcomeErr
}
func (*fakeActionStore) Exists(context.Context, string) (bool, bool, error) { return false, false, nil }

type fakeActionPublisher struct {
	order       *[]string
	publication action.Publication
	err         error
}

func (publisher *fakeActionPublisher) Publish(context.Context, action.Intent) (action.Publication, error) {
	*publisher.order = append(*publisher.order, "publish")
	return publisher.publication, publisher.err
}

type fakeActionEvents struct {
	order *[]string
	err   error
	event event.Event
}

func (events *fakeActionEvents) Publish(_ context.Context, published event.Event) error {
	*events.order = append(*events.order, "event")
	events.event = published
	return events.err
}

func TestActionServiceCommitOrderingAndPartialFailures(t *testing.T) {
	intent := serviceActionIntent()
	publication := action.Publication{Provider: "wordpress", ExternalID: "9", URL: "https://example.test/9", Status: "published"}
	tests := []struct {
		name      string
		store     func(*[]string) *fakeActionStore
		publisher func(*[]string) *fakeActionPublisher
		eventErr  error
		wantOrder []string
		wantEvent bool
		wantError bool
	}{
		{"success", func(order *[]string) *fakeActionStore {
			return &fakeActionStore{order: order, intent: action.Evidence{Committed: true}, outcome: action.Evidence{Committed: true}}
		}, func(order *[]string) *fakeActionPublisher {
			return &fakeActionPublisher{order: order, publication: publication}
		}, nil, []string{"intent", "publish", "outcome", "event"}, true, false},
		{"intent failure", func(order *[]string) *fakeActionStore {
			return &fakeActionStore{order: order, intentErr: errors.New("intent failed")}
		}, func(order *[]string) *fakeActionPublisher {
			return &fakeActionPublisher{order: order, publication: publication}
		}, nil, []string{"intent"}, false, true},
		{"publish failure", func(order *[]string) *fakeActionStore {
			return &fakeActionStore{order: order, intent: action.Evidence{Committed: true}}
		}, func(order *[]string) *fakeActionPublisher {
			return &fakeActionPublisher{order: order, err: errors.New("publish failed")}
		}, nil, []string{"intent", "publish"}, false, true},
		{"outcome failure", func(order *[]string) *fakeActionStore {
			return &fakeActionStore{order: order, intent: action.Evidence{Committed: true}, outcomeErr: errors.New("outcome failed")}
		}, func(order *[]string) *fakeActionPublisher {
			return &fakeActionPublisher{order: order, publication: publication}
		}, nil, []string{"intent", "publish", "outcome"}, false, true},
		{"committed outcome cleanup failure", func(order *[]string) *fakeActionStore {
			return &fakeActionStore{order: order, intent: action.Evidence{Committed: true}, outcome: action.Evidence{Committed: true}, outcomeErr: errors.New("sync failed")}
		}, func(order *[]string) *fakeActionPublisher {
			return &fakeActionPublisher{order: order, publication: publication}
		}, nil, []string{"intent", "publish", "outcome", "event"}, true, true},
		{"event failure", func(order *[]string) *fakeActionStore {
			return &fakeActionStore{order: order, intent: action.Evidence{Committed: true}, outcome: action.Evidence{Committed: true}}
		}, func(order *[]string) *fakeActionPublisher {
			return &fakeActionPublisher{order: order, publication: publication}
		}, errors.New("event failed"), []string{"intent", "publish", "outcome", "event"}, true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			order := []string{}
			events := &fakeActionEvents{order: &order, err: test.eventErr}
			service, err := NewActionService(test.store(&order), test.publisher(&order), events)
			if err != nil {
				t.Fatal(err)
			}
			result, executeErr := service.Execute(context.Background(), intent)
			if (executeErr != nil) != test.wantError || !reflect.DeepEqual(order, test.wantOrder) || result.EventPublished != (test.wantEvent && test.eventErr == nil) {
				t.Fatalf("Execute() = %#v, %v order=%#v", result, executeErr, order)
			}
			if test.wantEvent && events.event.Type != event.ActionCompleted {
				t.Fatalf("Event = %#v", events.event)
			}
		})
	}
}

func serviceActionIntent() action.Intent {
	return action.Intent{
		SchemaVersion: action.SchemaVersion, ActionID: "CMD-ACTION-001", Kind: action.KindWordPressPublish,
		TargetID: "site-main", RequestedAt: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		Source: action.Source{ProjectID: "PROJECT-001", ProjectName: "P", TaskID: "TASK-001", Reference: "Deliverables/TASK-001.md", SHA256: strings.Repeat("a", 64), Title: "Title", Content: "Body"},
	}
}
