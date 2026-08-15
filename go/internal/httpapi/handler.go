package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/metrics"
	"github.com/AkiraShimizu0/workcairn/go/internal/notification"
	workspaceprocess "github.com/AkiraShimizu0/workcairn/go/internal/process"
	"github.com/AkiraShimizu0/workcairn/go/internal/scheduler"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

const maxCommandRequestBytes = 2 << 20

type Handler struct {
	executor                   Executor
	inspector                  Inspector
	scheduleInspector          ScheduleInspector
	metricsInspector           MetricsInspector
	notificationInspector      NotificationInspector
	interactionInspector       InteractionInspector
	interactionPlanner         InteractionPlanner
	interactionWorkflowPlanner InteractionWorkflowPlanner
	interactionActionPlanner   InteractionActionPlanner
	organizationInspector      OrganizationInspector
	taskEvidenceInspector      TaskEvidenceInspector
	workReportInspector        WorkReportInspector
	conversationInspector      ConversationInspector
	companyActivityInspector   CompanyActivityInspector
	providerStatusInspector    ProviderStatusInspector
	localSetup                 LocalSetup
	localSetupAddress          string
	localAccess                *LocalAccess
	asyncCommands              *asyncCommandRunner
	mux                        *http.ServeMux
}

type ScheduleInspector interface {
	InspectSchedules(ctx context.Context) ([]scheduler.Record, error)
	InspectSchedule(ctx context.Context, scheduleID string) (scheduler.Record, error)
}

type MetricsInspector interface {
	InspectMetrics() metrics.Snapshot
}

type NotificationInspector interface {
	InspectNotifications(ctx context.Context) ([]notification.Record, error)
	InspectNotification(ctx context.Context, eventID string) (notification.Record, error)
}

type InteractionInspector interface {
	InspectInteractions(ctx context.Context) ([]interaction.Record, error)
	InspectInteraction(ctx context.Context, sessionID string) (interaction.Record, error)
	InspectInteractionNext(ctx context.Context, sessionID string) (interaction.NextAction, error)
}

type InteractionPlanner interface {
	PlanInteraction(ctx context.Context, request InteractionPlanRequest) (workspaceprocess.InteractionStartPlan, error)
}

type InteractionWorkflowPlanner interface {
	PlanInteractionWorkflow(ctx context.Context, request InteractionWorkflowPlanRequest) (workspaceprocess.InteractionWorkflowPlan, error)
}

type InteractionActionPlanner interface {
	PlanInteractionAction(ctx context.Context, request InteractionActionPlanRequest) (workspaceprocess.InteractionActionPlan, error)
}

type OrganizationInspector interface {
	InspectOrganization(ctx context.Context) (workspaceprocess.OrganizationInspection, error)
}

type TaskEvidenceInspector interface {
	InspectTaskEvidence(ctx context.Context, projectName, taskID string) (workspaceprocess.TaskEvidenceInspection, error)
}

type WorkReportInspector interface {
	InspectWorkReport(ctx context.Context, sessionID string) (workspaceprocess.WorkReport, error)
}

type ConversationInspector interface {
	InspectConversation(ctx context.Context, sessionID string) (workspaceprocess.ConversationInspection, error)
}

type CompanyActivityInspector interface {
	InspectCompanyActivity(ctx context.Context) (workspaceprocess.CompanyActivity, error)
}

type ProviderStatusInspector interface {
	InspectProviderStatus() ProviderStatus
}

type WorkspaceStatusInspector interface {
	InspectWorkspaceStatus(ctx context.Context) (WorkspaceStatus, error)
}

// LocalSetup owns explicit, Mac-only OS integration. It must never accept a
// credential or filesystem path from the HTTP request.
type LocalSetup interface {
	ConnectClaude(ctx context.Context) error
	RevealWorkspace(ctx context.Context) error
}

