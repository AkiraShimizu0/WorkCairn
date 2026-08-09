package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
	workspaceprocess "github.com/AkiraShimizu0/workspace-os/go/internal/process"
	"github.com/AkiraShimizu0/workspace-os/go/internal/scheduler"
	"github.com/AkiraShimizu0/workspace-os/go/internal/service"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

type fakeCommandBackend struct {
	calls   int
	result  any
	err     error
	record  commandledger.Record
	started chan struct{}
	release chan struct{}
}

func (fake *fakeCommandBackend) Execute(ctx context.Context, _ Command) (any, error) {
	fake.calls++
	if fake.started != nil {
		close(fake.started)
		select {
		case <-fake.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return fake.result, fake.err
}

func (fake *fakeCommandBackend) Inspect(context.Context, string, string, string) (commandledger.Record, error) {
	return fake.record, fake.err
}

func TestHandlerRequiresVersionedApprovedCommandIDBeforeExecution(t *testing.T) {
	backend := &fakeCommandBackend{result: map[string]string{"status": "ok"}}
	handler, err := NewHandler(backend, backend)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"version":"workspace-command.v1","operation":"task.execute","approved":true,"payload":{}}`,
		`{"version":"workspace-command.v1","command_id":"CMD-001","operation":"task.execute","approved":false,"payload":{}}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/commands", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest && response.Code != http.StatusForbidden {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
	}
	if backend.calls != 0 {
		t.Fatalf("unapproved or invalid commands reached executor: %d", backend.calls)
	}
}

