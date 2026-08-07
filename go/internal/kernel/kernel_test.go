package kernel

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/AkiraShimizu0/workspace-os/go/internal/project"
	"github.com/AkiraShimizu0/workspace-os/go/internal/workflow"
)

type fakeProjectService struct {
	nextTaskID string
	err        error
	nextCalls  int
}
type fakeWorkflowService struct {
	result workflow.ReadinessResult
	err    error
}
type fakeOrganizationService struct{}
type fakeTaskService struct{}

func (service *fakeProjectService) NextTaskID([]string) (string, error) {
	service.nextCalls++
	return service.nextTaskID, service.err
}
func (service *fakeProjectService) ValidateTask(project.Task) error {
	return service.err
}
func (service *fakeProjectService) CanTransition(project.Status, project.Status) error {
	return service.err
}
func (service *fakeWorkflowService) Readiness(
	[]workflow.Task,
	[]workflow.Dependency,
	map[string]bool,
) (workflow.ReadinessResult, error) {
	return service.result, service.err
}
func (*fakeOrganizationService) IsOrganizationService() {}
func (*fakeTaskService) IsTaskService()                 {}

func TestNewKernel(t *testing.T) {
	kernel, err := New("v0.3.0")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	status := kernel.Status()
	if status.State != StateStopped || status.Version != "v0.3.0" {
		t.Fatalf("Status() = %#v", status)
	}
	if len(status.RegisteredServices) != 0 {
		t.Fatalf("RegisteredServices = %#v", status.RegisteredServices)
	}
}

func TestNewKernelRejectsEmptyVersion(t *testing.T) {
	if _, err := New(" "); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("New() error = %v, want ErrInvalidVersion", err)
	}
}

func TestKernelLifecycle(t *testing.T) {
	kernel, _ := New("v0.3.0")
	if err := kernel.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if kernel.Status().State != StateStarted {
		t.Fatalf("state = %s, want started", kernel.Status().State)
	}
	if err := kernel.Start(); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second Start() error = %v, want ErrAlreadyStarted", err)
	}
	if err := kernel.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if kernel.Status().State != StateStopped {
		t.Fatalf("state = %s, want stopped", kernel.Status().State)
	}
	if err := kernel.Stop(); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("second Stop() error = %v, want ErrNotStarted", err)
	}
}

func TestKernelRejectsStopBeforeStart(t *testing.T) {
	kernel, _ := New("v0.3.0")
	if err := kernel.Stop(); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("Stop() error = %v, want ErrNotStarted", err)
	}
}

func TestServiceRegistrationAndLookup(t *testing.T) {
	kernel, _ := New("v0.3.0")
	project := &fakeProjectService{}
	workflow := &fakeWorkflowService{}
	organization := &fakeOrganizationService{}
	task := &fakeTaskService{}

	registrations := []struct {
		name string
		err  error
	}{
		{"project", kernel.RegisterProjectService(project)},
		{"workflow", kernel.RegisterWorkflowService(workflow)},
		{"organization", kernel.RegisterOrganizationService(organization)},
		{"task", kernel.RegisterTaskService(task)},
	}
	for _, registration := range registrations {
		if registration.err != nil {
			t.Fatalf("register %s error = %v", registration.name, registration.err)
		}
	}

	gotProject, err := kernel.ProjectService()
	if err != nil || gotProject != project {
		t.Fatalf("ProjectService() = %#v, %v", gotProject, err)
	}
	gotWorkflow, err := kernel.WorkflowService()
	if err != nil || gotWorkflow != workflow {
		t.Fatalf("WorkflowService() = %#v, %v", gotWorkflow, err)
	}
	gotOrganization, err := kernel.OrganizationService()
	if err != nil || gotOrganization != organization {
		t.Fatalf("OrganizationService() = %#v, %v", gotOrganization, err)
	}
	gotTask, err := kernel.TaskService()
	if err != nil || gotTask != task {
		t.Fatalf("TaskService() = %#v, %v", gotTask, err)
	}

	wantServices := []ServiceKind{
		ServiceOrganization,
		ServiceProject,
		ServiceTask,
		ServiceWorkflow,
	}
	if got := kernel.Status().RegisteredServices; !reflect.DeepEqual(got, wantServices) {
		t.Fatalf("RegisteredServices = %#v, want %#v", got, wantServices)
	}
}

