package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
)

func TestEventServiceLifecycleAndDelivery(t *testing.T) {
	service := NewEventService(nil)
	var delivered bool
	_, err := service.Subscribe(event.TaskStarted, func(context.Context, event.Event) error {
		delivered = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	published, err := event.New(event.TaskStarted, "task", "TASK-001", json.RawMessage(`{"status":"進行中"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Publish(context.Background(), published); !errors.Is(err, ErrEventServiceNotStarted) {
		t.Fatalf("Publish() before Start error = %v", err)
	}
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	if err := service.Start(); !errors.Is(err, ErrEventServiceAlreadyStarted) {
		t.Fatalf("second Start() error = %v", err)
	}
	if err := service.Publish(context.Background(), published); err != nil {
		t.Fatal(err)
	}
	if !delivered {
		t.Fatal("event was not delivered")
	}
	if err := service.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := service.Publish(context.Background(), published); !errors.Is(err, ErrEventServiceNotStarted) {
		t.Fatalf("Publish() after Stop error = %v", err)
	}
	if err := service.Stop(); !errors.Is(err, ErrEventServiceNotStarted) {
		t.Fatalf("second Stop() error = %v", err)
	}
}
