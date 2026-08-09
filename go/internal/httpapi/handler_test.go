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
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/action"
	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
	"github.com/AkiraShimizu0/workspace-os/go/internal/interaction"
	"github.com/AkiraShimizu0/workspace-os/go/internal/metrics"
	"github.com/AkiraShimizu0/workspace-os/go/internal/notification"
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

type fakeInteractionWorkflowBackend struct {
	fakeCommandBackend
	plan workspaceprocess.InteractionWorkflowPlan
}

func (fake *fakeInteractionWorkflowBackend) PlanInteractionWorkflow(context.Context, InteractionWorkflowPlanRequest) (workspaceprocess.InteractionWorkflowPlan, error) {
	return fake.plan, fake.err
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

func TestServerRejectsNonLoopbackExposure(t *testing.T) {
	backend := &fakeCommandBackend{}
	handler, err := NewHandler(backend, backend)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"0.0.0.0:8787", ":8787", "192.0.2.1:8787"} {
		if _, err := NewServer(address, handler); err == nil {
			t.Fatalf("NewServer(%q) accepted a non-loopback address", address)
		}
	}
	for _, address := range []string{"127.0.0.1:0", "localhost:8787", "[::1]:0"} {
		if _, err := NewServer(address, handler); err != nil {
			t.Fatalf("NewServer(%q): %v", address, err)
		}
	}
}

func TestInteractionPlanStartReplayAndReadOnlyInspectionHTTP(t *testing.T) {
	root := t.TempDir()
	executor, err := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(executor, executor)
	planBody := `{"version":"workspace-interaction.v1","session_id":"SESSION-HTTP-001","request":"Webアプリを作りたい","model":"Claude Sonnet 5","current_time":"2026-08-09T12:00:00Z"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/interaction-plans", bytes.NewBufferString(planBody))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("plan response = %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".workspace-os")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only Interaction plan changed Vault: %v", err)
	}
	var envelope Response
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	var plan workspaceprocess.InteractionStartPlan
	if err := json.Unmarshal(envelope.Result, &plan); err != nil || plan.Session.RequestDigest == "" {
		t.Fatalf("plan = %#v, %v", plan, err)
	}
	command := map[string]any{
		"version": ContractVersion, "command_id": "CMD-INTERACTION-HTTP-START", "operation": "interaction.start", "approved": true,
		"payload": map[string]any{
			"session_id": plan.Session.SessionID, "request": plan.Session.Request, "request_digest": plan.Session.RequestDigest,
			"model": plan.Session.Model, "current_time": plan.Session.CreatedAt.Format(time.RFC3339),
		},
	}
	first := performCommand(t, handler, command)
	if first.Code != http.StatusOK {
		t.Fatalf("start response = %d %s", first.Code, first.Body.String())
	}
	second := performCommand(t, handler, command)
	if second.Code != http.StatusOK || second.Body.String() != first.Body.String() {
		t.Fatalf("start replay = %d %s", second.Code, second.Body.String())
	}
	for _, path := range []string{"/v1/interactions", "/v1/interactions/SESSION-HTTP-001"} {
		inspection := httptest.NewRecorder()
		handler.ServeHTTP(inspection, httptest.NewRequest(http.MethodGet, path, nil))
		if inspection.Code != http.StatusOK || !strings.Contains(inspection.Body.String(), string(interaction.StatePlanGenerationApprovalRequired)) {
			t.Fatalf("inspection %s = %d %s", path, inspection.Code, inspection.Body.String())
		}
	}
}

func TestInteractionWorkflowPlanHTTPUsesVersionedReadOnlyContract(t *testing.T) {
	backend := &fakeInteractionWorkflowBackend{plan: workspaceprocess.InteractionWorkflowPlan{
		SessionID: "SESSION-HTTP-001", SessionVersion: 3, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ",
		ReviewerID: "QA-001", ReviewerName: "伊藤 健太", ReviewerModel: "Claude Sonnet 5", MaxTasks: 10,
		WorkflowPlanDigest: "sha256:" + strings.Repeat("a", 64), Executable: true, ApprovalRequired: true,
	}}
	handler, err := NewHandler(backend, backend)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"version":"workspace-interaction.v1","session_id":"SESSION-HTTP-001","expected_version":3,"reviewer_id":"QA-001","current_time":"2026-08-09T12:00:00Z","max_tasks":10}`
	request := httptest.NewRequest(http.MethodPost, "/v1/interaction-workflow-plans", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), backend.plan.WorkflowPlanDigest) || backend.calls != 0 {
		t.Fatalf("Interaction Workflow plan response = %d %s calls=%d", response.Code, response.Body.String(), backend.calls)
	}
	invalid := httptest.NewRequest(http.MethodPost, "/v1/interaction-workflow-plans", bytes.NewBufferString(strings.Replace(body, `"max_tasks":10`, `"max_tasks":101`, 1)))
	invalid.Header.Set("Content-Type", "application/json")
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid Interaction Workflow plan = %d %s", invalidResponse.Code, invalidResponse.Body.String())
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

