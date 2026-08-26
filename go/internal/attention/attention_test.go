package attention

import (
	"testing"
	"time"
)

func TestDedupeKeepsFirstPerTypeAndEntityID(t *testing.T) {
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	items := []Item{
		{Type: TypeRoutineRecoveryRequired, EntityID: "ROUTINE-1", Summary: "first", ObservedAt: at},
		{Type: TypeRoutineRecoveryRequired, EntityID: "ROUTINE-1", Summary: "second", ObservedAt: at},
		{Type: TypeApprovalRequired, EntityID: "ROUTINE-1", Summary: "different type, same entity", ObservedAt: at},
	}
	deduped := Dedupe(items)
	if len(deduped) != 2 {
		t.Fatalf("Dedupe() = %#v, want 2 items", deduped)
	}
	if deduped[0].Summary != "first" {
		t.Fatalf("Dedupe() kept %q, want the first occurrence", deduped[0].Summary)
	}
}

func TestSortOrdersByTypeThenObservedAtThenEntityID(t *testing.T) {
	early := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	late := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	items := []Item{
		{Type: TypeRoutineRecoveryRequired, EntityID: "ROUTINE-1", ObservedAt: early},
		{Type: TypeApprovalRequired, EntityID: "B", ObservedAt: late},
		{Type: TypeApprovalRequired, EntityID: "A", ObservedAt: late},
		{Type: TypeHumanInputRequired, EntityID: "X", ObservedAt: early},
	}
	Sort(items)
	want := []string{"A", "B", "X", "ROUTINE-1"}
	for index, id := range want {
		if items[index].EntityID != id {
			t.Fatalf("Sort() order = %#v, want EntityIDs in order %v", items, want)
		}
	}
}

func TestSortIsStableAndDeterministicAcrossRepeatedCalls(t *testing.T) {
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	build := func() []Item {
		return []Item{
			{Type: TypeRoutineRecoveryRequired, EntityID: "ROUTINE-2", ObservedAt: at},
			{Type: TypeApprovalRequired, EntityID: "SESSION-1", ObservedAt: at},
			{Type: TypeRoutineRecoveryRequired, EntityID: "ROUTINE-1", ObservedAt: at},
		}
	}
	first := build()
	Sort(first)
	second := build()
	Sort(second)
	for index := range first {
		if first[index] != second[index] {
			t.Fatalf("Sort() is not deterministic: first=%#v second=%#v", first, second)
		}
	}
}