func NewHandler(executor Executor, inspector Inspector) (*Handler, error) {
	if executor == nil || inspector == nil {
		return nil, errors.New("Command executor and inspector are required")
	}
	handler := &Handler{
		executor: executor, inspector: inspector, mux: http.NewServeMux(),
		asyncCommands: newAsyncCommandRunner(executor, maxConcurrentAsyncCommands),
	}
	handler.mux.HandleFunc("GET /healthz", handler.health)
	handler.mux.HandleFunc("GET /readyz", handler.ready)
	handler.mux.HandleFunc("GET /{$}", handler.webIndex)
	handler.mux.HandleFunc("GET /assets/{name}", handler.webAsset)
	handler.mux.HandleFunc("GET /manifest.webmanifest", handler.webManifest)
	handler.mux.HandleFunc("GET /v1/local-access/status", handler.localAccessStatus)
	handler.mux.HandleFunc("POST /v1/local-access/pair", handler.pairLocalAccess)
	handler.mux.HandleFunc("POST /v1/commands", handler.execute)
	handler.mux.HandleFunc("GET /v1/commands/{command_id}", handler.inspect)
	if scheduleInspector, ok := executor.(ScheduleInspector); ok {
		handler.scheduleInspector = scheduleInspector
		handler.mux.HandleFunc("GET /v1/schedules", handler.inspectSchedules)
		handler.mux.HandleFunc("GET /v1/schedules/{schedule_id}", handler.inspectSchedule)
	}
	if metricsInspector, ok := executor.(MetricsInspector); ok {
		handler.metricsInspector = metricsInspector
		handler.mux.HandleFunc("GET /v1/metrics", handler.inspectMetrics)
	}
	if notificationInspector, ok := executor.(NotificationInspector); ok {
		handler.notificationInspector = notificationInspector
		handler.mux.HandleFunc("GET /v1/notifications", handler.inspectNotifications)
		handler.mux.HandleFunc("GET /v1/notifications/{event_id}", handler.inspectNotification)
	}
	if interactionInspector, ok := executor.(InteractionInspector); ok {
		handler.interactionInspector = interactionInspector
		handler.mux.HandleFunc("GET /v1/interactions", handler.inspectInteractions)
		handler.mux.HandleFunc("GET /v1/interactions/{session_id}", handler.inspectInteraction)
		handler.mux.HandleFunc("GET /v1/interactions/{session_id}/next", handler.inspectInteractionNext)
	}
	if interactionPlanner, ok := executor.(InteractionPlanner); ok {
		handler.interactionPlanner = interactionPlanner
		handler.mux.HandleFunc("POST /v1/interaction-plans", handler.planInteraction)
	}
	if workflowPlanner, ok := executor.(InteractionWorkflowPlanner); ok {
		handler.interactionWorkflowPlanner = workflowPlanner
		handler.mux.HandleFunc("POST /v1/interaction-workflow-plans", handler.planInteractionWorkflow)
	}
	if actionPlanner, ok := executor.(InteractionActionPlanner); ok {
		handler.interactionActionPlanner = actionPlanner
		handler.mux.HandleFunc("POST /v1/interaction-action-plans", handler.planInteractionAction)
	}
	if organizationInspector, ok := executor.(OrganizationInspector); ok {
		handler.organizationInspector = organizationInspector
		handler.mux.HandleFunc("GET /v1/organization", handler.inspectOrganization)
	}
	if taskEvidenceInspector, ok := executor.(TaskEvidenceInspector); ok {
		handler.taskEvidenceInspector = taskEvidenceInspector
		handler.mux.HandleFunc("GET /v1/projects/{project_name}/tasks/{task_id}/evidence", handler.inspectTaskEvidence)
	}
	if workReportInspector, ok := executor.(WorkReportInspector); ok {
		handler.workReportInspector = workReportInspector
		handler.mux.HandleFunc("GET /v1/interactions/{session_id}/work-report", handler.inspectWorkReport)
	}
	if conversationInspector, ok := executor.(ConversationInspector); ok {
		handler.conversationInspector = conversationInspector
		handler.mux.HandleFunc("GET /v1/interactions/{session_id}/conversation", handler.inspectConversation)
	}
	if companyActivityInspector, ok := executor.(CompanyActivityInspector); ok {
		handler.companyActivityInspector = companyActivityInspector
		handler.mux.HandleFunc("GET /v1/company-activity", handler.inspectCompanyActivity)
	}
	if providerStatusInspector, ok := executor.(ProviderStatusInspector); ok {
		handler.providerStatusInspector = providerStatusInspector
		handler.mux.HandleFunc("GET /v1/provider-status", handler.inspectProviderStatus)
	}
	if workspaceStatusInspector, ok := executor.(WorkspaceStatusInspector); ok {
		handler.mux.HandleFunc("GET /v1/workspace-status", func(response http.ResponseWriter, request *http.Request) {
			status, statusErr := workspaceStatusInspector.InspectWorkspaceStatus(request.Context())
			if statusErr != nil {
				writeCommandResponse(response, http.StatusUnprocessableEntity, Response{Version: WorkspaceStatusVersion, OK: false, Error: &CommandError{Code: "WORKSPACE_STATUS_FAILED", RecoveryRequired: true}})
				return
			}
			encoded, encodeErr := json.Marshal(status)
			if encodeErr != nil {
				writeCommandResponse(response, http.StatusInternalServerError, Response{Version: WorkspaceStatusVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
				return
			}
			writeCommandResponse(response, http.StatusOK, Response{Version: WorkspaceStatusVersion, OK: true, Result: encoded})
		})
	}
	return handler, nil
}

func (handler *Handler) EnableLocalSetup(setup LocalSetup, localAddress string) error {
	if setup == nil || strings.TrimSpace(localAddress) == "" {
		return errors.New("local setup and Mac address are required")
	}
	handler.localSetup = setup
	handler.localSetupAddress = strings.TrimSpace(localAddress)
	handler.mux.HandleFunc("POST /v1/local-setup/claude", handler.connectClaude)
	handler.mux.HandleFunc("POST /v1/local-setup/reveal-workspace", handler.revealWorkspace)
	return nil
}

func (handler *Handler) connectClaude(response http.ResponseWriter, request *http.Request) {
	if !handler.authorizeLocalSetup(response, request) {
		return
	}
	if err := handler.localSetup.ConnectClaude(request.Context()); err != nil {
		writeCommandResponse(response, http.StatusUnprocessableEntity, Response{Version: ProviderStatusVersion, OK: false, Error: providerConnectionError(err)})
		return
	}
	encoded, _ := json.Marshal(handler.providerStatusInspector.InspectProviderStatus())
	writeCommandResponse(response, http.StatusOK, Response{Version: ProviderStatusVersion, OK: true, Result: encoded})
}

type credentialSetupDiagnostic interface {
	CredentialSubstage() string
	CredentialClassification() string
}

func providerConnectionError(err error) *CommandError {
	commandErr := &CommandError{Code: "PROVIDER_CONNECTION_SETUP_FAILED", Stage: "provider_connection_setup"}
	var diagnostic credentialSetupDiagnostic
	if !errors.As(err, &diagnostic) {
		return commandErr
	}
	envelope := failure.New(commandErr.Code, commandErr.Stage)
	envelope.Substage = diagnostic.CredentialSubstage()
	envelope.Category = diagnostic.CredentialClassification()
	commandErr.Details = &envelope
	return commandErr
}

func (handler *Handler) revealWorkspace(response http.ResponseWriter, request *http.Request) {
	if !handler.authorizeLocalSetup(response, request) {
		return
	}
	if err := handler.localSetup.RevealWorkspace(request.Context()); err != nil {
		writeCommandResponse(response, http.StatusUnprocessableEntity, Response{Version: WorkspaceStatusVersion, OK: false, Error: &CommandError{Code: "WORKSPACE_REVEAL_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: WorkspaceStatusVersion, OK: true, Result: json.RawMessage(`{"revealed":true}`)})
}

func (handler *Handler) authorizeLocalSetup(response http.ResponseWriter, request *http.Request) bool {
	if handler.localSetup == nil || !validLocalIntent(request) {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "LOCAL_MAC_SETUP_REQUIRED"})
		return false
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "LOCAL_MAC_SETUP_REQUIRED"})
		return false
	}
	remote := net.ParseIP(host)
	allowed := net.ParseIP(handler.localSetupAddress)
	if remote == nil || !(remote.IsLoopback() || allowed != nil && remote.Equal(allowed)) {
		writeJSON(response, http.StatusForbidden, map[string]string{"error": "LOCAL_MAC_SETUP_REQUIRED"})
		return false
	}
	return true
}

func (handler *Handler) inspectProviderStatus(response http.ResponseWriter, _ *http.Request) {
	encoded, err := json.Marshal(handler.providerStatusInspector.InspectProviderStatus())
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: ProviderStatusVersion, OK: false, Error: &CommandError{Code: "PROVIDER_STATUS_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: ProviderStatusVersion, OK: true, Result: encoded})
}

func (handler *Handler) inspectCompanyActivity(response http.ResponseWriter, request *http.Request) {
	activity, err := handler.companyActivityInspector.InspectCompanyActivity(request.Context())
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: workspaceprocess.CompanyActivityVersion, OK: false, Error: &CommandError{Code: "COMPANY_ACTIVITY_INSPECTION_FAILED", RecoveryRequired: true}})
		return
	}
	encoded, err := json.Marshal(activity)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: workspaceprocess.CompanyActivityVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: workspaceprocess.CompanyActivityVersion, OK: true, Result: encoded})
}

