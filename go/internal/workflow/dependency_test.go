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

// TestParseDependenciesRejectsNonCanonicalTaskID is a regression test for a
// duplicate-validation gap found in the repository audit: this package used
// to accept any TASK-<digits> shape (e.g. "TASK-1", unpadded), which
// task.ParseTaskID -- the canonical Task ID validator every other Task
// Store/Domain boundary uses -- would reject. Both the row's own Task ID and
// a Depends On reference must now agree with the same canonical shape.
func TestParseDependenciesRejectsNonCanonicalTaskID(t *testing.T) {
	tests := []struct {
		name     string
		markdown string
	}{
		{"unpadded task id", "| TASK-1 | PROPOSED-001 | なし | test |"},
		{"unpadded depends on", "| TASK-001 | PROPOSED-001 | なし | test |\n| TASK-002 | PROPOSED-002 | TASK-1 | test |"},
		{"two-digit task id", "| TASK-01 | PROPOSED-001 | なし | test |"},
		{"non-numeric suffix", "| TASK-abc | PROPOSED-001 | なし | test |"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseDependencies(test.markdown); err == nil {
				t.Fatalf("ParseDependencies(%q) expected an error", test.markdown)
			}
		})
	}
}

// TestParseDependenciesAcceptsCanonicalTaskIDsAtAndBeyondThreeDigits proves
// the shared validator still accepts every canonical shape (three digits and
// beyond, matching task.ParseTaskID's own \d{3,} pattern), not just the
// three-digit IDs the other fixtures happen to use.
func TestParseDependenciesAcceptsCanonicalTaskIDsAtAndBeyondThreeDigits(t *testing.T) {
	markdown := "| TASK-999 | PROPOSED-001 | なし | test |\n| TASK-1000 | PROPOSED-002 | TASK-999 | test |"
	got, err := ParseDependencies(markdown)
	if err != nil {
		t.Fatalf("ParseDependencies() error = %v", err)
	}
	want := []Dependency{
		{TaskID: "TASK-999", DependsOn: []string{}},
		{TaskID: "TASK-1000", DependsOn: []string{"TASK-999"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseDependencies() = %#v, want %#v", got, want)
	}
}
