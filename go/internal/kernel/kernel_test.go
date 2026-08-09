package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
	"github.com/AkiraShimizu0/workspace-os/go/internal/execution"
	"github.com/AkiraShimizu0/workspace-os/go/internal/project"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
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
type fakeTaskService struct {
	active          bool
	activateCalls   int
	deactivateCalls int
	createCalls     int
}
type fakeWorkerService struct {
	active          bool
	activateCalls   int
	deactivateCalls int
	executeCalls    int
}
type fakeExecutionService struct {
	active          bool
	activateCalls   int
	deactivateCalls int
	executeCalls    int
}
type fakeEventService struct {
	started      bool
	startCalls   int
	stopCalls    int
	publishCalls int
}
type fakeSchedulerService struct {
	started    bool
	startCalls int
	stopCalls  int
}

var errFakeEventServiceStopped = errors.New("fake event service is stopped")
var errFakeTaskServiceInactive = errors.New("fake task service is inactive")
var errFakeWorkerServiceInactive = errors.New("fake worker service is inactive")
var errFakeExecutionServiceInactive = errors.New("fake execution service is inactive")

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
func (service *fakeSchedulerService) Start() error {
	service.started = true
	service.startCalls++
	return nil
}
func (service *fakeSchedulerService) Stop() error {
	service.started = false
	service.stopCalls++
	return nil
}
func (*fakeOrganizationService) IsOrganizationService() {}
func (service *fakeTaskService) Activate() error {
	service.activateCalls++
	service.active = true
	return nil
}
func (service *fakeTaskService) Deactivate() error {
	service.deactivateCalls++
	service.active = false
	return nil
}
func (service *fakeTaskService) Create(_ context.Context, input task.CreateInput) (task.Task, error) {
	if !service.active {
		return task.Task{}, errFakeTaskServiceInactive
	}
	service.createCalls++
	return task.New(input)
}
func (*fakeTaskService) Start(context.Context, string) (task.Task, error) {
	return task.Task{}, nil
}
func (*fakeTaskService) Complete(context.Context, string) (task.Task, error) {
	return task.Task{}, nil
}
func (*fakeTaskService) Fail(context.Context, string, string) (task.Task, error) {
	return task.Task{}, nil
}
func (*fakeTaskService) Hold(context.Context, string, string) (task.Task, error) {
	return task.Task{}, nil
}
func (*fakeTaskService) Resume(context.Context, string) (task.Task, error) {
	return task.Task{}, nil
}
func (*fakeTaskService) Get(context.Context, string) (task.Task, error) {
	return task.Task{}, nil
}
func (service *fakeWorkerService) Activate() error {
	service.activateCalls++
	service.active = true
	return nil
}
func (service *fakeWorkerService) Deactivate() error {
	service.deactivateCalls++
	service.active = false
	return nil
}
func (service *fakeWorkerService) Execute(_ context.Context, request worker.ExecutionRequest) (worker.ExecutionResult, error) {
	if !service.active {
		return worker.ExecutionResult{}, errFakeWorkerServiceInactive
	}
	service.executeCalls++
	return worker.ExecutionResult{
		Content: "result", EmployeeID: request.Employee.EmployeeID, TaskID: request.Task.TaskID,
		Runner: "FakeRunner", Model: request.Employee.Model, Status: worker.StatusCompleted,
	}, nil
}
func (service *fakeExecutionService) Activate() error {
	service.activateCalls++
	service.active = true
	return nil
}
func (service *fakeExecutionService) Deactivate() error {
	service.deactivateCalls++
	service.active = false
	return nil
}
func (service *fakeExecutionService) Execute(_ context.Context, request execution.Request) (execution.Result, error) {
	if !service.active {
		return execution.Result{}, errFakeExecutionServiceInactive
	}
	service.executeCalls++
	return execution.Result{ProjectID: request.ProjectID, TaskID: request.TaskID}, nil
}
func (service *fakeEventService) Start() error {
	service.startCalls++
	service.started = true
	return nil
}
func (service *fakeEventService) Stop() error {
	service.stopCalls++
	service.started = false
	return nil
}
func (service *fakeEventService) Publish(context.Context, event.Event) error {
	if !service.started {
		return errFakeEventServiceStopped
	}
	service.publishCalls++
	return nil
}
func (*fakeEventService) Subscribe(event.Type, event.Handler) (event.Subscription, error) {
	return event.Subscription{}, nil
}
func (*fakeEventService) Unsubscribe(event.Subscription) error { return nil }

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
	events := &fakeEventService{}
	organization := &fakeOrganizationService{}
	task := &fakeTaskService{}
	workerService := &fakeWorkerService{}
	executionService := &fakeExecutionService{}
	schedulerService := &fakeSchedulerService{}

	registrations := []struct {
		name string
		err  error
	}{
		{"project", kernel.RegisterProjectService(project)},
		{"workflow", kernel.RegisterWorkflowService(workflow)},
		{"event", kernel.RegisterEventService(events)},
		{"organization", kernel.RegisterOrganizationService(organization)},
		{"task", kernel.RegisterTaskService(task)},
		{"worker", kernel.RegisterWorkerService(workerService)},
		{"execution", kernel.RegisterExecutionService(executionService)},
		{"scheduler", kernel.RegisterSchedulerService(schedulerService)},
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
	gotEvents, err := kernel.EventService()
	if err != nil || gotEvents != events {
		t.Fatalf("EventService() = %#v, %v", gotEvents, err)
	}
	gotOrganization, err := kernel.OrganizationService()
	if err != nil || gotOrganization != organization {
		t.Fatalf("OrganizationService() = %#v, %v", gotOrganization, err)
	}
	gotTask, err := kernel.TaskService()
	if err != nil || gotTask != task {
		t.Fatalf("TaskService() = %#v, %v", gotTask, err)
	}
	gotWorker, err := kernel.WorkerService()
	if err != nil || gotWorker != workerService {
		t.Fatalf("WorkerService() = %#v, %v", gotWorker, err)
	}
	gotExecution, err := kernel.ExecutionService()
	if err != nil || gotExecution != executionService {
		t.Fatalf("ExecutionService() = %#v, %v", gotExecution, err)
	}
	gotScheduler, err := kernel.SchedulerService()
	if err != nil || gotScheduler != schedulerService {
		t.Fatalf("SchedulerService() = %#v, %v", gotScheduler, err)
	}

	wantServices := []ServiceKind{
		ServiceEvent,
		ServiceExecution,
		ServiceOrganization,
		ServiceProject,
		ServiceScheduler,
		ServiceTask,
		ServiceWorker,
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
	if _, err := kernel.EventService(); !errors.Is(err, ErrServiceNotRegistered) {
		t.Fatalf("EventService() error = %v, want ErrServiceNotRegistered", err)
	}
	if _, err := kernel.WorkerService(); !errors.Is(err, ErrServiceNotRegistered) {
		t.Fatalf("WorkerService() error = %v, want ErrServiceNotRegistered", err)
	}
	if _, err := kernel.ExecutionService(); !errors.Is(err, ErrServiceNotRegistered) {
		t.Fatalf("ExecutionService() error = %v, want ErrServiceNotRegistered", err)
	}
	if _, err := kernel.SchedulerService(); !errors.Is(err, ErrServiceNotRegistered) {
		t.Fatalf("SchedulerService() error = %v, want ErrServiceNotRegistered", err)
	}
}

func TestKernelControlsSchedulerLifecycle(t *testing.T) {
	workspaceKernel, _ := New("v0.3.0")
	schedulerService := &fakeSchedulerService{}
	if err := workspaceKernel.RegisterSchedulerService(schedulerService); err != nil {
		t.Fatal(err)
	}
	if err := workspaceKernel.Start(); err != nil || !schedulerService.started {
		t.Fatalf("Start() = %v scheduler=%#v", err, schedulerService)
	}
	if err := workspaceKernel.Stop(); err != nil || schedulerService.started || schedulerService.startCalls != 1 || schedulerService.stopCalls != 1 {
		t.Fatalf("Stop() = %v scheduler=%#v", err, schedulerService)
	}
}

func TestKernelControlsEventServiceLifecycle(t *testing.T) {
	kernel, _ := New("v0.3.0")
	events := &fakeEventService{}
	if err := kernel.RegisterEventService(events); err != nil {
		t.Fatal(err)
	}
	published := event.Event{}
	if err := events.Publish(context.Background(), published); !errors.Is(err, errFakeEventServiceStopped) {
		t.Fatalf("Publish() before Kernel.Start error = %v", err)
	}
	if err := kernel.Start(); err != nil {
		t.Fatal(err)
	}
	if err := events.Publish(context.Background(), published); err != nil {
		t.Fatalf("Publish() while Kernel started error = %v", err)
	}
	if err := kernel.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := events.Publish(context.Background(), published); !errors.Is(err, errFakeEventServiceStopped) {
		t.Fatalf("Publish() after Kernel.Stop error = %v", err)
	}
	if events.startCalls != 1 || events.stopCalls != 1 || events.publishCalls != 1 {
		t.Fatalf("event lifecycle calls = start:%d stop:%d publish:%d", events.startCalls, events.stopCalls, events.publishCalls)
	}
}

func TestKernelControlsTaskServiceLifecycle(t *testing.T) {
	kernel, _ := New("v0.3.0")
	tasks := &fakeTaskService{}
	if err := kernel.RegisterTaskService(tasks); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	input := task.CreateInput{ID: "TASK-001", Title: "test"}
	if _, err := tasks.Create(ctx, input); !errors.Is(err, errFakeTaskServiceInactive) {
		t.Fatalf("Create() before Kernel.Start error = %v", err)
	}
	if err := kernel.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Create(ctx, input); err != nil {
		t.Fatalf("Create() while Kernel started error = %v", err)
	}
	if err := kernel.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Create(ctx, input); !errors.Is(err, errFakeTaskServiceInactive) {
		t.Fatalf("Create() after Kernel.Stop error = %v", err)
	}
	if tasks.activateCalls != 1 || tasks.deactivateCalls != 1 || tasks.createCalls != 1 {
		t.Fatalf("task lifecycle calls = activate:%d deactivate:%d create:%d", tasks.activateCalls, tasks.deactivateCalls, tasks.createCalls)
	}
}

func TestKernelControlsWorkerServiceLifecycle(t *testing.T) {
	kernel, _ := New("v0.3.0")
	workers := &fakeWorkerService{}
	if err := kernel.RegisterWorkerService(workers); err != nil {
		t.Fatal(err)
	}
	request := worker.ExecutionRequest{}
	if _, err := workers.Execute(context.Background(), request); !errors.Is(err, errFakeWorkerServiceInactive) {
		t.Fatalf("Execute() before Kernel.Start error = %v", err)
	}
	if err := kernel.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := workers.Execute(context.Background(), request); err != nil {
		t.Fatalf("Execute() while Kernel started error = %v", err)
	}
	if err := kernel.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := workers.Execute(context.Background(), request); !errors.Is(err, errFakeWorkerServiceInactive) {
		t.Fatalf("Execute() after Kernel.Stop error = %v", err)
	}
	if workers.activateCalls != 1 || workers.deactivateCalls != 1 || workers.executeCalls != 1 {
		t.Fatalf("worker lifecycle calls = activate:%d deactivate:%d execute:%d", workers.activateCalls, workers.deactivateCalls, workers.executeCalls)
	}
}

func TestKernelControlsExecutionServiceLifecycle(t *testing.T) {
	kernel, _ := New("v0.3.0")
	executions := &fakeExecutionService{}
	if err := kernel.RegisterExecutionService(executions); err != nil {
		t.Fatal(err)
	}
	request := execution.Request{ProjectID: "PROJECT-001", TaskID: "TASK-001"}
	if _, err := executions.Execute(context.Background(), request); !errors.Is(err, errFakeExecutionServiceInactive) {
		t.Fatalf("Execute() before Kernel.Start error = %v", err)
	}
	if err := kernel.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := executions.Execute(context.Background(), request); err != nil {
		t.Fatalf("Execute() while Kernel started error = %v", err)
	}
	if err := kernel.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := executions.Execute(context.Background(), request); !errors.Is(err, errFakeExecutionServiceInactive) {
		t.Fatalf("Execute() after Kernel.Stop error = %v", err)
	}
	if executions.activateCalls != 1 || executions.deactivateCalls != 1 || executions.executeCalls != 1 {
		t.Fatalf("execution lifecycle calls = activate:%d deactivate:%d execute:%d", executions.activateCalls, executions.deactivateCalls, executions.executeCalls)
	}
}

func TestServiceRegistrationRequiresStoppedKernel(t *testing.T) {
	kernel, _ := New("v0.3.0")
	if err := kernel.Start(); err != nil {
		t.Fatal(err)
	}
	if err := kernel.RegisterEventService(&fakeEventService{}); !errors.Is(err, ErrServiceRegistrationClosed) {
		t.Fatalf("RegisterEventService() error = %v", err)
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
