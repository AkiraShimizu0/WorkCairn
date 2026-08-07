package task

import (
	"errors"
	"testing"
)

func TestTaskLifecycle(t *testing.T) {
	assigneeID := "DEV-001"
	created, err := New(CreateInput{ID: "TASK-001", Title: "実装", AssigneeID: &assigneeID})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != StatusUnstarted || created.Version != 1 {
		t.Fatalf("created task = %#v", created)
	}
	started, err := created.Start()
	if err != nil || started.Status != StatusInProgress || started.Version != 2 {
		t.Fatalf("Start() = %#v, %v", started, err)
	}
	failed, err := started.Fail("runner unavailable")
	if err != nil || failed.Status != StatusInProgress || failed.Version != 3 || failed.LastFailureReason == "" {
		t.Fatalf("Fail() = %#v, %v", failed, err)
	}
	held, err := failed.Hold("manual review")
	if err != nil || held.Status != StatusOnHold || held.Version != 4 || held.HoldReason == "" {
		t.Fatalf("Hold() = %#v, %v", held, err)
	}
	resumed, err := held.Resume()
	if err != nil || resumed.Status != StatusUnstarted || resumed.Version != 5 || resumed.HoldReason != "" {
		t.Fatalf("Resume() = %#v, %v", resumed, err)
	}
	restarted, err := resumed.Start()
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.Complete()
	if err != nil || completed.Status != StatusCompleted || completed.Version != 7 {
		t.Fatalf("Complete() = %#v, %v", completed, err)
	}
}

func TestTaskRejectsInvalidLifecycle(t *testing.T) {
	created, _ := New(CreateInput{ID: "TASK-001", Title: "test"})
	if _, err := created.Complete(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("unstarted Complete() error = %v", err)
	}
	if _, err := created.Fail("failed"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("unstarted Fail() error = %v", err)
	}
	started, _ := created.Start()
	completed, _ := started.Complete()
	operations := []struct {
		name string
		run  func() error
	}{
		{"start", func() error { _, err := completed.Start(); return err }},
		{"complete", func() error { _, err := completed.Complete(); return err }},
		{"fail", func() error { _, err := completed.Fail("failed"); return err }},
		{"hold", func() error { _, err := completed.Hold("hold"); return err }},
		{"resume", func() error { _, err := completed.Resume(); return err }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("operation error = %v", err)
			}
		})
	}
}

func TestTaskRequiresFailureAndHoldReasons(t *testing.T) {
	created, _ := New(CreateInput{ID: "TASK-001", Title: "test"})
	started, _ := created.Start()
	if _, err := started.Fail(" "); !errors.Is(err, ErrInvalidReason) {
		t.Fatalf("Fail() error = %v", err)
	}
	if _, err := started.Hold(""); !errors.Is(err, ErrInvalidReason) {
		t.Fatalf("Hold() error = %v", err)
	}
}