func (handler *Handler) inspectWorkReport(response http.ResponseWriter, request *http.Request) {
	report, err := handler.workReportInspector.InspectWorkReport(request.Context(), request.PathValue("session_id"))
	if err != nil {
		status, code := http.StatusInternalServerError, "WORK_REPORT_INSPECTION_FAILED"
		if errors.Is(err, interaction.ErrNotFound) {
			status, code = http.StatusNotFound, "INTERACTION_NOT_FOUND"
		} else if errors.Is(err, interaction.ErrInvalidSession) {
			status, code = http.StatusBadRequest, "INVALID_SESSION_ID"
		}
		writeCommandResponse(response, status, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: code, RecoveryRequired: status == http.StatusInternalServerError}})
		return
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: InteractionContractVersion, OK: true, Result: encoded})
}

func (handler *Handler) inspectConversation(response http.ResponseWriter, request *http.Request) {
	inspection, err := handler.conversationInspector.InspectConversation(request.Context(), request.PathValue("session_id"))
	if err != nil {
		status, code := http.StatusInternalServerError, "CONVERSATION_INSPECTION_FAILED"
		if errors.Is(err, interaction.ErrNotFound) {
			status, code = http.StatusNotFound, "INTERACTION_NOT_FOUND"
		} else if errors.Is(err, interaction.ErrInvalidSession) {
			status, code = http.StatusBadRequest, "INVALID_SESSION_ID"
		}
		writeCommandResponse(response, status, Response{Version: workspaceprocess.ConversationVersion, OK: false, Error: &CommandError{Code: code, RecoveryRequired: status == http.StatusInternalServerError}})
		return
	}
	encoded, err := json.Marshal(inspection)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: workspaceprocess.ConversationVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: workspaceprocess.ConversationVersion, OK: true, Result: encoded})
}