func TestServiceRegistrationRejectsInvalidAndDuplicateServices(t *testing.T) {
	kernel, _ := New("v0.3.0")
	var nilProject *fakeProjectService
	if err := kernel.RegisterProjectService(nilProject); !errors.Is(err, ErrInvalidService) {
		t.Fatalf("nil registration error = %v, want ErrInvalidService", err)
	}
	if err := kernel.RegisterProjectService(&fakeProjectService{}); err != nil {
		t.Fatalf("registration error = %v", err)
	}
	if err := kernel.RegisterProjectService(&fakeProjectService{}); !errors.Is(err, ErrServiceAlreadyRegistered) {
		t.Fatalf("duplicate registration error = %v", err)
	}
}

func TestUnregisteredServiceIsRejected(t *testing.T) {
	kernel, _ := New("v0.3.0")
	if _, err := kernel.WorkflowService(); !errors.Is(err, ErrServiceNotRegistered) {
		t.Fatalf("WorkflowService() error = %v, want ErrServiceNotRegistered", err)
	}
}

func TestKernelStatusCommand(t *testing.T) {
	kernel, _ := New("v0.3.0")
	result, err := kernel.HandleCommand(Command{Type: CommandKernelStatus})
	if err != nil {
		t.Fatalf("HandleCommand() error = %v", err)
	}
	status, ok := result.Data.(KernelStatus)
	if !ok {
		t.Fatalf("command data type = %T, want KernelStatus", result.Data)
	}
	if result.Type != CommandKernelStatus || status.State != StateStopped {
		t.Fatalf("command result = %#v", result)
	}
}

func TestDomainCommandRequiresStartedKernel(t *testing.T) {
	kernel, _ := New("v0.3.0")
	payload, _ := json.Marshal(map[string]any{"existing_ids": []string{}})
	_, err := kernel.HandleCommand(Command{
		Type:    CommandProjectNextTaskID,
		Payload: payload,
	})
	assertCommandErrorKind(t, err, ErrorKernelNotStarted)
}

func TestDomainCommandUsesInjectedService(t *testing.T) {
	kernel, _ := New("v0.3.0")
	projectService := &fakeProjectService{nextTaskID: "TASK-042"}
	if err := kernel.RegisterProjectService(projectService); err != nil {
		t.Fatal(err)
	}
	if err := kernel.Start(); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"existing_ids": []string{"TASK-001"}})
	result, err := kernel.HandleCommand(Command{
		Type:    CommandProjectNextTaskID,
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("HandleCommand() error = %v", err)
	}
	if !reflect.DeepEqual(result.Data, map[string]string{"task_id": "TASK-042"}) {
		t.Fatalf("HandleCommand() data = %#v", result.Data)
	}
	if projectService.nextCalls != 1 {
		t.Fatalf("NextTaskID() calls = %d", projectService.nextCalls)
	}
}

func TestDomainCommandRejectsUnregisteredService(t *testing.T) {
	kernel, _ := New("v0.3.0")
	if err := kernel.Start(); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"existing_ids": []string{}})
	_, err := kernel.HandleCommand(Command{
		Type:    CommandProjectNextTaskID,
		Payload: payload,
	})
	assertCommandErrorKind(t, err, ErrorServiceNotRegistered)
}

func TestDomainErrorIsClassifiedAndPreserved(t *testing.T) {
	kernel, _ := New("v0.3.0")
	domainError := project.ErrInvalidTaskID
	if err := kernel.RegisterProjectService(&fakeProjectService{err: domainError}); err != nil {
		t.Fatal(err)
	}
	if err := kernel.Start(); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"existing_ids": []string{"BAD-001"}})
	_, err := kernel.HandleCommand(Command{
		Type:    CommandProjectNextTaskID,
		Payload: payload,
	})
	assertCommandErrorKind(t, err, ErrorInvalidTaskID)
	if !errors.Is(err, domainError) {
		t.Fatalf("HandleCommand() error = %v, want wrapped domain error", err)
	}
}

func TestUnknownCommandIsRejected(t *testing.T) {
	kernel, _ := New("v0.3.0")
	_, err := kernel.HandleCommand(Command{Type: "project.create"})
	if !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("HandleCommand() error = %v, want ErrUnknownCommand", err)
	}
	assertCommandErrorKind(t, err, ErrorUnknownOperation)
}

func assertCommandErrorKind(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	var commandError *CommandError
	if !errors.As(err, &commandError) || commandError.Kind != want {
		t.Fatalf("command error = %v, want kind %s", err, want)
	}
}
