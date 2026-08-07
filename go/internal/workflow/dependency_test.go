package workflow

import (
	"reflect"
	"testing"
)

func TestParseDependencies(t *testing.T) {
	markdown := `| Task ID | Proposed ID | Depends On | Rationale |
|---|---|---|---|
| TASK-001 | PROPOSED-001 | なし | A \| B |
| TASK-002 | PROPOSED-002 | TASK-001 | test |
| TASK-003 | PROPOSED-003 | TASK-001, TASK-002 | test |`

	got, err := ParseDependencies(markdown)
	if err != nil {
		t.Fatalf("ParseDependencies() error = %v", err)
	}
	want := []Dependency{
		{TaskID: "TASK-001", DependsOn: []string{}},
		{TaskID: "TASK-002", DependsOn: []string{"TASK-001"}},
		{TaskID: "TASK-003", DependsOn: []string{"TASK-001", "TASK-002"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseDependencies() = %#v, want %#v", got, want)
	}
}

func TestParseDependenciesRejectsInvalidID(t *testing.T) {
	markdown := "| bad-id | PROPOSED-001 | なし | test |"
	if _, err := ParseDependencies(markdown); err == nil {
		t.Fatal("ParseDependencies() expected an error")
	}
}
