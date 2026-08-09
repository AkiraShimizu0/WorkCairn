// Package commandcontract owns the transport-neutral typed payload surface for
// product side-effect commands shared by HTTP and Scheduler Adapters.
package commandcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/action"
	"github.com/AkiraShimizu0/workspace-os/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workspace-os/go/internal/interaction"
	"github.com/AkiraShimizu0/workspace-os/go/internal/organization"
	"github.com/AkiraShimizu0/workspace-os/go/internal/project"
)

var ErrInvalidPayload = errors.New("invalid product command payload")

func Schedulable(operation string) bool {
	switch operation {
	case "task.execute", "review.execute", "revision.execute", "workflow.execute", "workflow.reviewed.execute",
		"ceo_plan.apply", "project.bootstrap", "task.create", "project.dependencies.create",
		"organization.employee_hire", "organization.employee_rename", "organization.employee_id_repair", "organization.sync",
		"action.wordpress.publish":
		return true
	default:
		return false
	}
}

func ValidatePayload(operation string, content json.RawMessage) error {
	if err := validateRequiredFields(operation, content); err != nil {
		return err
	}
	var target any
	switch operation {
	case "task.execute":
		target = &struct {
			ProjectID         string    `json:"project_id"`
			ProjectName       string    `json:"project_name"`
			TaskID            string    `json:"task_id"`
			CurrentTime       time.Time `json:"current_time"`
			ApprovalReference string    `json:"approval_reference,omitempty"`
			ExecutionID       string    `json:"execution_id,omitempty"`
		}{}
	case "review.execute":
		target = &struct {
			ProjectID     string    `json:"project_id"`
			ProjectName   string    `json:"project_name"`
			TaskID        string    `json:"task_id"`
			ReviewerID    string    `json:"reviewer_id"`
			ReviewVersion string    `json:"review_version,omitempty"`
			CurrentTime   time.Time `json:"current_time"`
		}{}
	case "revision.execute":
		target = &struct {
			ProjectID     string    `json:"project_id"`
			ProjectName   string    `json:"project_name"`
			SourceTaskID  string    `json:"source_task_id"`
			ReviewVersion string    `json:"review_version,omitempty"`
			CurrentTime   time.Time `json:"current_time"`
		}{}
	case "workflow.execute":
		target = &struct {
			ProjectID         string    `json:"project_id"`
			ProjectName       string    `json:"project_name"`
			CurrentTime       time.Time `json:"current_time"`
			ApprovalReference string    `json:"approval_reference,omitempty"`
			MaxTasks          int       `json:"max_tasks"`
		}{}
	case "workflow.reviewed.execute":
		target = &struct {
			ProjectID         string    `json:"project_id"`
			ProjectName       string    `json:"project_name"`
			ReviewerID        string    `json:"reviewer_id"`
			CurrentTime       time.Time `json:"current_time"`
			ApprovalReference string    `json:"approval_reference,omitempty"`
			MaxTasks          int       `json:"max_tasks"`
		}{}
	case "ceo_plan.apply":
		target = &struct {
			ProjectID   string       `json:"project_id"`
			Plan        ceoplan.Plan `json:"plan"`
			CurrentTime time.Time    `json:"current_time"`
		}{}
	case "project.bootstrap":
		target = &struct {
			ProjectID   string    `json:"project_id"`
			ProjectName string    `json:"project_name"`
			Description string    `json:"description,omitempty"`
			CurrentTime time.Time `json:"current_time"`
		}{}
	case "task.create":
		target = &struct {
			ProjectName string    `json:"project_name"`
			Title       string    `json:"title"`
			AssigneeID  *string   `json:"assignee_id,omitempty"`
			CurrentTime time.Time `json:"current_time"`
		}{}
	case "project.dependencies.create":
		target = &struct {
			ProjectName string                   `json:"project_name"`
			Rows        []project.TaskDependency `json:"rows"`
			CurrentTime time.Time                `json:"current_time"`
		}{}
	case "organization.employee_hire":
		target = &struct {
			Candidate   organization.EmployeeCandidate `json:"candidate"`
			CurrentTime time.Time                      `json:"current_time"`
		}{}
	case "organization.employee_rename":
		target = &struct {
			Request     organization.RenameRequest `json:"request"`
			CurrentTime time.Time                  `json:"current_time"`
		}{}
	case "organization.employee_id_repair":
		target = &struct {
			Expected    []organization.IDRepair `json:"expected"`
			CurrentTime time.Time               `json:"current_time"`
		}{}
	case "organization.sync":
		target = &struct {
			CurrentTime time.Time `json:"current_time"`
		}{}
	case "action.wordpress.publish":
		target = &struct {
			ProjectID    string    `json:"project_id"`
			ProjectName  string    `json:"project_name"`
			TaskID       string    `json:"task_id"`
			TargetID     string    `json:"target_id"`
			CurrentTime  time.Time `json:"current_time"`
			SourceSHA256 string    `json:"source_sha256"`
		}{}
	case "interaction.start":
		target = &struct {
			SessionID     string    `json:"session_id"`
			Request       string    `json:"request"`
			RequestDigest string    `json:"request_digest"`
			Model         string    `json:"model"`
			CurrentTime   time.Time `json:"current_time"`
		}{}
	case "interaction.plan.generate":
		target = &struct {
			SessionID       string    `json:"session_id"`
			ExpectedVersion uint64    `json:"expected_version"`
			CurrentTime     time.Time `json:"current_time"`
		}{}
	case "interaction.answer":
		target = &struct {
			SessionID       string               `json:"session_id"`
			ExpectedVersion uint64               `json:"expected_version"`
			Answers         []interaction.Answer `json:"answers"`
			CurrentTime     time.Time            `json:"current_time"`
		}{}
	case "interaction.plan.apply":
		target = &struct {
			SessionID       string    `json:"session_id"`
			ExpectedVersion uint64    `json:"expected_version"`
			ProjectID       string    `json:"project_id"`
			PlanDigest      string    `json:"plan_digest"`
			CurrentTime     time.Time `json:"current_time"`
		}{}
	case "interaction.workflow.execute":
		target = &struct {
			SessionID          string    `json:"session_id"`
			ExpectedVersion    uint64    `json:"expected_version"`
			ReviewerID         string    `json:"reviewer_id"`
			CurrentTime        time.Time `json:"current_time"`
			ApprovalReference  string    `json:"approval_reference,omitempty"`
			MaxTasks           int       `json:"max_tasks"`
			WorkflowPlanDigest string    `json:"workflow_plan_digest"`
		}{}
	default:
		return ErrInvalidPayload
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalidPayload
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidPayload
	}
	return nil
}