func (handler *Handler) inspectTaskEvidence(response http.ResponseWriter, request *http.Request) {
	inspection, err := handler.taskEvidenceInspector.InspectTaskEvidence(request.Context(), request.PathValue("project_name"), request.PathValue("task_id"))
	if err != nil {
		status, code := http.StatusUnprocessableEntity, "TASK_EVIDENCE_INSPECTION_FAILED"
		if errors.Is(err, task.ErrTaskNotFound) {
			status, code = http.StatusNotFound, "TASK_NOT_FOUND"
		}
		writeCommandResponse(response, status, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: code, RecoveryRequired: status != http.StatusNotFound}})
		return
	}
	encoded, err := json.Marshal(inspection)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: ContractVersion, OK: true, Result: encoded})
}

func (handler *Handler) inspectOrganization(response http.ResponseWriter, request *http.Request) {
	inspection, err := handler.organizationInspector.InspectOrganization(request.Context())
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: "ORGANIZATION_INSPECTION_FAILED", RecoveryRequired: true}})
		return
	}
	encoded, err := json.Marshal(inspection)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: ContractVersion, OK: true, Result: encoded})
}

func (handler *Handler) inspectInteractionNext(response http.ResponseWriter, request *http.Request) {
	next, err := handler.interactionInspector.InspectInteractionNext(request.Context(), request.PathValue("session_id"))
	if err != nil {
		status, code := http.StatusInternalServerError, "INTERACTION_INSPECTION_FAILED"
		if errors.Is(err, interaction.ErrNotFound) {
			status, code = http.StatusNotFound, "INTERACTION_NOT_FOUND"
		} else if errors.Is(err, interaction.ErrInvalidSession) {
			status, code = http.StatusBadRequest, "INVALID_SESSION_ID"
		}
		writeCommandResponse(response, status, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: code, RecoveryRequired: status == http.StatusInternalServerError}})
		return
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: InteractionContractVersion, OK: true, Result: encoded})
}

