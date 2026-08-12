package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/action"
	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/event"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/metrics"
	"github.com/AkiraShimizu0/workcairn/go/internal/notification"
	workspaceprocess "github.com/AkiraShimizu0/workcairn/go/internal/process"
	"github.com/AkiraShimizu0/workcairn/go/internal/scheduler"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
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

type fakeInteractionActionBackend struct {
	fakeCommandBackend
	plan workspaceprocess.InteractionActionPlan
}

type fakeAsyncBackend struct {
	validateErr error
	started     chan struct{}
	release     chan struct{}
	completed   chan error
	record      commandledger.Record
}

type providerStatusHTTPDoer func(*http.Request) (*http.Response, error)

func (doer providerStatusHTTPDoer) Do(request *http.Request) (*http.Response, error) {
	return doer(request)
}

func (fake *fakeAsyncBackend) ValidateCommand(command Command) error {
	if fake.validateErr != nil {
		return fake.validateErr
	}
	return command.Validate()
}

func (fake *fakeAsyncBackend) Execute(ctx context.Context, _ Command) (any, error) {
	close(fake.started)
	var err error
	select {
	case <-fake.release:
	case <-ctx.Done():
		err = ctx.Err()
	}
	fake.completed <- err
	return nil, err
}

func (fake *fakeAsyncBackend) Inspect(context.Context, string, string, string) (commandledger.Record, error) {
	return fake.record, nil
}

func (fake *fakeInteractionActionBackend) PlanInteractionAction(context.Context, InteractionActionPlanRequest) (workspaceprocess.InteractionActionPlan, error) {
	return fake.plan, fake.err
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
	for _, address := range []string{"0.0.0.0:8787", "203.0.113.1:8787", "workspace.local:8787"} {
		if _, err := NewLocalNetworkServer(address, handler); err == nil {
			t.Fatalf("NewLocalNetworkServer(%q) accepted an unsafe address", address)
		}
	}
	for _, address := range []string{"127.0.0.1:0", "192.168.1.20:8787", "10.0.0.5:8787", "[fd00::1]:8787"} {
		if _, err := NewLocalNetworkServer(address, handler); err != nil {
			t.Fatalf("NewLocalNetworkServer(%q): %v", address, err)
		}
	}
}

func TestEmbeddedMobileUIAndSecurityHeadersAreServedWithoutFrontendBusinessRules(t *testing.T) {
	backend := &fakeCommandBackend{}
	handler, err := NewHandler(backend, backend)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{"/", "text/html", "WorkCairn"},
		{"/assets/styles.css", "text/css", "safe-area-inset-bottom"},
		{"/assets/app.js", "text/javascript", "/v1/interactions"},
		{"/manifest.webmanifest", "application/manifest+json", "WorkCairn"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), test.contentType) ||
			!strings.Contains(response.Body.String(), test.contains) || response.Header().Get("Content-Security-Policy") == "" ||
			response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("asset %s = %d %q headers=%v", test.path, response.Code, response.Body.String(), response.Header())
		}
	}
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	for _, forbidden := range []string{"TaskStarted", "TaskCompleted", "request_changes →", "ANTHROPIC_API_KEY", "innerHTML", "crypto.randomUUID", "Math.random"} {
		if strings.Contains(asset.Body.String(), forbidden) {
			t.Fatalf("mobile UI contains forbidden rule or secret surface %q", forbidden)
		}
	}
	for _, required := range []string{
		`Prefer: "respond-async"`, "monitorAcceptedCommand", "?scope=workspace",
		"Your company is working. No action needed.", "renderCompanyFlow", "const next = state.next",
		"/work-report", "autonomy_contract", "renderProofOfWork", "renderCEOAttention",
		"cryptoAPI.getRandomValues", "BROWSER_SECURE_RANDOM_UNAVAILABLE",
		`ui.requestForm.addEventListener("submit", prepareNewRequest)`, `requestJSON("/v1/interaction-plans"`,
		`showError(error, "依頼内容を確認できませんでした")`,
		`requestJSON("/v1/provider-status")`, "PROVIDER_CONFIGURATION_REQUIRED", "AIサービスへ接続してください",
		"openSettingsDialog", "renderProviderSettings", "秘密情報はiPhoneやbrowser storageへ保存しません",
	} {
		if !strings.Contains(asset.Body.String(), required) {
			t.Fatalf("mobile UI is missing command continuity boundary %q", required)
		}
	}
	index := httptest.NewRecorder()
	handler.ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, required := range []string{"My Actions", "Company View", "AI社員", "RESPONSIBILITY FLOW", "AUTONOMY CONTRACT", "PROOF OF WORK", "CEO ATTENTION", "AI Connections", "Automatic", "接続済みAIサービスから、WorkCairnが実行先を選びます"} {
		if !strings.Contains(index.Body.String(), required) {
			t.Fatalf("mobile UI is missing Living Company Dashboard surface %q", required)
		}
	}
	for _, forbidden := range []string{`name="model"`, `type="password"`, "利用モデル"} {
		if strings.Contains(index.Body.String(), forbidden) {
			t.Fatalf("mobile UI still asks for per-request model selection %q", forbidden)
		}
	}
}

