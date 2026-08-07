package project

import "testing"

func TestTaskValidationSharedFixture(t *testing.T) {
	fixture := loadProjectFixture(t)
	for _, testCase := range fixture.TaskValidationCases {
		t.Run(testCase.Name, func(t *testing.T) {
			err := ValidateTask(testCase.Task)
			if (err == nil) != testCase.Valid {
				t.Fatalf("ValidateTask() error = %v, valid = %v", err, testCase.Valid)
			}
		})
	}
}