func (handler *Handler) planInteractionAction(response http.ResponseWriter, request *http.Request) {
	if !strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		writeCommandResponse(response, http.StatusUnsupportedMediaType, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "UNSUPPORTED_MEDIA_TYPE"}})
		return
	}
	content, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxCommandRequestBytes))
	if err != nil {
		writeCommandResponse(response, http.StatusRequestEntityTooLarge, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "INVALID_INTERACTION_ACTION_PLAN"}})
		return
	}
	var planRequest InteractionActionPlanRequest
	if err := decodePayload(content, &planRequest); err != nil || planRequest.Validate() != nil {
		writeCommandResponse(response, http.StatusBadRequest, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "INVALID_INTERACTION_ACTION_PLAN"}})
		return
	}
	plan, err := handler.interactionActionPlanner.PlanInteractionAction(request.Context(), planRequest)
	if err != nil {
		writeCommandResponse(response, http.StatusUnprocessableEntity, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "INTERACTION_ACTION_PLAN_FAILED"}})
		return
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: InteractionContractVersion, OK: true, Result: encoded})
}

func (handler *Handler) planInteractionWorkflow(response http.ResponseWriter, request *http.Request) {
	if !strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		writeCommandResponse(response, http.StatusUnsupportedMediaType, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "UNSUPPORTED_MEDIA_TYPE"}})
		return
	}
	content, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxCommandRequestBytes))
	if err != nil {
		writeCommandResponse(response, http.StatusRequestEntityTooLarge, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "INVALID_INTERACTION_WORKFLOW_PLAN"}})
		return
	}
	var planRequest InteractionWorkflowPlanRequest
	if err := decodePayload(content, &planRequest); err != nil || planRequest.Validate() != nil {
		writeCommandResponse(response, http.StatusBadRequest, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "INVALID_INTERACTION_WORKFLOW_PLAN"}})
		return
	}
	plan, err := handler.interactionWorkflowPlanner.PlanInteractionWorkflow(request.Context(), planRequest)
	if err != nil {
		code, stage := "INTERACTION_WORKFLOW_PLAN_FAILED", ""
		var planError *workspaceprocess.InteractionWorkflowPlanError
		if errors.As(err, &planError) {
			stage = string(planError.Stage)
			if planError.Stage == workspaceprocess.InteractionWorkflowAssignmentStage {
				code = "WORKFLOW_TASK_ASSIGNMENT_REQUIRED"
			} else if planError.Stage == workspaceprocess.InteractionWorkflowReviewerStage {
				code = "WORKFLOW_REVIEWER_ASSIGNMENT_REQUIRED"
			}
		}
		writeCommandResponse(response, http.StatusUnprocessableEntity, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: code, Stage: stage}})
		return
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: InteractionContractVersion, OK: true, Result: encoded})
}

func (handler *Handler) planInteraction(response http.ResponseWriter, request *http.Request) {
	if !strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		writeCommandResponse(response, http.StatusUnsupportedMediaType, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "UNSUPPORTED_MEDIA_TYPE"}})
		return
	}
	content, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxCommandRequestBytes))
	if err != nil {
		writeCommandResponse(response, http.StatusRequestEntityTooLarge, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "INVALID_INTERACTION_PLAN"}})
		return
	}
	var planRequest InteractionPlanRequest
	if err := decodePayload(content, &planRequest); err != nil {
		writeCommandResponse(response, http.StatusBadRequest, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "INVALID_INTERACTION_PLAN"}})
		return
	}
	planRequest = planRequest.withDefaults()
	if planRequest.Validate() != nil {
		writeCommandResponse(response, http.StatusBadRequest, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "INVALID_INTERACTION_PLAN"}})
		return
	}
	plan, err := handler.interactionPlanner.PlanInteraction(request.Context(), planRequest)
	if err != nil {
		writeCommandResponse(response, http.StatusUnprocessableEntity, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "INTERACTION_PLAN_FAILED"}})
		return
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: InteractionContractVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: InteractionContractVersion, OK: true, Result: encoded})
}

