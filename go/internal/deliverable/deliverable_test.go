package deliverable

import (
	"errors"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/worker"
)

func TestDocumentValidation(t *testing.T) {
	document := validDocument()
	if err := document.Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*Document)
	}{
		{"Project ID", func(value *Document) { value.ProjectID = "" }},
		{"Project name", func(value *Document) { value.ProjectName = "bad\nname" }},
		{"Task title", func(value *Document) { value.TaskTitle = "" }},
		{"execution time", func(value *Document) { value.ExecutedAt = time.Time{} }},
		{"Task ID", func(value *Document) { value.Execution.TaskID = "bad" }},
		{"Worker result", func(value *Document) { value.Execution.Content = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validDocument()
			test.change(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("Validate() accepted invalid document")
			}
		})
	}
}

func TestRecordValidation(t *testing.T) {
	valid := Record{TaskID: "TASK-001", RelativePath: "Deliverables/TASK-001.md"}
	if err := valid.Validate("TASK-001"); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []Record{
		{TaskID: "TASK-002", RelativePath: valid.RelativePath},
		{TaskID: valid.TaskID, RelativePath: ""},
		{TaskID: valid.TaskID, RelativePath: "../TASK-001.md"},
		{TaskID: valid.TaskID, RelativePath: `..\TASK-001.md`},
		{TaskID: valid.TaskID, RelativePath: "Deliverables/TASK-001.md\nforged"},
	} {
		if err := candidate.Validate("TASK-001"); !errors.Is(err, ErrInvalidDocument) {
			t.Fatalf("Validate(%#v) error = %v", candidate, err)
		}
	}
}

func validDocument() Document {
	return Document{
		ProjectID:   "PROJECT-001",
		ProjectName: "ToDoアプリ",
		TaskTitle:   "要件を整理する",
		ExecutedAt:  time.Date(2026, time.August, 6, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60)),
		Execution: worker.ExecutionResult{
			Content:    "# 完成した仕様書\n\n本文",
			EmployeeID: "PLAN-001",
			TaskID:     "TASK-001",
			Runner:     "ClaudeRunner",
			Model:      "Claude Sonnet 5",
			Duration:   time.Second,
			Status:     worker.StatusCompleted,
		},
	}
}
