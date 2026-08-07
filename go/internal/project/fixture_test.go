package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type projectFixture struct {
	TaskIDCases         []taskIDCase         `json:"task_id_cases"`
	StatusCases         []statusCase         `json:"status_cases"`
	TransitionCases     []transitionCase     `json:"transition_cases"`
	TaskValidationCases []taskValidationCase `json:"task_validation_cases"`
}

type taskIDCase struct {
	Name        string   `json:"name"`
	ExistingIDs []string `json:"existing_ids"`
	ExpectedID  string   `json:"expected_id"`
	ErrorKind   string   `json:"error_kind"`
}

type statusCase struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Valid  bool   `json:"valid"`
}

type transitionCase struct {
	Name  string `json:"name"`
	From  Status `json:"from"`
	To    Status `json:"to"`
	Valid bool   `json:"valid"`
}

type taskValidationCase struct {
	Name  string `json:"name"`
	Task  Task   `json:"task"`
	Valid bool   `json:"valid"`
}

func loadProjectFixture(t *testing.T) projectFixture {
	t.Helper()
	path := filepath.Join(
		"..",
		"..",
		"..",
		"fixtures",
		"project",
		"task_domain_cases.json",
	)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read project fixture: %v", err)
	}
	var fixture projectFixture
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatalf("decode project fixture: %v", err)
	}
	return fixture
}