func (handler *Handler) inspectInteractions(response http.ResponseWriter, request *http.Request) {
	records, err := handler.interactionInspector.InspectInteractions(request.Context())
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: "INTERACTION_INSPECTION_FAILED", RecoveryRequired: true}})
		return
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: ContractVersion, OK: true, Result: encoded})
}

func (handler *Handler) inspectInteraction(response http.ResponseWriter, request *http.Request) {
	record, err := handler.interactionInspector.InspectInteraction(request.Context(), request.PathValue("session_id"))
	if err != nil {
		status, code := http.StatusInternalServerError, "INTERACTION_INSPECTION_FAILED"
		if errors.Is(err, interaction.ErrNotFound) {
			status, code = http.StatusNotFound, "INTERACTION_NOT_FOUND"
		} else if errors.Is(err, interaction.ErrInvalidSession) {
			status, code = http.StatusBadRequest, "INVALID_SESSION_ID"
		}
		writeCommandResponse(response, status, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: code, RecoveryRequired: status == http.StatusInternalServerError}})
		return
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: ContractVersion, OK: true, Result: encoded})
}

func (handler *Handler) inspectMetrics(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, handler.metricsInspector.InspectMetrics())
}

func (handler *Handler) inspectNotifications(response http.ResponseWriter, request *http.Request) {
	records, err := handler.notificationInspector.InspectNotifications(request.Context())
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: "NOTIFICATION_INSPECTION_FAILED", RecoveryRequired: true}})
		return
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: ContractVersion, OK: true, Result: encoded})
}

func (handler *Handler) inspectNotification(response http.ResponseWriter, request *http.Request) {
	record, err := handler.notificationInspector.InspectNotification(request.Context(), request.PathValue("event_id"))
	if err != nil {
		status, code := http.StatusInternalServerError, "NOTIFICATION_INSPECTION_FAILED"
		if errors.Is(err, notification.ErrNotFound) {
			status, code = http.StatusNotFound, "NOTIFICATION_NOT_FOUND"
		} else if errors.Is(err, notification.ErrInvalidRecord) {
			status, code = http.StatusBadRequest, "INVALID_EVENT_ID"
		}
		writeCommandResponse(response, status, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: code, RecoveryRequired: status == http.StatusInternalServerError}})
		return
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: ContractVersion, OK: true, Result: encoded})
}

func (handler *Handler) inspectSchedules(response http.ResponseWriter, request *http.Request) {
	records, err := handler.scheduleInspector.InspectSchedules(request.Context())
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: "SCHEDULE_INSPECTION_FAILED", RecoveryRequired: true}})
		return
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: ContractVersion, OK: true, Result: encoded})
}

func (handler *Handler) inspectSchedule(response http.ResponseWriter, request *http.Request) {
	record, err := handler.scheduleInspector.InspectSchedule(request.Context(), request.PathValue("schedule_id"))
	if err != nil {
		status, code := http.StatusInternalServerError, "SCHEDULE_INSPECTION_FAILED"
		if errors.Is(err, scheduler.ErrNotFound) {
			status, code = http.StatusNotFound, "SCHEDULE_NOT_FOUND"
		} else if errors.Is(err, scheduler.ErrInvalidSchedule) {
			status, code = http.StatusBadRequest, "INVALID_SCHEDULE_ID"
		}
		writeCommandResponse(response, status, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: code, RecoveryRequired: status == http.StatusInternalServerError}})
		return
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: ContractVersion, OK: true, Result: encoded})
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data:; object-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
	response.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if !handler.authorizeLocalRequest(response, request) {
		return
	}
	handler.mux.ServeHTTP(response, request)
}

func (handler *Handler) health(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (handler *Handler) ready(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]string{"status": "ready"})
}

