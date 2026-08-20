package workflow

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type readinessFixture struct {
	Cases  []readinessCase `json:"cases"`
	Errors []readinessCase `json:"errors"`
}

type readinessCase struct {
	Name                string            `json:"name"`
	Tasks               []Task            `json:"tasks"`
	Dependencies        []Dependency      `json:"dependencies"`
	ExistingEmployeeIDs []string          `json:"existing_employee_ids"`
	Expected            readinessExpected `json:"expected"`
	ErrorKind           string            `json:"error_kind"`
}

type readinessExpected struct {
	TaskID    string   `json:"task_id"`
	Ready     bool     `json:"ready"`
	State     State    `json:"state"`
	BlockedBy []string `json:"blocked_by"`
	Reason    string   `json:"reason"`
}

func TestSharedReadinessFixture(t *testing.T) {
	fixture := loadReadinessFixture(t)
	for _, testCase := range fixture.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			result, err := EvaluateReadiness(
				testCase.Tasks,
				testCase.Dependencies,
				employeeSet(testCase.ExistingEmployeeIDs),
			)
			if err != nil {
				t.Fatalf("EvaluateReadiness() error = %v", err)
			}
			if result.TaskID != testCase.Expected.TaskID ||
				result.Ready != testCase.Expected.Ready ||
				result.State != testCase.Expected.State ||
				result.Reason != testCase.Expected.Reason ||
				!reflect.DeepEqual(result.BlockedBy, testCase.Expected.BlockedBy) {
				t.Fatalf("EvaluateReadiness() = %#v, expected %#v", result, testCase.Expected)
			}
		})
	}
}

func TestSharedErrorFixture(t *testing.T) {
	fixture := loadReadinessFixture(t)
	for _, testCase := range fixture.Errors {
		t.Run(testCase.Name, func(t *testing.T) {
			_, err := EvaluateReadiness(
				testCase.Tasks,
				testCase.Dependencies,
				employeeSet(testCase.ExistingEmployeeIDs),
			)
			if err == nil {
				t.Fatal("EvaluateReadiness() expected an error")
			}
			switch testCase.ErrorKind {
			case "unknown_dependency":
				if !errors.Is(err, ErrUnknownDependency) {
					t.Fatalf("error = %v, want ErrUnknownDependency", err)
				}
			case "cyclic_dependency":
				if !errors.Is(err, ErrCyclicDependency) {
					t.Fatalf("error = %v, want ErrCyclicDependency", err)
				}
			default:
				t.Fatalf("unknown fixture error kind: %s", testCase.ErrorKind)
			}
		})
	}
}

func TestEvaluateTaskReadinessSelectsRevisionAheadOfEarlierUnrelatedTask(t *testing.T) {
	employeeID := "PLAN-001"
	tasks := []Task{
		{ID: "TASK-001", Title: "source", AssigneeID: &employeeID, Status: StatusCompleted},
		{ID: "TASK-002", Title: "next main task", AssigneeID: &employeeID, Status: StatusUnstarted},
		{ID: "TASK-003", Title: "revision", AssigneeID: &employeeID, Status: StatusUnstarted},
	}
	result, err := EvaluateTaskReadiness("TASK-003", tasks, []Dependency{
		{TaskID: "TASK-002", DependsOn: []string{"TASK-001"}},
		{TaskID: "TASK-003", DependsOn: []string{}},
	}, map[string]bool{employeeID: true})
	if err != nil || !result.Ready || result.TaskID != "TASK-003" || result.State != StateReady {
		t.Fatalf("EvaluateTaskReadiness() = %#v, %v", result, err)
	}
	sequential, err := EvaluateReadiness(tasks, []Dependency{
		{TaskID: "TASK-002", DependsOn: []string{"TASK-001"}},
		{TaskID: "TASK-003", DependsOn: []string{}},
	}, map[string]bool{employeeID: true})
	if err != nil || sequential.TaskID != "TASK-002" {
		t.Fatalf("EvaluateReadiness() = %#v, %v", sequential, err)
	}
}

