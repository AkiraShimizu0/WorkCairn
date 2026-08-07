package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testEvent(eventType Type, aggregateID string) Event {
	return Event{
		ID:            fmt.Sprintf("event-%s-%d", aggregateID, time.Now().UnixNano()),
		Type:          eventType,
		Timestamp:     time.Now().UTC(),
		AggregateType: "task",
		AggregateID:   aggregateID,
		Payload:       json.RawMessage(`{"ok":true}`),
	}
}

func TestBusSubscribePublishAndUnsubscribe(t *testing.T) {
	bus := NewBus()
	var calls int
	subscription, err := bus.Subscribe(TaskCreated, func(context.Context, Event) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(context.Background(), testEvent(TaskCreated, "TASK-001")); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d", calls)
	}
	if err := bus.Unsubscribe(subscription); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(context.Background(), testEvent(TaskCreated, "TASK-002")); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("handler calls after unsubscribe = %d", calls)
	}
	if err := bus.Unsubscribe(subscription); !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("second Unsubscribe() error = %v", err)
	}
}

func TestBusPublishWithNoSubscribers(t *testing.T) {
	if err := NewBus().Publish(context.Background(), testEvent(TaskCompleted, "TASK-001")); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
}

func TestBusMultipleSubscribersUseRegistrationOrderAndIsolatedEvents(t *testing.T) {
	bus := NewBus()
	var calls []string
	_, _ = bus.Subscribe(TaskStarted, func(_ context.Context, received Event) error {
		calls = append(calls, "first")
		received.Payload[0] = '['
		received.Metadata["source"] = "changed"
		return nil
	})
	_, _ = bus.Subscribe(TaskStarted, func(_ context.Context, received Event) error {
		calls = append(calls, "second")
		if string(received.Payload) != `{"ok":true}` || received.Metadata["source"] != "test" {
			t.Fatalf("subscriber received mutated event: %#v", received)
		}
		return nil
	})
	published := testEvent(TaskStarted, "TASK-001")
	published.Metadata = map[string]string{"source": "test"}
	if err := bus.Publish(context.Background(), published); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"first", "second"}) {
		t.Fatalf("handler order = %#v", calls)
	}
}

func TestBusHandlerFailureDoesNotSuppressLaterSubscribers(t *testing.T) {
	bus := NewBus()
	wantError := errors.New("audit unavailable")
	var laterCalled bool
	_, _ = bus.Subscribe(TaskFailed, func(context.Context, Event) error { return wantError })
	_, _ = bus.Subscribe(TaskFailed, func(context.Context, Event) error {
		laterCalled = true
		return nil
	})
	err := bus.Publish(context.Background(), testEvent(TaskFailed, "TASK-001"))
	if !errors.Is(err, ErrHandlerFailed) || !errors.Is(err, wantError) {
		t.Fatalf("Publish() error = %v", err)
	}
	if !laterCalled {
		t.Fatal("later subscriber was suppressed")
	}
	var deliveryError *DeliveryError
	if !errors.As(err, &deliveryError) || len(deliveryError.Failures) != 1 {
		t.Fatalf("delivery error = %#v", deliveryError)
	}
}

func TestBusRoutesMultipleEventTypes(t *testing.T) {
	bus := NewBus()
	var taskCalls, workflowCalls int
	_, _ = bus.Subscribe(TaskCompleted, func(context.Context, Event) error { taskCalls++; return nil })
	_, _ = bus.Subscribe(WorkflowCompleted, func(context.Context, Event) error { workflowCalls++; return nil })
	_ = bus.Publish(context.Background(), testEvent(TaskCompleted, "TASK-001"))
	_ = bus.Publish(context.Background(), testEvent(WorkflowCompleted, "PROJECT-001"))
	if taskCalls != 1 || workflowCalls != 1 {
		t.Fatalf("calls = task:%d workflow:%d", taskCalls, workflowCalls)
	}
}

func TestBusSequentialPublishOrder(t *testing.T) {
	bus := NewBus()
	var received []string
	_, _ = bus.Subscribe(TaskCreated, func(_ context.Context, event Event) error {
		received = append(received, event.AggregateID)
		return nil
	})
	for _, taskID := range []string{"TASK-001", "TASK-002", "TASK-003"} {
		if err := bus.Publish(context.Background(), testEvent(TaskCreated, taskID)); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(received, []string{"TASK-001", "TASK-002", "TASK-003"}) {
		t.Fatalf("publish order = %#v", received)
	}
}

func TestBusConcurrentPublishAndSubscribe(t *testing.T) {
	bus := NewBus()
	var calls atomic.Int64
	_, _ = bus.Subscribe(TaskStarted, func(context.Context, Event) error {
		calls.Add(1)
		return nil
	})
	const publications = 100
	var waitGroup sync.WaitGroup
	for index := range publications {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			if err := bus.Publish(context.Background(), testEvent(TaskStarted, fmt.Sprintf("TASK-%03d", index))); err != nil {
				t.Errorf("Publish() error = %v", err)
			}
		}(index)
	}
	for range 25 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			subscription, err := bus.Subscribe(TaskStarted, func(context.Context, Event) error { return nil })
			if err != nil {
				t.Errorf("Subscribe() error = %v", err)
				return
			}
			if err := bus.Unsubscribe(subscription); err != nil {
				t.Errorf("Unsubscribe() error = %v", err)
			}
		}()
	}
	waitGroup.Wait()
	if calls.Load() != publications {
		t.Fatalf("stable subscriber calls = %d, want %d", calls.Load(), publications)
	}
}

func TestBusSubscriptionSnapshotAndNestedPublish(t *testing.T) {
	bus := NewBus()
	var calls []string
	_, _ = bus.Subscribe(TaskCreated, func(ctx context.Context, received Event) error {
		calls = append(calls, "primary:"+received.AggregateID)
		if received.AggregateID == "OUTER" {
			_, _ = bus.Subscribe(TaskCreated, func(_ context.Context, late Event) error {
				calls = append(calls, "late:"+late.AggregateID)
				return nil
			})
			if err := bus.Publish(ctx, testEvent(TaskCreated, "NESTED")); err != nil {
				t.Errorf("nested Publish() error = %v", err)
			}
		}
		return nil
	})
	if err := bus.Publish(context.Background(), testEvent(TaskCreated, "OUTER")); err != nil {
		t.Fatal(err)
	}
	want := []string{"primary:OUTER", "primary:NESTED", "late:NESTED"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("snapshot/nested calls = %#v, want %#v", calls, want)
	}
}

func TestBusRejectsInvalidInputsBeforeDelivery(t *testing.T) {
	bus := NewBus()
	var called bool
	_, _ = bus.Subscribe(TaskCreated, func(context.Context, Event) error { called = true; return nil })
	invalid := testEvent(TaskCreated, "TASK-001")
	invalid.Payload = nil
	if err := bus.Publish(context.Background(), invalid); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("Publish() error = %v", err)
	}
	if called {
		t.Fatal("invalid event was delivered")
	}
	if _, err := bus.Subscribe(Type("unknown"), func(context.Context, Event) error { return nil }); !errors.Is(err, ErrUnknownEventType) {
		t.Fatalf("Subscribe() unknown type error = %v", err)
	}
	if _, err := bus.Subscribe(TaskCreated, nil); !errors.Is(err, ErrNilHandler) {
		t.Fatalf("Subscribe() nil handler error = %v", err)
	}
}
