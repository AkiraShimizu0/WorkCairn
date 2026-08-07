package worker

import (
	"errors"
	"testing"
	"time"
)

func validRequest() ExecutionRequest {
	return ExecutionRequest{
		Employee: EmployeeContext{
			EmployeeID: "PLAN-001",
			Name:       "山本 真帆",
			Department: "企画部",
			Role:       "Product Manager",
			Model:      "Claude Sonnet 5",
		},
		Task: TaskContext{
			TaskID:      "TASK-001",
			Title:       "要件を整理する",
			ProjectName: "ToDoアプリ",
		},
		CurrentTime: time.Date(2026, time.August, 7, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60)),
	}
}

func TestExecutionRequestValidation(t *testing.T) {
	if err := validRequest().Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	tests := []struct {
		name   string
		change func(*ExecutionRequest)
	}{
		{"employee ID", func(request *ExecutionRequest) { request.Employee.EmployeeID = "" }},
		{"employee name", func(request *ExecutionRequest) { request.Employee.Name = "" }},
		{"department", func(request *ExecutionRequest) { request.Employee.Department = "" }},
		{"role", func(request *ExecutionRequest) { request.Employee.Role = "" }},
		{"model", func(request *ExecutionRequest) { request.Employee.Model = "" }},
		{"task ID", func(request *ExecutionRequest) { request.Task.TaskID = "BAD-001" }},
		{"task title", func(request *ExecutionRequest) { request.Task.Title = "" }},
		{"project", func(request *ExecutionRequest) { request.Task.ProjectName = "" }},
		{"datetime", func(request *ExecutionRequest) { request.CurrentTime = time.Time{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validRequest()
			test.change(&request)
			if err := request.Validate(); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestPromptValidation(t *testing.T) {
	if err := (Prompt{System: "system", User: "user"}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []Prompt{{User: "user"}, {System: "system"}} {
		if err := prompt.Validate(); !errors.Is(err, ErrInvalidPrompt) {
			t.Fatalf("Validate() error = %v", err)
		}
	}
}

func TestRunResultValidation(t *testing.T) {
	inputTokens, outputTokens := 10, 20
	valid := RunResult{
		Content: "# result", Runner: "FakeRunner", Model: "Fake Model",
		Usage:    TokenUsage{InputTokens: &inputTokens, OutputTokens: &outputTokens},
		Duration: time.Second,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalidToken := -1
	cases := []RunResult{
		{Runner: "FakeRunner", Model: "Fake Model"},
		{Content: "content", Model: "Fake Model"},
		{Content: "content", Runner: "FakeRunner"},
		{Content: "content", Runner: "FakeRunner", Model: "Fake Model", Duration: -1},
		{Content: "content", Runner: "FakeRunner", Model: "Fake Model", Usage: TokenUsage{InputTokens: &invalidToken}},
	}
	for _, result := range cases {
		if err := result.Validate(); !errors.Is(err, ErrInvalidRunnerResult) {
			t.Fatalf("Validate() error = %v for %#v", err, result)
		}
	}
}

func TestExecutionResultValidation(t *testing.T) {
	result := ExecutionResult{
		Content: "content", EmployeeID: "PLAN-001", TaskID: "TASK-001",
		Runner: "FakeRunner", Model: "Fake Model", Status: StatusCompleted,
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	result.Status = "failed"
	if err := result.Validate(); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("Validate() error = %v", err)
	}
}