func TestEvaluateAllReadinessNoDependencies(t *testing.T) {
	employeeID := "PLAN-001"
	tasks := []Task{
		{ID: "TASK-001", Title: "A", AssigneeID: &employeeID, Status: StatusUnstarted},
		{ID: "TASK-002", Title: "B", AssigneeID: &employeeID, Status: StatusUnstarted},
		{ID: "TASK-003", Title: "C", AssigneeID: &employeeID, Status: StatusUnstarted},
	}
	ready, err := EvaluateAllReadiness(tasks, nil, map[string]bool{employeeID: true})
	if err != nil {
		t.Fatalf("EvaluateAllReadiness() error = %v", err)
	}
	assertReadyIDs(t, ready, []string{"TASK-001", "TASK-002", "TASK-003"})
}

func TestEvaluateAllReadinessSingleDependencyBlocksUntilComplete(t *testing.T) {
	employeeID := "PLAN-001"
	dependencies := []Dependency{{TaskID: "TASK-002", DependsOn: []string{"TASK-001"}}}
	incomplete := []Task{
		{ID: "TASK-001", Title: "A", AssigneeID: &employeeID, Status: StatusUnstarted},
		{ID: "TASK-002", Title: "B", AssigneeID: &employeeID, Status: StatusUnstarted},
	}
	ready, err := EvaluateAllReadiness(incomplete, dependencies, map[string]bool{employeeID: true})
	if err != nil {
		t.Fatalf("EvaluateAllReadiness() error = %v", err)
	}
	assertReadyIDs(t, ready, []string{"TASK-001"})

	completed := []Task{
		{ID: "TASK-001", Title: "A", AssigneeID: &employeeID, Status: StatusCompleted},
		{ID: "TASK-002", Title: "B", AssigneeID: &employeeID, Status: StatusUnstarted},
	}
	ready, err = EvaluateAllReadiness(completed, dependencies, map[string]bool{employeeID: true})
	if err != nil {
		t.Fatalf("EvaluateAllReadiness() error = %v", err)
	}
	assertReadyIDs(t, ready, []string{"TASK-002"})
}

func TestEvaluateAllReadinessMultipleDependenciesRequireAllComplete(t *testing.T) {
	employeeID := "PLAN-001"
	dependencies := []Dependency{{TaskID: "TASK-004", DependsOn: []string{"TASK-001", "TASK-002", "TASK-003"}}}
	partiallyDone := []Task{
		{ID: "TASK-001", Title: "A", AssigneeID: &employeeID, Status: StatusCompleted},
		{ID: "TASK-002", Title: "B", AssigneeID: &employeeID, Status: StatusCompleted},
		{ID: "TASK-003", Title: "C", AssigneeID: &employeeID, Status: StatusUnstarted},
		{ID: "TASK-004", Title: "Synthesis", AssigneeID: &employeeID, Status: StatusUnstarted},
	}
	ready, err := EvaluateAllReadiness(partiallyDone, dependencies, map[string]bool{employeeID: true})
	if err != nil {
		t.Fatalf("EvaluateAllReadiness() error = %v", err)
	}
	// TASK-003 has no dependency of its own and is unstarted, so it is ready;
	// TASK-004 (the Synthesis Task) is still blocked by TASK-003.
	assertReadyIDs(t, ready, []string{"TASK-003"})

	allDone := []Task{
		{ID: "TASK-001", Title: "A", AssigneeID: &employeeID, Status: StatusCompleted},
		{ID: "TASK-002", Title: "B", AssigneeID: &employeeID, Status: StatusCompleted},
		{ID: "TASK-003", Title: "C", AssigneeID: &employeeID, Status: StatusCompleted},
		{ID: "TASK-004", Title: "Synthesis", AssigneeID: &employeeID, Status: StatusUnstarted},
	}
	ready, err = EvaluateAllReadiness(allDone, dependencies, map[string]bool{employeeID: true})
	if err != nil {
		t.Fatalf("EvaluateAllReadiness() error = %v", err)
	}
	assertReadyIDs(t, ready, []string{"TASK-004"})
}