func (handler *Handler) execute(response http.ResponseWriter, request *http.Request) {
	if !strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		writeCommandResponse(response, http.StatusUnsupportedMediaType, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: "UNSUPPORTED_MEDIA_TYPE"}})
		return
	}
	content, err := io.ReadAll(http.MaxBytesReader(response, request.Body, maxCommandRequestBytes))
	if err != nil {
		writeCommandResponse(response, http.StatusRequestEntityTooLarge, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: "INVALID_COMMAND"}})
		return
	}
	var command Command
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil || decoder.Decode(&struct{}{}) != io.EOF || command.Validate() != nil {
		writeCommandResponse(response, http.StatusBadRequest, Response{Version: ContractVersion, CommandID: command.CommandID, OK: false, Error: &CommandError{Code: "INVALID_COMMAND"}})
		return
	}
	if !command.Approved {
		writeCommandResponse(response, http.StatusForbidden, Response{Version: ContractVersion, CommandID: command.CommandID, OK: false, Error: &CommandError{Code: "APPROVAL_REQUIRED"}})
		return
	}
	if !publicBetaCommandAllowed(command.Operation) {
		writeCommandResponse(response, http.StatusForbidden, Response{Version: ContractVersion, CommandID: command.CommandID, OK: false, Error: &CommandError{Code: "OPERATION_NOT_AVAILABLE"}})
		return
	}
	if prefersAsync(request.Header.Get("Prefer")) {
		if !supportsAsyncOperation(command.Operation) {
			writeCommandResponse(response, http.StatusBadRequest, Response{Version: ContractVersion, CommandID: command.CommandID, OK: false, Error: &CommandError{Code: "ASYNC_OPERATION_UNSUPPORTED"}})
			return
		}
		if err := handler.asyncCommands.accept(command); err != nil {
			status, code := http.StatusBadRequest, "INVALID_COMMAND"
			switch {
			case errors.Is(err, errAsyncCapacity):
				status, code = http.StatusTooManyRequests, "ASYNC_CAPACITY_EXHAUSTED"
			case errors.Is(err, errAsyncStopping), errors.Is(err, errAsyncUnavailable):
				status, code = http.StatusServiceUnavailable, "ASYNC_EXECUTION_UNAVAILABLE"
			}
			writeCommandResponse(response, status, Response{Version: ContractVersion, CommandID: command.CommandID, OK: false, Error: &CommandError{Code: code}})
			return
		}
		statusURL := asyncStatusURL(command.CommandID)
		encoded, _ := json.Marshal(acceptedCommand{CommandID: command.CommandID, StatusURL: statusURL})
		response.Header().Set("Preference-Applied", "respond-async")
		response.Header().Set("Location", statusURL)
		writeCommandResponse(response, http.StatusAccepted, Response{Version: ContractVersion, CommandID: command.CommandID, OK: true, Result: encoded})
		return
	}
	result, commandErr := handler.executor.Execute(request.Context(), command)
	encoded, encodeErr := marshalResult(result)
	if encodeErr != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: ContractVersion, CommandID: command.CommandID, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	if commandErr != nil {
		status, mapped := mapCommandError(commandErr)
		writeCommandResponse(response, status, Response{Version: ContractVersion, CommandID: command.CommandID, OK: false, Result: encoded, Error: mapped})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: ContractVersion, CommandID: command.CommandID, OK: true, Result: encoded})
}

func supportsAsyncOperation(operation string) bool {
	return publicBetaCommandAllowed(operation)
}

func (handler *Handler) inspect(response http.ResponseWriter, request *http.Request) {
	commandID := request.PathValue("command_id")
	if commandledger.ValidateCommandID(commandID) != nil {
		writeCommandResponse(response, http.StatusBadRequest, Response{Version: ContractVersion, OK: false, Error: &CommandError{Code: "INVALID_COMMAND_ID"}})
		return
	}
	record, err := handler.inspector.Inspect(request.Context(), request.URL.Query().Get("scope"), request.URL.Query().Get("project"), commandID)
	if err != nil {
		status := http.StatusInternalServerError
		code := "COMMAND_INSPECTION_FAILED"
		if errors.Is(err, commandledger.ErrNotFound) {
			status, code = http.StatusNotFound, "COMMAND_NOT_FOUND"
		} else if errors.Is(err, ErrInvalidCommand) {
			status, code = http.StatusBadRequest, "INVALID_COMMAND_SCOPE"
		}
		writeCommandResponse(response, status, Response{Version: ContractVersion, CommandID: commandID, OK: false, Error: &CommandError{Code: code}})
		return
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		writeCommandResponse(response, http.StatusInternalServerError, Response{Version: ContractVersion, CommandID: commandID, OK: false, Error: &CommandError{Code: "RESULT_ENCODING_FAILED"}})
		return
	}
	writeCommandResponse(response, http.StatusOK, Response{Version: ContractVersion, CommandID: commandID, OK: true, Result: encoded})
}