func TestProviderStatusIsRedactedAndDoesNotCallProvider(t *testing.T) {
	root := t.TempDir()
	providerCalls := 0
	client := providerStatusHTTPDoer(func(*http.Request) (*http.Response, error) {
		providerCalls++
		return nil, errors.New("unexpected Provider call")
	})
	configured, err := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{
		APIKey: "secret-that-must-not-appear", ProviderModel: "provider-model-that-must-not-appear",
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(configured, configured)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/provider-status", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"configured":true`) || providerCalls != 0 ||
		strings.Contains(response.Body.String(), "secret-that-must-not-appear") || strings.Contains(response.Body.String(), "provider-model-that-must-not-appear") {
		t.Fatalf("configured Provider status = %d %s calls=%d", response.Code, response.Body.String(), providerCalls)
	}

	unconfigured, _ := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{}, client)
	unconfiguredHandler, _ := NewHandler(unconfigured, unconfigured)
	unconfiguredResponse := httptest.NewRecorder()
	unconfiguredHandler.ServeHTTP(unconfiguredResponse, httptest.NewRequest(http.MethodGet, "/v1/provider-status", nil))
	if unconfiguredResponse.Code != http.StatusOK || !strings.Contains(unconfiguredResponse.Body.String(), `"configured":false`) ||
		!strings.Contains(unconfiguredResponse.Body.String(), `"credential"`) || !strings.Contains(unconfiguredResponse.Body.String(), `"provider_model"`) || providerCalls != 0 {
		t.Fatalf("unconfigured Provider status = %d %s calls=%d", unconfiguredResponse.Code, unconfiguredResponse.Body.String(), providerCalls)
	}
}