// TestEvaluateAllReadinessSynthesisFansIn is the direct fan-out/fan-in shape
// ADR-0051 describes: A/B/C run independently, and only once all three are
// complete does the Synthesis Task (S, depending on all three) become ready.
func TestEvaluateAllReadinessSynthesisFansIn(t *testing.T) {
	employeeID := "PLAN-001"
	tasks := []Task{
		{ID: "TASK-A", Title: "Research", AssigneeID: &employeeID, Status: StatusUnstarted},
		{ID: "TASK-B", Title: "Competitor analysis", AssigneeID: &employeeID, Status: StatusUnstarted},
		{ID: "TASK-C", Title: "Customer analysis", AssigneeID: &employeeID, Status: StatusUnstarted},
		{ID: "TASK-S", Title: "Synthesis", AssigneeID: &employeeID, Status: StatusUnstarted},
	}
	dependencies := []Dependency{{TaskID: "TASK-S", DependsOn: []string{"TASK-A", "TASK-B", "TASK-C"}}}
	ready, err := EvaluateAllReadiness(tasks, dependencies, map[string]bool{employeeID: true})
	if err != nil {
		t.Fatalf("EvaluateAllReadiness() error = %v", err)
	}
	assertReadyIDs(t, ready, []string{"TASK-A", "TASK-B", "TASK-C"})

	tasks[0].Status, tasks[1].Status, tasks[2].Status = StatusCompleted, StatusCompleted, StatusCompleted
	ready, err = EvaluateAllReadiness(tasks, dependencies, map[string]bool{employeeID: true})
	if err != nil {
		t.Fatalf("EvaluateAllReadiness() error = %v", err)
	}
	assertReadyIDs(t, ready, []string{"TASK-S"})
}

func TestEvaluateAllReadinessExcludesCompletedTasks(t *testing.T) {
	employeeID := "PLAN-001"
	tasks := []Task{
		{ID: "TASK-001", Title: "A", AssigneeID: &employeeID, Status: StatusCompleted},
		{ID: "TASK-002", Title: "B", AssigneeID: &employeeID, Status: StatusUnstarted},
	}
	ready, err := EvaluateAllReadiness(tasks, nil, map[string]bool{employeeID: true})
	if err != nil {
		t.Fatalf("EvaluateAllReadiness() error = %v", err)
	}
	assertReadyIDs(t, ready, []string{"TASK-002"})
}

func TestEvaluateAllReadinessExcludesNonExecutableStates(t *testing.T) {
	employeeID := "PLAN-001"
	tasks := []Task{
		{ID: "TASK-001", Title: "in progress", AssigneeID: &employeeID, Status: "進行中"},
		{ID: "TASK-002", Title: "on hold", AssigneeID: &employeeID, Status: "保留"},
		{ID: "TASK-003", Title: "ready", AssigneeID: &employeeID, Status: StatusUnstarted},
	}
	ready, err := EvaluateAllReadiness(tasks, nil, map[string]bool{employeeID: true})
	if err != nil {
		t.Fatalf("EvaluateAllReadiness() error = %v", err)
	}
	assertReadyIDs(t, ready, []string{"TASK-003"})
}

func TestEvaluateAllReadinessExcludesMissingOrUnknownAssignee(t *testing.T) {
	employeeID := "PLAN-001"
	tasks := []Task{
		{ID: "TASK-001", Title: "no assignee", AssigneeID: nil, Status: StatusUnstarted},
		{ID: "TASK-002", Title: "unknown assignee", AssigneeID: strPtr("GHOST-001"), Status: StatusUnstarted},
		{ID: "TASK-003", Title: "known assignee", AssigneeID: &employeeID, Status: StatusUnstarted},
	}
	ready, err := EvaluateAllReadiness(tasks, nil, map[string]bool{employeeID: true})
	if err != nil {
		t.Fatalf("EvaluateAllReadiness() error = %v", err)
	}
	assertReadyIDs(t, ready, []string{"TASK-003"})
}