func (handler *Handler) Shutdown(ctx context.Context) error {
	return handler.asyncCommands.shutdown(ctx)
}

func mapCommandError(err error) (int, *CommandError) {
	var recorded *workspaceprocess.RecordedCommandError
	switch {
	case errors.Is(err, ErrInvalidCommand):
		return http.StatusBadRequest, &CommandError{Code: "INVALID_COMMAND"}
	case errors.Is(err, ErrUnsupportedCommand):
		return http.StatusBadRequest, &CommandError{Code: "UNSUPPORTED_COMMAND"}
	case errors.Is(err, commandledger.ErrRequestConflict):
		return http.StatusConflict, &CommandError{Code: "COMMAND_ID_CONFLICT", Stage: "command_claim"}
	case errors.Is(err, commandledger.ErrInProgress):
		return http.StatusConflict, &CommandError{Code: "COMMAND_IN_PROGRESS", Stage: "command_claim", RecoveryRequired: true}
	case errors.Is(err, workspaceprocess.ErrCommandLedgerCommit):
		return http.StatusInternalServerError, &CommandError{Code: "COMMAND_LEDGER_PARTIAL", Stage: "command_outcome_commit", RecoveryRequired: true}
	case errors.Is(err, commandledger.ErrInvalidRecord):
		return http.StatusInternalServerError, &CommandError{Code: "COMMAND_LEDGER_INVALID", Stage: "command_claim", RecoveryRequired: true}
	case errors.As(err, &recorded):
		return http.StatusUnprocessableEntity, &CommandError{Code: recorded.Code, Stage: recorded.Stage, RecoveryRequired: recorded.Partial, Details: recorded.Envelope}
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, &CommandError{Code: "COMMAND_TIMEOUT", RecoveryRequired: true}
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout, &CommandError{Code: "COMMAND_CANCELED", RecoveryRequired: true}
	default:
		return http.StatusUnprocessableEntity, &CommandError{Code: "COMMAND_FAILED"}
	}
}

func writeCommandResponse(response http.ResponseWriter, status int, payload Response) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	encoder := json.NewEncoder(response)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}

type Server struct {
	server          *http.Server
	handlerShutdown func(context.Context) error
}

type handlerShutdowner interface {
	Shutdown(context.Context) error
}

func NewServer(address string, handler http.Handler) (*Server, error) {
	if strings.TrimSpace(address) == "" || handler == nil {
		return nil, errors.New("server address and handler are required")
	}
	if err := validateLoopbackAddress(address); err != nil {
		return nil, err
	}
	server := &Server{server: &http.Server{
		Addr: address, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}}
	if shutdowner, ok := handler.(handlerShutdowner); ok {
		server.handlerShutdown = shutdowner.Shutdown
	}
	return server, nil
}

func NewLocalNetworkServer(address string, handler http.Handler) (*Server, error) {
	if strings.TrimSpace(address) == "" || handler == nil {
		return nil, errors.New("server address and handler are required")
	}
	if err := validateLocalNetworkAddress(address); err != nil {
		return nil, err
	}
	server := &Server{server: &http.Server{
		Addr: address, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}}
	if shutdowner, ok := handler.(handlerShutdowner); ok {
		server.handlerShutdown = shutdowner.Shutdown
	}
	return server, nil
}

func validateLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("server address must be a loopback host and port")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("remote listen addresses require authentication, TLS, and authorization")
	}
	return nil
}

func validateLocalNetworkAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("server address must be a local IP and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
		return errors.New("mobile listen address must be loopback, private, or link-local")
	}
	return nil
}

func (server *Server) ListenAndServe() error { return server.server.ListenAndServe() }
func (server *Server) Serve(listener net.Listener) error {
	return server.server.Serve(listener)
}
func (server *Server) Shutdown(ctx context.Context) error {
	serverErr := server.server.Shutdown(ctx)
	if server.handlerShutdown != nil {
		return errors.Join(serverErr, server.handlerShutdown(ctx))
	}
	return serverErr
}
