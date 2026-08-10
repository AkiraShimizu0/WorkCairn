package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/kernel"
)

type fakeCommandExecutor struct {
	commands []kernel.Command
}

func (executor *fakeCommandExecutor) HandleCommand(command kernel.Command) (kernel.CommandResult, error) {
	executor.commands = append(executor.commands, command)
	return kernel.CommandResult{Type: command.Type, Data: map[string]string{"operation": command.Type}}, nil
}

type contractFixture struct {
	Cases []contractCase `json:"cases"`
}

type contractCase struct {
	Name     string          `json:"name"`
	Request  json.RawMessage `json:"request"`
	RawInput *string         `json:"raw_input"`
	Expected json.RawMessage `json:"expected"`
}

func TestContractFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", "fixtures", "go_core", "contract_cases.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture contractFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}

	for _, testCase := range fixture.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			input := []byte(testCase.Request)
			if testCase.RawInput != nil {
				input = []byte(*testCase.RawInput)
			}
			var output bytes.Buffer
			run(bytes.NewReader(input), &output)
			assertJSONEqual(t, testCase.Expected, output.Bytes())
		})
	}
}

func TestOversizedInput(t *testing.T) {
	var output bytes.Buffer
	run(bytes.NewReader(bytes.Repeat([]byte("x"), maxRequestBytes+1)), &output)
	var actual response
	if err := json.Unmarshal(output.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	if actual.OK || actual.Error == nil || actual.Error.Code != "INVALID_REQUEST" {
		t.Fatalf("unexpected response: %#v", actual)
	}
}

func TestProjectAndWorkflowOperationsUseCommandExecutor(t *testing.T) {
	executor := &fakeCommandExecutor{}
	operations := []string{
		kernel.CommandProjectNextTaskID,
		kernel.CommandProjectValidate,
		kernel.CommandProjectTransition,
		kernel.CommandWorkflowReadiness,
	}
	for _, operation := range operations {
		response := dispatch(request{
			Version:   contractVersion,
			Operation: operation,
			Payload:   json.RawMessage(`{}`),
		}, executor)
		if !response.OK {
			t.Fatalf("dispatch(%s) response = %#v", operation, response)
		}
	}
	if len(executor.commands) != len(operations) {
		t.Fatalf("HandleCommand() calls = %d, want %d", len(executor.commands), len(operations))
	}
	for index, command := range executor.commands {
		if command.Type != operations[index] {
			t.Fatalf("command[%d].Type = %s, want %s", index, command.Type, operations[index])
		}
	}
}

func assertJSONEqual(t *testing.T, expectedJSON, actualJSON []byte) {
	t.Helper()
	var expected any
	var actual any
	if err := json.Unmarshal(expectedJSON, &expected); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(actualJSON, &actual); err != nil {
		t.Fatalf("invalid CLI response: %v: %s", err, actualJSON)
	}
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("expected %s, got %s", expectedJSON, actualJSON)
	}
}
