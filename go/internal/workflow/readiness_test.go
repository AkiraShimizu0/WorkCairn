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