func TestProcessExecutorExposesRedactedNotificationsAndMetricsWithoutReplayDuplication(t *testing.T) {
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
		"version": ContractVersion, "command_id": "CMD-OBS-PROJECT-001", "operation": "project.bootstrap", "approved": true,
		"payload": map[string]any{"project_id": "PROJECT-001", "project_name": "観測案件", "current_time": "2026-08-09T12:00:00+09:00"},
	}
	if response := performSingleCommand(t, handler, bootstrap); response.Code != http.StatusOK {
		t.Fatalf("bootstrap = %d %s", response.Code, response.Body.String())
	}
	create := map[string]any{
		"version": ContractVersion, "command_id": "CMD-OBS-TASK-001", "operation": "task.create", "approved": true,
		"payload": map[string]any{"project_name": "観測案件", "title": "secret task title", "assignee_id": nil, "current_time": "2026-08-09T12:01:00+09:00"},
	}
	first := performSingleCommand(t, handler, create)
	if first.Code != http.StatusOK {
		t.Fatalf("task create = %d %s", first.Code, first.Body.String())
	}

	metricsResponse := httptest.NewRecorder()
	handler.ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/v1/metrics", nil))
	var snapshot metrics.Snapshot
	if err := json.Unmarshal(metricsResponse.Body.Bytes(), &snapshot); err != nil || metricsResponse.Code != http.StatusOK || snapshot.Total != 1 || snapshot.ByEventType[event.TaskCreated] != 1 {
		t.Fatalf("metrics = %d %#v, %v", metricsResponse.Code, snapshot, err)
	}

	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, httptest.NewRequest(http.MethodGet, "/v1/notifications", nil))
	var envelope Response
	if err := json.Unmarshal(listResponse.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	var records []notification.Record
	if err := json.Unmarshal(envelope.Result, &records); err != nil || listResponse.Code != http.StatusOK || len(records) != 1 || records[0].EventType != event.TaskCreated {
		t.Fatalf("notifications = %d %#v, %v", listResponse.Code, records, err)
	}
	if bytes.Contains(listResponse.Body.Bytes(), []byte("secret task title")) {
		t.Fatalf("Notification response leaked Event payload: %s", listResponse.Body.String())
	}
	detailResponse := httptest.NewRecorder()
	handler.ServeHTTP(detailResponse, httptest.NewRequest(http.MethodGet, "/v1/notifications/"+records[0].EventID, nil))
	if detailResponse.Code != http.StatusOK || !bytes.Contains(detailResponse.Body.Bytes(), []byte(records[0].EventID)) {
		t.Fatalf("notification detail = %d %s", detailResponse.Code, detailResponse.Body.String())
	}

	replay := performSingleCommand(t, handler, create)
	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}
	if got := executor.InspectMetrics(); got.Total != 1 {
		t.Fatalf("replay metrics = %#v", got)
	}
	replayedRecords, err := executor.InspectNotifications(context.Background())
	if err != nil || len(replayedRecords) != 1 {
		t.Fatalf("replay notifications = %#v, %v", replayedRecords, err)
	}
}

func TestHTTPWordPressActionUsesTypedCommandLedgerAndObserverPath(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "プロジェクト", "記事案件")
	if err := os.MkdirAll(filepath.Join(project, "Deliverables"), 0o755); err != nil {
		t.Fatal(err)
	}
	deliverableContent := "---\ntype: task-deliverable\nproject: 記事案件\ntask_id: TASK-001\nassignee_id: WRITER-001\nrunner: fake\nexecuted_at: 2026-08-09 10:00:00\n---\n\n# 公開記事\n\n本文\n"
	if err := os.WriteFile(filepath.Join(project, "Deliverables", "TASK-001.md"), []byte(deliverableContent), 0o644); err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		providerCalls++
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":88,"link":"https://example.test/88","status":"publish"}`))
	}))
	defer server.Close()
	executor, err := NewProcessExecutorWithActionConfig(root, workspaceprocess.ClaudeProcessConfig{}, workspaceprocess.WordPressProcessConfig{
		TargetID: "site-main", BaseURL: server.URL, Username: "fake-user", ApplicationPassword: "fake-password",
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(executor, executor)
	command := map[string]any{
		"version": ContractVersion, "command_id": "CMD-ACTION-HTTP-001", "operation": "action.wordpress.publish", "approved": true,
		"payload": map[string]any{"project_id": "PROJECT-001", "project_name": "記事案件", "task_id": "TASK-001", "target_id": "site-main", "source_sha256": action.SourceDigest([]byte(deliverableContent)), "current_time": "2026-08-09T12:00:00Z"},
	}
	first := performSingleCommand(t, handler, command)
	if first.Code != http.StatusOK || !bytes.Contains(first.Body.Bytes(), []byte(`"status":"published"`)) || providerCalls != 1 {
		t.Fatalf("Action command = %d %s calls=%d", first.Code, first.Body.String(), providerCalls)
	}
	replay := performSingleCommand(t, handler, command)
	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() || providerCalls != 1 {
		t.Fatalf("Action replay = %d %s calls=%d", replay.Code, replay.Body.String(), providerCalls)
	}
	if snapshot := executor.InspectMetrics(); snapshot.ByEventType[event.ActionCompleted] != 1 {
		t.Fatalf("Action metrics = %#v", snapshot)
	}
	records, err := executor.InspectNotifications(context.Background())
	if err != nil || len(records) != 1 || records[0].EventType != event.ActionCompleted {
		t.Fatalf("Action notifications = %#v, %v", records, err)
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

func performSingleCommand(t *testing.T, handler http.Handler, command map[string]any) *httptest.ResponseRecorder {
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
