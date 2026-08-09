package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/AkiraShimizu0/workspace-os/go/internal/bootstrap"
	"github.com/AkiraShimizu0/workspace-os/go/internal/buildinfo"
	"github.com/AkiraShimizu0/workspace-os/go/internal/kernel"
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

type commandExecutor interface {
	HandleCommand(command kernel.Command) (kernel.CommandResult, error)
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		if err := json.NewEncoder(os.Stdout).Encode(buildinfo.Current()); err != nil {
			os.Exit(1)
		}
		return
	}
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
	workspaceKernel, err := bootstrap.NewDefaultKernel(bootstrap.DefaultKernelVersion)
	if err != nil {
		_ = writeResponse(output, failure("INTERNAL_ERROR", "internal error"))
		return 1
	}
	if err := workspaceKernel.Start(); err != nil {
		_ = writeResponse(output, failure("INTERNAL_ERROR", "internal error"))
		return 1
	}
	defer func() { _ = workspaceKernel.Stop() }()
	if err := writeResponse(output, dispatch(req, workspaceKernel)); err != nil {
		return 1
	}
	written = true
	return 0
}

func dispatch(req request, executor commandExecutor) response {
	if req.Version != contractVersion {
		return failure("UNSUPPORTED_VERSION", "contract version is not supported")
	}
	if req.Operation == "" || len(req.Payload) == 0 {
		return failure("INVALID_REQUEST", "operation and payload are required")
	}

	result, err := executor.HandleCommand(kernel.Command{
		Type:    req.Operation,
		Payload: req.Payload,
	})
	if err != nil {
		return kernelFailure(err)
	}
	return success(result.Data)
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

func kernelFailure(err error) response {
	var commandError *kernel.CommandError
	if !errors.As(err, &commandError) {
		return failure("INTERNAL_ERROR", safeMessage("INTERNAL_ERROR"))
	}
	code := string(commandError.Kind)
	return failure(code, safeMessage(code))
}

func safeMessage(code string) string {
	messages := map[string]string{
		"INVALID_TASK_ID": "task ID is invalid", "DUPLICATE_TASK_ID": "task ID is duplicated",
		"INVALID_STATUS": "task status is invalid", "INVALID_TRANSITION": "task status transition is invalid",
		"INVALID_TASK_TITLE": "task title is invalid", "INVALID_ASSIGNEE_ID": "assignee ID is invalid",
		"UNKNOWN_DEPENDENCY": "dependency references an unknown task", "CYCLIC_DEPENDENCY": "dependency graph contains a cycle",
		"INVALID_REQUEST": "request is invalid", "UNKNOWN_OPERATION": "operation is not supported",
		"TASK_NOT_READY": "task is not ready", "KERNEL_NOT_STARTED": "workspace kernel is not started",
		"SERVICE_NOT_REGISTERED": "required kernel service is not registered", "INTERNAL_ERROR": "internal error",
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