func validateRequiredFields(operation string, content json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(content, &fields); err != nil || fields == nil {
		return ErrInvalidPayload
	}
	requiredStrings := map[string][]string{
		"task.execute":                    {"project_id", "project_name", "task_id"},
		"review.execute":                  {"project_id", "project_name", "task_id", "reviewer_id"},
		"revision.execute":                {"project_id", "project_name", "source_task_id"},
		"workflow.execute":                {"project_id", "project_name"},
		"workflow.reviewed.execute":       {"project_id", "project_name", "reviewer_id"},
		"ceo_plan.apply":                  {"project_id"},
		"project.bootstrap":               {"project_id", "project_name"},
		"task.create":                     {"project_name", "title"},
		"project.dependencies.create":     {"project_name"},
		"organization.employee_hire":      {},
		"organization.employee_rename":    {},
		"organization.employee_id_repair": {},
		"organization.sync":               {},
		"action.wordpress.publish":        {"project_id", "project_name", "task_id", "target_id", "source_sha256"},
		"interaction.start":               {"session_id", "request", "request_digest", "model"},
		"interaction.plan.generate":       {"session_id"},
		"interaction.answer":              {"session_id"},
		"interaction.plan.apply":          {"session_id", "project_id", "plan_digest"},
		"interaction.workflow.execute":    {"session_id", "reviewer_id", "workflow_plan_digest"},
	}
	stringsRequired, supported := requiredStrings[operation]
	if !supported {
		return ErrInvalidPayload
	}
	for _, name := range stringsRequired {
		var value string
		if raw, ok := fields[name]; !ok || json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
			return ErrInvalidPayload
		}
	}
	var currentTime time.Time
	if raw, ok := fields["current_time"]; !ok || json.Unmarshal(raw, &currentTime) != nil || currentTime.IsZero() {
		return ErrInvalidPayload
	}
	requiredValues := map[string][]string{
		"ceo_plan.apply":                  {"plan"},
		"project.dependencies.create":     {"rows"},
		"organization.employee_hire":      {"candidate"},
		"organization.employee_rename":    {"request"},
		"organization.employee_id_repair": {"expected"},
		"interaction.answer":              {"answers"},
	}
	for _, name := range requiredValues[operation] {
		raw, ok := fields[name]
		if !ok || len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
			return ErrInvalidPayload
		}
	}
	if operation == "workflow.execute" || operation == "workflow.reviewed.execute" {
		var maxTasks int
		if raw, ok := fields["max_tasks"]; !ok || json.Unmarshal(raw, &maxTasks) != nil || maxTasks < 1 || maxTasks > 100 {
			return ErrInvalidPayload
		}
	}
	if operation == "action.wordpress.publish" {
		var digest string
		if json.Unmarshal(fields["source_sha256"], &digest) != nil || action.ValidateSourceDigest(digest) != nil {
			return ErrInvalidPayload
		}
	}
	if strings.HasPrefix(operation, "interaction.") {
		var sessionID string
		if json.Unmarshal(fields["session_id"], &sessionID) != nil || interaction.ValidateSessionID(sessionID) != nil {
			return ErrInvalidPayload
		}
	}
	if operation == "interaction.start" {
		var sessionID, request, digest, model string
		var currentTime time.Time
		_ = json.Unmarshal(fields["session_id"], &sessionID)
		_ = json.Unmarshal(fields["request"], &request)
		_ = json.Unmarshal(fields["request_digest"], &digest)
		_ = json.Unmarshal(fields["model"], &model)
		_ = json.Unmarshal(fields["current_time"], &currentTime)
		record, err := interaction.New(sessionID, request, model, currentTime)
		if err != nil || record.RequestDigest != digest {
			return ErrInvalidPayload
		}
	}
	if operation == "interaction.plan.generate" || operation == "interaction.answer" || operation == "interaction.plan.apply" || operation == "interaction.workflow.execute" {
		var expected uint64
		if raw, ok := fields["expected_version"]; !ok || json.Unmarshal(raw, &expected) != nil || expected == 0 {
			return ErrInvalidPayload
		}
	}
	if operation == "interaction.plan.apply" {
		var digest string
		if json.Unmarshal(fields["plan_digest"], &digest) != nil || interaction.ValidateDigest(digest) != nil {
			return ErrInvalidPayload
		}
	}
	if operation == "interaction.answer" {
		var answers []interaction.Answer
		if json.Unmarshal(fields["answers"], &answers) != nil || interaction.ValidateAnswerPayload(answers) != nil {
			return ErrInvalidPayload
		}
	}
	if operation == "interaction.workflow.execute" {
		var digest string
		var maxTasks int
		if json.Unmarshal(fields["workflow_plan_digest"], &digest) != nil || interaction.ValidateDigest(digest) != nil ||
			json.Unmarshal(fields["max_tasks"], &maxTasks) != nil || maxTasks <= 0 || maxTasks > 100 {
			return ErrInvalidPayload
		}
	}
	return nil
}
