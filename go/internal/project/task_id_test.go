package project

import (
	"errors"
	"testing"
)

func TestNextTaskIDSharedFixture(t *testing.T) {
	fixture := loadProjectFixture(t)
	for _, testCase := range fixture.TaskIDCases {
		t.Run(testCase.Name, func(t *testing.T) {
			result, err := NextTaskID(testCase.ExistingIDs)
			switch testCase.ErrorKind {
			case "":
				if err != nil {
					t.Fatalf("NextTaskID() error = %v", err)
				}
				if result != testCase.ExpectedID {
					t.Fatalf("NextTaskID() = %s, want %s", result, testCase.ExpectedID)
				}
			case "invalid_task_id":
				if !errors.Is(err, ErrInvalidTaskID) {
					t.Fatalf("error = %v, want ErrInvalidTaskID", err)
				}
			case "duplicate_task_id":
				if !errors.Is(err, ErrDuplicateTaskID) {
					t.Fatalf("error = %v, want ErrDuplicateTaskID", err)
				}
			default:
				t.Fatalf("unknown fixture error kind: %s", testCase.ErrorKind)
			}
		})
	}
}