func TestEvaluateAllReadinessMissingDependencyIsError(t *testing.T) {
	employeeID := "PLAN-001"
	tasks := []Task{{ID: "TASK-001", Title: "A", AssigneeID: &employeeID, Status: StatusUnstarted}}
	dependencies := []Dependency{{TaskID: "TASK-001", DependsOn: []string{"TASK-999"}}}
	if _, err := EvaluateAllReadiness(tasks, dependencies, map[string]bool{employeeID: true}); !errors.Is(err, ErrUnknownDependency) {
		t.Fatalf("EvaluateAllReadiness() error = %v, want ErrUnknownDependency", err)
	}
}

func TestEvaluateAllReadinessCyclicDependencyIsError(t *testing.T) {
	employeeID := "PLAN-001"
	tasks := []Task{
		{ID: "TASK-001", Title: "A", AssigneeID: &employeeID, Status: StatusUnstarted},
		{ID: "TASK-002", Title: "B", AssigneeID: &employeeID, Status: StatusUnstarted},
		{ID: "TASK-003", Title: "C", AssigneeID: &employeeID, Status: StatusUnstarted},
	}
	dependencies := []Dependency{
		{TaskID: "TASK-001", DependsOn: []string{"TASK-003"}},
		{TaskID: "TASK-002", DependsOn: []string{"TASK-001"}},
		{TaskID: "TASK-003", DependsOn: []string{"TASK-002"}},
	}
	if _, err := EvaluateAllReadiness(tasks, dependencies, map[string]bool{employeeID: true}); !errors.Is(err, ErrCyclicDependency) {
		t.Fatalf("EvaluateAllReadiness() error = %v, want ErrCyclicDependency", err)
	}
}

// TestEvaluateAllReadinessDeterministicOrdering runs the same input through
// EvaluateAllReadiness repeatedly and requires byte-identical output order
// every time -- it must never depend on map iteration order, only on the
// order Tasks were given in.
func TestEvaluateAllReadinessDeterministicOrdering(t *testing.T) {
	employeeID := "PLAN-001"
	tasks := []Task{
		{ID: "TASK-005", Title: "E", AssigneeID: &employeeID, Status: StatusUnstarted},
		{ID: "TASK-003", Title: "C", AssigneeID: &employeeID, Status: StatusUnstarted},
		{ID: "TASK-001", Title: "A", AssigneeID: &employeeID, Status: StatusUnstarted},
		{ID: "TASK-004", Title: "D", AssigneeID: &employeeID, Status: StatusUnstarted},
		{ID: "TASK-002", Title: "B", AssigneeID: &employeeID, Status: StatusUnstarted},
	}
	want := []string{"TASK-005", "TASK-003", "TASK-001", "TASK-004", "TASK-002"}
	for attempt := 0; attempt < 20; attempt++ {
		ready, err := EvaluateAllReadiness(tasks, nil, map[string]bool{employeeID: true})
		if err != nil {
			t.Fatalf("EvaluateAllReadiness() error = %v", err)
		}
		assertReadyIDs(t, ready, want)
	}
}

func TestEvaluateAllReadinessEmptyWorkflow(t *testing.T) {
	ready, err := EvaluateAllReadiness(nil, nil, map[string]bool{})
	if err != nil {
		t.Fatalf("EvaluateAllReadiness() error = %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("EvaluateAllReadiness() = %#v, want empty", ready)
	}
}

func assertReadyIDs(t *testing.T, ready []ReadinessResult, want []string) {
	t.Helper()
	got := make([]string, len(ready))
	for index, result := range ready {
		got[index] = result.TaskID
		if !result.Ready || result.State != StateReady {
			t.Fatalf("EvaluateAllReadiness() result %#v is not marked ready", result)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EvaluateAllReadiness() task IDs = %v, want %v", got, want)
	}
}

func strPtr(value string) *string { return &value }

func loadReadinessFixture(t *testing.T) readinessFixture {
	t.Helper()
	path := filepath.Join(
		"..",
		"..",
		"..",
		"fixtures",
		"workflow",
		"readiness_cases.json",
	)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture readinessFixture
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return fixture
}

func employeeSet(employeeIDs []string) map[string]bool {
	employees := make(map[string]bool, len(employeeIDs))
	for _, employeeID := range employeeIDs {
		employees[employeeID] = true
	}
	return employees
}
