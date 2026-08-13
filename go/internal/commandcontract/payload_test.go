package commandcontract

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWordPressActionFixtureUsesSchedulableStrictPayload(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "action", "wordpress_publish_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var command struct {
		Operation string          `json:"operation"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(content, &command); err != nil {
		t.Fatal(err)
	}
	if !Schedulable(command.Operation) || ValidatePayload(command.Operation, command.Payload) != nil {
		t.Fatalf("Action fixture is not a valid schedulable payload")
	}
	var fields map[string]any
	_ = json.Unmarshal(command.Payload, &fields)
	fields["application_password"] = "must-be-rejected"
	invalid, _ := json.Marshal(fields)
	if err := ValidatePayload(command.Operation, invalid); err != ErrInvalidPayload {
		t.Fatalf("credential field error = %v", err)
	}
}

func TestSchedulablePayloadRejectsUnknownAndSecretFields(t *testing.T) {
	valid := json.RawMessage(`{"project_id":"PROJECT-001","project_name":"P","current_time":"2026-08-10T09:30:00+09:00","max_tasks":10}`)
	if !Schedulable("workflow.execute") || ValidatePayload("workflow.execute", valid) != nil {
		t.Fatal("valid Workflow payload was rejected")
	}
	invalid := json.RawMessage(`{"project_id":"PROJECT-001","project_name":"P","current_time":"2026-08-10T09:30:00+09:00","max_tasks":10,"api_key":"secret"}`)
	if err := ValidatePayload("workflow.execute", invalid); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("unknown payload error = %v", err)
	}
	if Schedulable("schedule.create") || !errors.Is(ValidatePayload("schedule.create", json.RawMessage(`{}`)), ErrInvalidPayload) {
		t.Fatal("Scheduler control command became recursively schedulable")
	}
	if err := ValidatePayload("workflow.execute", json.RawMessage(`{}`)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("empty Workflow payload error = %v", err)
	}
}

func TestInteractionPayloadsAreStrictAndNotSchedulable(t *testing.T) {
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	requestDigest := "sha256:3c8f6dc8dde25e7cad6814e9ee01b8efabe7451719fafa18c84792eb35aa8bbe"
	cases := map[string]string{
		"interaction.start":                    `{"session_id":"SESSION-001","request":"Webアプリを作りたい","request_digest":"` + requestDigest + `","model":"Claude Sonnet 5","current_time":"2026-08-09T12:00:00Z"}`,
		"interaction.plan.generate":            `{"session_id":"SESSION-001","expected_version":1,"current_time":"2026-08-10T09:30:00+09:00"}`,
		"interaction.answer":                   `{"session_id":"SESSION-001","expected_version":2,"answers":[{"question":"Q","answer":"A"}],"current_time":"2026-08-10T09:30:00+09:00"}`,
		"interaction.plan.apply":               `{"session_id":"SESSION-001","expected_version":3,"project_id":"PROJECT-001","plan_digest":"` + digest + `","current_time":"2026-08-10T09:30:00+09:00"}`,
		"interaction.workflow.execute":         `{"session_id":"SESSION-001","expected_version":4,"reviewer_id":"QA-001","current_time":"2026-08-10T09:30:00+09:00","max_tasks":10,"autonomy_contract":{"schema_version":"workcairn-autonomy.v1","task_execution":"delegated","review":"required","revision":"delegated","external_publish":"separate_approval","spending":"forbidden","allowed_employee_ids":["PLAN-001","QA-001"],"allowed_models":["Claude Sonnet 5"],"execution_limit":10},"workflow_plan_digest":"` + digest + `"}`,
		"interaction.action.wordpress.publish": `{"session_id":"SESSION-001","expected_version":5,"task_id":"TASK-001","target_id":"site-main","current_time":"2026-08-10T09:30:00+09:00","action_plan_digest":"` + digest + `"}`,
	}
	for operation, payload := range cases {
		if Schedulable(operation) || ValidatePayload(operation, json.RawMessage(payload)) != nil {
			t.Fatalf("Interaction payload %s was rejected or became schedulable", operation)
		}
		var fields map[string]any
		_ = json.Unmarshal([]byte(payload), &fields)
		fields["api_key"] = "secret"
		invalid, _ := json.Marshal(fields)
		if err := ValidatePayload(operation, invalid); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("Interaction secret field %s error = %v", operation, err)
		}
	}
}

func TestWorkspaceSetupPayloadIsStrictAndNotSchedulable(t *testing.T) {
	valid := json.RawMessage(`{"current_time":"2026-08-13T15:00:00+09:00"}`)
	if Schedulable("workspace.setup") || ValidatePayload("workspace.setup", valid) != nil {
		t.Fatal("valid explicit Workspace setup payload was rejected or became schedulable")
	}
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"current_time":"2026-08-13T15:00:00+09:00","vault_path":"/private/example"}`),
		json.RawMessage(`{"current_time":"2026-08-13T15:00:00+09:00","api_key":"secret"}`),
	} {
		if err := ValidatePayload("workspace.setup", invalid); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("invalid Workspace setup payload error = %v", err)
		}
	}
}