func TestHandlerMapsRunningCommandToRecoveryBoundary(t *testing.T) {
	backend := &fakeCommandBackend{err: commandledger.ErrInProgress}
	handler, _ := NewHandler(backend, backend)
	request := httptest.NewRequest(http.MethodPost, "/v1/commands", bytes.NewBufferString(`{"version":"workspace-command.v1","command_id":"CMD-001","operation":"task.execute","approved":true,"payload":{}}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var decoded Response
	_ = json.Unmarshal(response.Body.Bytes(), &decoded)
	if response.Code != http.StatusConflict || decoded.Error == nil || decoded.Error.Code != "COMMAND_IN_PROGRESS" || !decoded.Error.RecoveryRequired {
		t.Fatalf("response = %d %#v", response.Code, decoded)
	}
}

func TestProcessExecutorHTTPProjectCommandReplayConflictAndInspect(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	executor, err := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(executor, executor)
	command := map[string]any{
		"version": ContractVersion, "command_id": "CMD-HTTP-PROJECT-001", "operation": "project.bootstrap", "approved": true,
		"payload": map[string]any{"project_id": "PROJECT-001", "project_name": "HTTP案件", "description": "HTTP経由", "current_time": "2026-08-08T12:00:00+09:00"},
	}
	first := performCommand(t, handler, command)
	if first.Code != http.StatusOK {
		t.Fatalf("first response = %d %s", first.Code, first.Body.String())
	}
	beforeReplay := snapshotHTTPVault(t, root)
	second := performCommand(t, handler, command)
	if second.Code != http.StatusOK || second.Body.String() != first.Body.String() || !reflect.DeepEqual(beforeReplay, snapshotHTTPVault(t, root)) {
		t.Fatalf("replay response = %d %s", second.Code, second.Body.String())
	}
	command["payload"].(map[string]any)["description"] = "different"
	conflict := performCommand(t, handler, command)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict response = %d %s", conflict.Code, conflict.Body.String())
	}
	statusRequest := httptest.NewRequest(http.MethodGet, "/v1/commands/CMD-HTTP-PROJECT-001?scope=workspace", nil)
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK || !bytes.Contains(statusResponse.Body.Bytes(), []byte(`"state":"succeeded"`)) {
		t.Fatalf("status response = %d %s", statusResponse.Code, statusResponse.Body.String())
	}
}

func TestProcessExecutorRecognizesReviewedWorkflowV1Command(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト", "P"), 0o755); err != nil {
		t.Fatal(err)
	}
	executor, err := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"project_id": "PROJECT-001", "project_name": "P", "reviewer_id": "QA-001",
		"current_time": "2026-08-09T10:00:00+09:00", "max_tasks": 10,
	})
	_, err = executor.Execute(context.Background(), Command{
		Version: ContractVersion, CommandID: "CMD-HTTP-REVIEWED-WORKFLOW-001",
		Operation: "workflow.reviewed.execute", Approved: true, Payload: payload,
	})
	if err == nil || errors.Is(err, ErrUnsupportedCommand) {
		t.Fatalf("reviewed Workflow dispatch error = %v", err)
	}
	record, inspectErr := executor.Inspect(context.Background(), "project", "P", "CMD-HTTP-REVIEWED-WORKFLOW-001")
	if inspectErr != nil || record.Operation != "workflow.reviewed.execute" || record.State != commandledger.StateFailed {
		t.Fatalf("reviewed Workflow command record = %#v, %v", record, inspectErr)
	}
}

func TestScheduledHTTPCommandDispatchesThroughExistingWriterOnce(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	executor, err := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(executor, executor)
	bootstrap := map[string]any{
		"version": ContractVersion, "command_id": "CMD-SCHEDULE-PROJECT-001", "operation": "project.bootstrap", "approved": true,
		"payload": map[string]any{"project_id": "PROJECT-001", "project_name": "自動化案件", "description": "schedule test", "current_time": "2026-08-09T12:00:00+09:00"},
	}
	if response := performCommand(t, handler, bootstrap); response.Code != http.StatusOK {
		t.Fatalf("bootstrap = %d %s", response.Code, response.Body.String())
	}
	schedule := map[string]any{
		"version": ContractVersion, "command_id": "CMD-CREATE-SCHEDULE-001", "operation": "schedule.create", "approved": true,
		"payload": map[string]any{
			"schedule_id": "SCHEDULE-001", "due_at": "2026-08-09T13:00:00+09:00", "current_time": "2026-08-09T12:30:00+09:00", "approval_reference": "approval-schedule-001",
			"target": map[string]any{
				"version": ContractVersion, "command_id": "CMD-SCHEDULED-TASK-CREATE-001", "operation": "task.create",
				"payload": map[string]any{"project_name": "自動化案件", "title": "予定されたTask", "assignee_id": nil, "current_time": "2026-08-09T13:00:00+09:00"},
			},
		},
	}
	if response := performCommand(t, handler, schedule); response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"state":"pending"`)) {
		t.Fatalf("schedule create = %d %s", response.Code, response.Body.String())
	}
	for _, path := range []string{"/v1/schedules", "/v1/schedules/SCHEDULE-001"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"schedule_id":"SCHEDULE-001"`)) {
			t.Fatalf("schedule inspect %s = %d %s", path, response.Code, response.Body.String())
		}
	}
	store, _ := vault.NewScheduleStore(root)
	schedulerService, _ := service.NewSchedulerService(store, executor, service.SchedulerConfig{PollInterval: time.Hour, Now: time.Now})
	due := time.Date(2026, time.August, 9, 13, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	run, err := schedulerService.RunDue(context.Background(), due)
	if err != nil || len(run.Records) != 1 || run.Records[0].State != scheduler.StateSucceeded {
		t.Fatalf("RunDue() = %#v, %v", run, err)
	}
	if _, err := schedulerService.RunDue(context.Background(), due.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	taskStore, _ := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "自動化案件"})
	created, err := taskStore.Get(context.Background(), "TASK-001")
	if err != nil || created.Title != "予定されたTask" || created.Status != task.StatusUnstarted {
		t.Fatalf("scheduled Task = %#v, %v", created, err)
	}
	commandRecord, err := executor.Inspect(context.Background(), "project", "自動化案件", "CMD-SCHEDULED-TASK-CREATE-001")
	if err != nil || commandRecord.State != commandledger.StateSucceeded {
		t.Fatalf("scheduled Command record = %#v, %v", commandRecord, err)
	}
}

func TestServerGracefulShutdownWaitsForRunningCommand(t *testing.T) {
	backend := &fakeCommandBackend{result: map[string]string{"status": "ok"}, started: make(chan struct{}), release: make(chan struct{})}
	handler, _ := NewHandler(backend, backend)
	server, _ := NewServer("127.0.0.1:0", handler)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	body := `{"version":"workspace-command.v1","command_id":"CMD-LONG-001","operation":"task.execute","approved":true,"payload":{}}`
	requestDone := make(chan error, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/v1/commands", bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		requestDone <- err
	}()
	<-backend.started
	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		shutdownDone <- server.Shutdown(ctx)
	}()
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned before command completion: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(backend.release)
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve() error = %v", err)
	}
}

func performCommand(t *testing.T, handler http.Handler, command map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	content, _ := json.Marshal(command)
	request := httptest.NewRequest(http.MethodPost, "/v1/commands", bytes.NewReader(content))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func snapshotHTTPVault(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			content, _ := os.ReadFile(path)
			relative, _ := filepath.Rel(root, path)
			result[relative] = string(content)
		}
		return err
	})
	return result
}