func TestAsyncInteractionCommandSurvivesRequestCancellationAndUsesLedgerLocation(t *testing.T) {
	backend := &fakeAsyncBackend{started: make(chan struct{}), release: make(chan struct{}), completed: make(chan error, 1)}
	handler, err := NewHandler(backend, backend)
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancelRequest := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/commands", bytes.NewBufferString(
		`{"version":"workspace-command.v1","command_id":"CMD-ASYNC-001","operation":"interaction.answer","approved":true,"payload":{}}`,
	)).WithContext(requestContext)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Prefer", "respond-async")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Header().Get("Preference-Applied") != "respond-async" ||
		response.Header().Get("Location") != "/v1/commands/CMD-ASYNC-001?scope=workspace" {
		t.Fatalf("async response = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	<-backend.started
	cancelRequest()
	select {
	case err := <-backend.completed:
		t.Fatalf("request cancellation reached accepted command: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(backend.release)
	if err := <-backend.completed; err != nil {
		t.Fatalf("accepted command = %v", err)
	}
	if err := handler.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncInteractionCommandRejectsInvalidUnsupportedAndExcessWorkBeforeExecution(t *testing.T) {
	invalid := &fakeAsyncBackend{validateErr: ErrInvalidCommand, started: make(chan struct{}), release: make(chan struct{}), completed: make(chan error, 1)}
	handler, _ := NewHandler(invalid, invalid)
	request := httptest.NewRequest(http.MethodPost, "/v1/commands", bytes.NewBufferString(
		`{"version":"workspace-command.v1","command_id":"CMD-ASYNC-BAD","operation":"interaction.answer","approved":true,"payload":{}}`,
	))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Prefer", "respond-async")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid async = %d %s", response.Code, response.Body.String())
	}

	unsupported := httptest.NewRequest(http.MethodPost, "/v1/commands", bytes.NewBufferString(
		`{"version":"workspace-command.v1","command_id":"CMD-ASYNC-OTHER","operation":"task.execute","approved":true,"payload":{}}`,
	))
	unsupported.Header.Set("Content-Type", "application/json")
	unsupported.Header.Set("Prefer", "respond-async")
	unsupportedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unsupportedResponse, unsupported)
	if unsupportedResponse.Code != http.StatusBadRequest || !strings.Contains(unsupportedResponse.Body.String(), "ASYNC_OPERATION_UNSUPPORTED") {
		t.Fatalf("unsupported async = %d %s", unsupportedResponse.Code, unsupportedResponse.Body.String())
	}

	busy := &fakeAsyncBackend{started: make(chan struct{}), release: make(chan struct{}), completed: make(chan error, 1)}
	runner := newAsyncCommandRunner(busy, 1)
	command := Command{Version: ContractVersion, CommandID: "CMD-ASYNC-CAPACITY", Operation: "interaction.answer", Approved: true, Payload: json.RawMessage(`{}`)}
	if err := runner.accept(command); err != nil {
		t.Fatal(err)
	}
	<-busy.started
	command.CommandID = "CMD-ASYNC-CAPACITY-2"
	if err := runner.accept(command); !errors.Is(err, errAsyncCapacity) {
		t.Fatalf("capacity error = %v", err)
	}
	close(busy.release)
	if err := <-busy.completed; err != nil {
		t.Fatal(err)
	}
	if err := runner.shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncCommandShutdownCancelsAtDeadline(t *testing.T) {
	backend := &fakeAsyncBackend{started: make(chan struct{}), release: make(chan struct{}), completed: make(chan error, 1)}
	runner := newAsyncCommandRunner(backend, 1)
	command := Command{Version: ContractVersion, CommandID: "CMD-ASYNC-SHUTDOWN", Operation: "interaction.answer", Approved: true, Payload: json.RawMessage(`{}`)}
	if err := runner.accept(command); err != nil {
		t.Fatal(err)
	}
	<-backend.started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := runner.shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v", err)
	}
	if err := <-backend.completed; !errors.Is(err, context.Canceled) {
		t.Fatalf("command cancellation = %v", err)
	}
}

func TestTrustedLANPairingProtectsAPIAndKeepsCodeOutOfResponses(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "社員"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "社員", "伊藤 健太.md"), []byte("---\nid: QA-001\ndepartment: 品質保証部\nrole: QA Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	executor, err := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(executor, executor)
	access, code, err := NewLocalAccess()
	if err != nil || len(code) < 20 {
		t.Fatalf("NewLocalAccess() code length=%d err=%v", len(code), err)
	}
	if err := handler.EnableLocalAccess(access); err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/organization", nil))
	if unauthorized.Code != http.StatusUnauthorized || strings.Contains(unauthorized.Body.String(), code) {
		t.Fatalf("unauthorized = %d %s", unauthorized.Code, unauthorized.Body.String())
	}

	pairRequest := httptest.NewRequest(http.MethodPost, "/v1/local-access/pair", bytes.NewBufferString(`{"code":"`+code+`"}`))
	pairRequest.Header.Set("Content-Type", "application/json")
	pairRequest.Header.Set(localIntentHeader, localIntentValue)
	pairRequest.Header.Set("Origin", "http://example.com")
	pairResponse := httptest.NewRecorder()
	handler.ServeHTTP(pairResponse, pairRequest)
	if pairResponse.Code != http.StatusOK || strings.Contains(pairResponse.Body.String(), code) || len(pairResponse.Result().Cookies()) != 1 || !pairResponse.Result().Cookies()[0].HttpOnly {
		t.Fatalf("pair = %d %s cookies=%v", pairResponse.Code, pairResponse.Body.String(), pairResponse.Result().Cookies())
	}
	cookie := pairResponse.Result().Cookies()[0]

	inspectionRequest := httptest.NewRequest(http.MethodGet, "/v1/organization", nil)
	inspectionRequest.AddCookie(cookie)
	inspectionResponse := httptest.NewRecorder()
	handler.ServeHTTP(inspectionResponse, inspectionRequest)
	if inspectionResponse.Code != http.StatusOK || !strings.Contains(inspectionResponse.Body.String(), "QA-001") || strings.Contains(inspectionResponse.Body.String(), code) {
		t.Fatalf("authorized inspection = %d %s", inspectionResponse.Code, inspectionResponse.Body.String())
	}

	planRequest := httptest.NewRequest(http.MethodPost, "/v1/interaction-plans", bytes.NewBufferString(
		`{"version":"workspace-interaction.v1","session_id":"SESSION-TRUSTED-LAN-001","request":"iPhoneから仕事を依頼する","model":"Claude Sonnet 5","current_time":"2026-08-12T12:00:00+09:00"}`,
	))
	planRequest.AddCookie(cookie)
	planRequest.Header.Set("Content-Type", "application/json")
	planRequest.Header.Set(localIntentHeader, localIntentValue)
	planRequest.Header.Set("Origin", "http://example.com")
	planResponse := httptest.NewRecorder()
	handler.ServeHTTP(planResponse, planRequest)
	if planResponse.Code != http.StatusOK || !strings.Contains(planResponse.Body.String(), "request_digest") {
		t.Fatalf("authorized Interaction preview = %d %s", planResponse.Code, planResponse.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".workspace-os")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only Interaction preview changed Vault: %v", err)
	}

	mutation := httptest.NewRequest(http.MethodPost, "/v1/commands", bytes.NewBufferString(`{}`))
	mutation.AddCookie(cookie)
	mutation.Header.Set("Content-Type", "application/json")
	mutationResponse := httptest.NewRecorder()
	handler.ServeHTTP(mutationResponse, mutation)
	if mutationResponse.Code != http.StatusForbidden {
		t.Fatalf("mutation without same-origin intent = %d %s", mutationResponse.Code, mutationResponse.Body.String())
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
	nextResponse := httptest.NewRecorder()
	handler.ServeHTTP(nextResponse, httptest.NewRequest(http.MethodGet, "/v1/interactions/SESSION-HTTP-001/next", nil))
	if nextResponse.Code != http.StatusOK || !strings.Contains(nextResponse.Body.String(), string(interaction.NextApprovePlanGeneration)) ||
		!strings.Contains(nextResponse.Body.String(), `"expected_version":1`) {
		t.Fatalf("Interaction next = %d %s", nextResponse.Code, nextResponse.Body.String())
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

func TestInteractionPlanDefaultsToAutomaticRuntimeSelection(t *testing.T) {
	root := t.TempDir()
	executor, err := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(executor, executor)
	response := performJSONRequest(t, handler, http.MethodPost, "/v1/interaction-plans", map[string]any{
		"version": InteractionContractVersion, "session_id": "SESSION-AUTO-MODEL-001",
		"request": "役割に適したAIで仕事を計画して", "current_time": "2026-08-12T12:00:00+09:00",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("automatic model plan = %d %s", response.Code, response.Body.String())
	}
	var plan workspaceprocess.InteractionStartPlan
	decodeHTTPResult(t, response, &plan)
	if plan.Session.Model != AutomaticInteractionModel || !plan.Executable {
		t.Fatalf("automatic model plan = %#v", plan)
	}
}

func TestMobileInteractionHTTPFlowUsesMockProviderAndTemporaryVaultToCompletion(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"社員", "プロジェクト"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"山本 真帆.md": "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n",
		"伊藤 健太.md": "---\nid: QA-001\ndepartment: 品質保証部\nrole: QA Engineer\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n",
	} {
		if err := os.WriteFile(filepath.Join(root, "社員", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	planOutput := func(questions []string) string {
		encoded, _ := json.Marshal(map[string]any{
			"project_name": "iPhone依頼", "objective": "依頼から成果物を完成させる", "summary": "mobile E2E",
			"required_departments": []string{"企画部"}, "required_roles": []string{"Product Manager"},
			"assigned_existing_employees": []string{"PLAN-001"},
			"proposed_tasks":              []map[string]any{{"title": "要件をまとめる", "assignee_id": "PLAN-001", "dependency_ids": []string{}, "rationale": "依頼を形にするため"}},
			"risks":                       []string{}, "ceo_questions": questions,
		})
		return string(encoded)
	}
	providerOutputs := []string{
		planOutput([]string{"対象はiPhoneを優先しますか？"}),
		planOutput([]string{}),
		"# 完成した成果物\n\niPhone向けの要件です。",
		"# Review\n\n確認結果です。\n\nREVIEW_RESULT_JSON_START\n{\"verdict\":\"Approve\",\"issues\":[]}\nREVIEW_RESULT_JSON_END",
	}
	providerCalls := 0
	providerServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/messages" || request.Header.Get("x-api-key") != "fake-mobile-key" || providerCalls >= len(providerOutputs) {
			t.Fatalf("unexpected mock Provider request path=%s calls=%d", request.URL.Path, providerCalls)
		}
		output := providerOutputs[providerCalls]
		providerCalls++
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"model": "claude-mobile-test", "content": []map[string]string{{"type": "text", "text": output}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
	}))
	defer providerServer.Close()
	executor, err := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{
		APIKey: "fake-mobile-key", ProviderModel: "claude-mobile-test", BaseURL: providerServer.URL,
	}, providerServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	handler, _ := NewHandler(executor, executor)
	base := time.Date(2026, time.August, 10, 9, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	sessionID := "SESSION-MOBILE-E2E-001"

	planRequest := map[string]any{
		"version": InteractionContractVersion, "session_id": sessionID, "request": "iPhone向けに仕事を完成させて",
		"model": "Claude Sonnet 5", "current_time": base,
	}
	planResponse := performJSONRequest(t, handler, http.MethodPost, "/v1/interaction-plans", planRequest)
	var startPlan workspaceprocess.InteractionStartPlan
	decodeHTTPResult(t, planResponse, &startPlan)
	performSuccessfulCommand(t, handler, map[string]any{
		"version": ContractVersion, "command_id": "CMD-MOBILE-START-001", "operation": "interaction.start", "approved": true,
		"payload": map[string]any{"session_id": sessionID, "request": planRequest["request"], "request_digest": startPlan.Session.RequestDigest, "model": planRequest["model"], "current_time": base},
	})

	performSuccessfulCommand(t, handler, map[string]any{
		"version": ContractVersion, "command_id": "CMD-MOBILE-PLAN-001", "operation": "interaction.plan.generate", "approved": true,
		"payload": map[string]any{"session_id": sessionID, "expected_version": 1, "current_time": base.Add(time.Minute)},
	})
	next := inspectInteractionNextHTTP(t, handler, sessionID)
	if next.Kind != interaction.NextAnswerClarifications || len(next.Questions) != 1 {
		t.Fatalf("clarification next = %#v", next)
	}
	performSuccessfulCommand(t, handler, map[string]any{
		"version": ContractVersion, "command_id": "CMD-MOBILE-ANSWER-001", "operation": "interaction.answer", "approved": true,
		"payload": map[string]any{"session_id": sessionID, "expected_version": next.ExpectedVersion, "answers": []map[string]string{{"question": next.Questions[0], "answer": "はい、iPhoneを優先します"}}, "current_time": base.Add(2 * time.Minute)},
	})
	next = inspectInteractionNextHTTP(t, handler, sessionID)
	performSuccessfulCommand(t, handler, map[string]any{
		"version": ContractVersion, "command_id": "CMD-MOBILE-PLAN-002", "operation": "interaction.plan.generate", "approved": true,
		"payload": map[string]any{"session_id": sessionID, "expected_version": next.ExpectedVersion, "current_time": base.Add(3 * time.Minute)},
	})
	next = inspectInteractionNextHTTP(t, handler, sessionID)
	if next.Kind != interaction.NextApprovePlanApply || next.PlanDigest == "" {
		t.Fatalf("plan apply next = %#v", next)
	}
	performSuccessfulCommand(t, handler, map[string]any{
		"version": ContractVersion, "command_id": "CMD-MOBILE-APPLY-001", "operation": "interaction.plan.apply", "approved": true,
		"payload": map[string]any{"session_id": sessionID, "expected_version": next.ExpectedVersion, "project_id": "PROJECT-MOBILE-001", "plan_digest": next.PlanDigest, "current_time": base.Add(4 * time.Minute)},
	})

	organizationResponse := httptest.NewRecorder()
	handler.ServeHTTP(organizationResponse, httptest.NewRequest(http.MethodGet, "/v1/organization", nil))
	if organizationResponse.Code != http.StatusOK || !strings.Contains(organizationResponse.Body.String(), "QA-001") {
		t.Fatalf("organization = %d %s", organizationResponse.Code, organizationResponse.Body.String())
	}
	next = inspectInteractionNextHTTP(t, handler, sessionID)
	workflowTime := base.Add(5 * time.Minute)
	workflowPlanResponse := performJSONRequest(t, handler, http.MethodPost, "/v1/interaction-workflow-plans", map[string]any{
		"version": InteractionContractVersion, "session_id": sessionID, "expected_version": next.ExpectedVersion,
		"reviewer_id": "QA-001", "current_time": workflowTime, "max_tasks": 10,
	})
	var workflowPlan workspaceprocess.InteractionWorkflowPlan
	decodeHTTPResult(t, workflowPlanResponse, &workflowPlan)
	performAcceptedCommand(t, handler, map[string]any{
		"version": ContractVersion, "command_id": "CMD-MOBILE-WORKFLOW-001", "operation": "interaction.workflow.execute", "approved": true,
		"payload": map[string]any{
			"session_id": sessionID, "expected_version": next.ExpectedVersion, "reviewer_id": "QA-001", "current_time": workflowTime,
			"max_tasks": 10, "autonomy_contract": workflowPlan.Autonomy, "workflow_plan_digest": workflowPlan.WorkflowPlanDigest, "approval_reference": "mobile-e2e-approval",
		},
	})
	waitForCommandStateHTTP(t, handler, "CMD-MOBILE-WORKFLOW-001", commandledger.StateSucceeded)

	next = inspectInteractionNextHTTP(t, handler, sessionID)
	if next.Kind != interaction.NextOptionalAction || !reflect.DeepEqual(next.EligibleTaskIDs, []string{"TASK-001"}) || providerCalls != len(providerOutputs) {
		t.Fatalf("completed next = %#v providerCalls=%d", next, providerCalls)
	}
	for _, relative := range []string{
		"プロジェクト/iPhone依頼/Deliverables/TASK-001.md",
		"プロジェクト/iPhone依頼/Reviews/TASK-001.review.json",
		"プロジェクト/iPhone依頼/Audit Log.md",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("missing mobile E2E evidence %s: %v", relative, err)
		}
	}
	evidenceResponse := httptest.NewRecorder()
	handler.ServeHTTP(evidenceResponse, httptest.NewRequest(http.MethodGet, "/v1/projects/"+url.PathEscape("iPhone依頼")+"/tasks/TASK-001/evidence", nil))
	var evidence workspaceprocess.TaskEvidenceInspection
	decodeHTTPResult(t, evidenceResponse, &evidence)
	if evidence.Deliverable == nil || evidence.Deliverable.Title != "要件をまとめる" || !strings.Contains(evidence.Deliverable.Content, "完成した成果物") || len(evidence.Reviews) != 1 || evidence.Reviews[0].Decision.Verdict != "Approve" {
		t.Fatalf("Task evidence = %#v", evidence)
	}
	reportResponse := httptest.NewRecorder()
	handler.ServeHTTP(reportResponse, httptest.NewRequest(http.MethodGet, "/v1/interactions/"+sessionID+"/work-report", nil))
	var report workspaceprocess.WorkReport
	decodeHTTPResult(t, reportResponse, &report)
	if report.Autonomy == nil || report.Autonomy.ExecutionLimit != 10 ||
		!reflect.DeepEqual(report.Autonomy.AllowedEmployeeIDs, []string{"PLAN-001", "QA-001"}) ||
		!report.Proof.FullyVerified || report.Proof.VerifiedTasks != 1 || len(report.Proof.Tasks) != 1 ||
		report.Proof.Tasks[0].MakerID != "PLAN-001" || report.Proof.Tasks[0].Review.ReviewerID != "QA-001" ||
		!report.Proof.Audit.Readable || report.Proof.Audit.RecordedEvents == 0 ||
		report.Attention.CompanySteps != 4 || report.Attention.DelegatedSteps != 2 ||
		report.Attention.ClarificationQuestions != 1 || report.Attention.ApprovalMoments != 5 || !report.Attention.NoActionNeeded {
		t.Fatalf("Work Report = %#v", report)
	}
}

func TestInteractionActionPlanHTTPRequiresProspectiveCommandIdentity(t *testing.T) {
	backend := &fakeInteractionActionBackend{plan: workspaceprocess.InteractionActionPlan{
		SessionID: "SESSION-HTTP-001", SessionVersion: 4, ProjectID: "PROJECT-001", ProjectName: "記事案件",
		TaskID: "TASK-001", TargetID: "site-main", ActionCommandID: "CHILD-ACTION-001",
		SourceSHA256: strings.Repeat("a", 64), ActionPlanDigest: "sha256:" + strings.Repeat("b", 64),
		Executable: true, ApprovalRequired: true,
	}}
	handler, err := NewHandler(backend, backend)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"version":"workspace-interaction.v1","session_id":"SESSION-HTTP-001","expected_version":4,"task_id":"TASK-001","target_id":"site-main","current_time":"2026-08-09T12:00:00Z","command_id":"CMD-INTERACTION-ACTION-001"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/interaction-action-plans", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), backend.plan.ActionPlanDigest) || backend.calls != 0 {
		t.Fatalf("Interaction Action plan response = %d %s calls=%d", response.Code, response.Body.String(), backend.calls)
	}
	invalid := httptest.NewRequest(http.MethodPost, "/v1/interaction-action-plans", bytes.NewBufferString(strings.Replace(body, `"command_id":"CMD-INTERACTION-ACTION-001"`, `"command_id":""`, 1)))
	invalid.Header.Set("Content-Type", "application/json")
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid Interaction Action plan = %d %s", invalidResponse.Code, invalidResponse.Body.String())
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

func performJSONRequest(t *testing.T, handler http.Handler, method, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	content, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(content))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s %s = %d %s", method, path, response.Code, response.Body.String())
	}
	return response
}

func performSuccessfulCommand(t *testing.T, handler http.Handler, command map[string]any) {
	t.Helper()
	response := performCommand(t, handler, command)
	if response.Code != http.StatusOK {
		t.Fatalf("command %v = %d %s", command["operation"], response.Code, response.Body.String())
	}
}

func performAcceptedCommand(t *testing.T, handler http.Handler, command map[string]any) {
	t.Helper()
	content, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/commands", bytes.NewReader(content))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Prefer", "respond-async")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Header().Get("Location") == "" {
		t.Fatalf("accepted command %v = %d %s", command["operation"], response.Code, response.Body.String())
	}
}

func waitForCommandStateHTTP(t *testing.T, handler http.Handler, commandID string, expected commandledger.State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/commands/"+commandID+"?scope=workspace", nil))
		if response.Code == http.StatusOK {
			var record commandledger.Record
			decodeHTTPResult(t, response, &record)
			if record.State == expected {
				return
			}
			if record.State.Terminal() {
				t.Fatalf("command %s terminal state = %s", commandID, record.State)
			}
		} else if response.Code != http.StatusNotFound {
			t.Fatalf("command status = %d %s", response.Code, response.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("command %s did not reach %s", commandID, expected)
}

func decodeHTTPResult(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	var envelope Response
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK {
		t.Fatalf("HTTP result is not OK: %#v", envelope.Error)
	}
	if err := json.Unmarshal(envelope.Result, target); err != nil {
		t.Fatal(err)
	}
}

func inspectInteractionNextHTTP(t *testing.T, handler http.Handler, sessionID string) interaction.NextAction {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/interactions/"+sessionID+"/next", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("Interaction next = %d %s", response.Code, response.Body.String())
	}
	var next interaction.NextAction
	decodeHTTPResult(t, response, &next)
	return next
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
