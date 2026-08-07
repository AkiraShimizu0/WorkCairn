package execution

import (
	"errors"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
	"github.com/AkiraShimizu0/workspace-os/go/internal/workflow"
)

func validRequest() Request {
	assigneeID := "PLAN-001"
	return Request{
		ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", TaskID: "TASK-001",
		Employee: worker.EmployeeContext{
			EmployeeID: assigneeID, Name: "山本 真帆", Department: "企画部",
			Role: "Product Manager", Model: "Fake Model",
		},
		Tasks:             []workflow.Task{{ID: "TASK-001", Title: "要件整理", AssigneeID: &assigneeID, Status: workflow.StatusUnstarted}},
		ExistingEmployees: map[string]bool{assigneeID: true},
		CurrentTime:       time.Now(),
	}
}

func TestRequestValidation(t *testing.T) {
	if err := validRequest().Validate(); err != nil {
		t.Fatal(err)
	}
	changes := []func(*Request){
		func(request *Request) { request.ProjectID = "" },
		func(request *Request) { request.ProjectName = "" },
		func(request *Request) { request.TaskID = "BAD-001" },
		func(request *Request) { request.Employee.EmployeeID = "" },
		func(request *Request) { request.CurrentTime = time.Time{} },
		func(request *Request) { request.Tasks = nil },
		func(request *Request) { request.TaskID = "TASK-002" },
	}
	for index, change := range changes {
		request := validRequest()
		change(&request)
		if err := request.Validate(); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("case %d Validate() error = %v", index, err)
		}
	}
}
