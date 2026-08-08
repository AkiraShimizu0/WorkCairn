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

	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
	workspaceprocess "github.com/AkiraShimizu0/workspace-os/go/internal/process"
)

const maxCommandRequestBytes = 2 << 20

type Handler struct {
	executor  Executor
	inspector Inspector
	mux       *http.ServeMux
}

func NewHandler(executor Executor, inspector Inspector) (*Handler, error) {
	if executor == nil || inspector == nil {
		return nil, errors.New("Command executor and inspector are required")
	}
	handler := &Handler{executor: executor, inspector: inspector, mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /healthz", handler.health)
	handler.mux.HandleFunc("GET /readyz", handler.ready)
	handler.mux.HandleFunc("POST /v1/commands", handler.execute)
	handler.mux.HandleFunc("GET /v1/commands/{command_id}", handler.inspect)
	return handler, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
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
		return http.StatusUnprocessableEntity, &CommandError{Code: recorded.Code, Stage: recorded.Stage, RecoveryRequired: recorded.Partial}
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
	response.WriteHeader(status)
	encoder := json.NewEncoder(response)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}

type Server struct{ server *http.Server }

func NewServer(address string, handler http.Handler) (*Server, error) {
	if strings.TrimSpace(address) == "" || handler == nil {
		return nil, errors.New("server address and handler are required")
	}
	return &Server{server: &http.Server{
		Addr: address, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}}, nil
}

func (server *Server) ListenAndServe() error { return server.server.ListenAndServe() }
func (server *Server) Serve(listener net.Listener) error {
	return server.server.Serve(listener)
}
func (server *Server) Shutdown(ctx context.Context) error {
	return server.server.Shutdown(ctx)
}
