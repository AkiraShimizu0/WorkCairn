package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/AkiraShimizu0/workspace-os/go/internal/project"
	"github.com/AkiraShimizu0/workspace-os/go/internal/workflow"
)

const (
	contractVersion = "v1"
	maxRequestBytes = 1 << 20
)

type request struct {
	Version   string          `json:"version"`
	Operation string          `json:"operation"`
	Payload   json.RawMessage `json:"payload"`
}

type response struct {
	Version string         `json:"version"`
	OK      bool           `json:"ok"`
	Result  any            `json:"result"`
	Error   *responseError `json:"error"`
}

type responseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type nextTaskIDPayload struct {
	ExistingIDs []string `json:"existing_ids"`
}

type validateTaskPayload struct {
	Task project.Task `json:"task"`
}

type transitionPayload struct {
	Current string `json:"current"`
	Target  string `json:"target"`
}

type readinessPayload struct {
	Tasks             []workflow.Task       `json:"tasks"`
	Dependencies      []workflow.Dependency `json:"dependencies"`
	ExistingEmployees []string              `json:"existing_employee_ids"`
}

func main() {
	os.Exit(run(os.Stdin, os.Stdout))
}

func run(input io.Reader, output io.Writer) (exitCode int) {
	written := false
	defer func() {
		if recover() != nil && !written {
			_ = writeResponse(output, failure("INTERNAL_ERROR", "internal error"))
			exitCode = 1
		}
	}()

	data, err := io.ReadAll(io.LimitReader(input, maxRequestBytes+1))
	if err != nil {
		_ = writeResponse(output, failure("INVALID_REQUEST", "unable to read request"))
		return 1
	}
	if len(data) > maxRequestBytes {
		_ = writeResponse(output, failure("INVALID_REQUEST", "request exceeds size limit"))
		return 1
	}

	var req request
	if err := decodeStrict(data, &req); err != nil {
		_ = writeResponse(output, failure("INVALID_REQUEST", "request must be valid JSON"))
		return 1
	}
	if err := writeResponse(output, dispatch(req)); err != nil {
		return 1
	}
	written = true
	return 0
}

func dispatch(req request) response {
	if req.Version != contractVersion {
		return failure("UNSUPPORTED_VERSION", "contract version is not supported")
	}
	if req.Operation == "" || len(req.Payload) == 0 {
		return failure("INVALID_REQUEST", "operation and payload are required")
	}

	switch req.Operation {
	case "project.next_task_id":
		var payload nextTaskIDPayload
		if err := decodeStrict(req.Payload, &payload); err != nil {
			return failure("INVALID_REQUEST", "invalid project.next_task_id payload")
		}
		taskID, err := project.NextTaskID(payload.ExistingIDs)
		if err != nil {
			return domainFailure(err)
		}
		return success(map[string]string{"task_id": taskID})
	case "project.validate_task":
		var payload validateTaskPayload
		if err := decodeStrict(req.Payload, &payload); err != nil {
			return failure("INVALID_REQUEST", "invalid project.validate_task payload")
		}
		if err := project.ValidateTask(payload.Task); err != nil {
			return domainFailure(err)
		}
		return success(map[string]bool{"valid": true})
	case "project.can_transition":
		var payload transitionPayload
		if err := decodeStrict(req.Payload, &payload); err != nil {
			return failure("INVALID_REQUEST", "invalid project.can_transition payload")
		}
		current, err := project.ParseStatus(payload.Current)
		if err != nil {
			return domainFailure(err)
		}
		target, err := project.ParseStatus(payload.Target)
		if err != nil {
			return domainFailure(err)
		}
		if err := project.ValidateTransition(current, target); err != nil {
			return domainFailure(err)
		}
		return success(map[string]bool{"allowed": true})
	case "workflow.readiness":
		var payload readinessPayload
		if err := decodeStrict(req.Payload, &payload); err != nil {
			return failure("INVALID_REQUEST", "invalid workflow.readiness payload")
		}
		employees := make(map[string]bool, len(payload.ExistingEmployees))
		for _, employeeID := range payload.ExistingEmployees {
			employees[employeeID] = true
		}
		result, err := workflow.EvaluateReadiness(payload.Tasks, payload.Dependencies, employees)
		if err != nil {
			return domainFailure(err)
		}
		return success(result)
	default:
		return failure("UNKNOWN_OPERATION", "operation is not supported")
	}
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func success(result any) response {
	return response{Version: contractVersion, OK: true, Result: result}
}

func failure(code, message string) response {
	return response{Version: contractVersion, OK: false, Error: &responseError{Code: code, Message: message}}
}

func domainFailure(err error) response {
	code := "INVALID_REQUEST"
	switch {
	case errors.Is(err, project.ErrInvalidTaskID):
		code = "INVALID_TASK_ID"
	case errors.Is(err, project.ErrDuplicateTaskID), errors.Is(err, workflow.ErrDuplicateTaskID):
		code = "DUPLICATE_TASK_ID"
	case errors.Is(err, project.ErrInvalidStatus):
		code = "INVALID_STATUS"
	case errors.Is(err, project.ErrInvalidTransition):
		code = "INVALID_TRANSITION"
	case errors.Is(err, project.ErrInvalidTaskTitle):
		code = "INVALID_TASK_TITLE"
	case errors.Is(err, project.ErrInvalidAssigneeID):
		code = "INVALID_ASSIGNEE_ID"
	case errors.Is(err, workflow.ErrUnknownDependency):
		code = "UNKNOWN_DEPENDENCY"
	case errors.Is(err, workflow.ErrCyclicDependency):
		code = "CYCLIC_DEPENDENCY"
	}
	return failure(code, safeMessage(code))
}

func safeMessage(code string) string {
	messages := map[string]string{
		"INVALID_TASK_ID": "task ID is invalid", "DUPLICATE_TASK_ID": "task ID is duplicated",
		"INVALID_STATUS": "task status is invalid", "INVALID_TRANSITION": "task status transition is invalid",
		"INVALID_TASK_TITLE": "task title is invalid", "INVALID_ASSIGNEE_ID": "assignee ID is invalid",
		"UNKNOWN_DEPENDENCY": "dependency references an unknown task", "CYCLIC_DEPENDENCY": "dependency graph contains a cycle",
		"INVALID_REQUEST": "request is invalid",
	}
	if message, ok := messages[code]; ok {
		return message
	}
	return "request could not be processed"
}

func writeResponse(output io.Writer, resp response) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(resp)
}
