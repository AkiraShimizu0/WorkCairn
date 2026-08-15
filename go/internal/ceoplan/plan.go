// Package ceoplan owns the provider- and storage-neutral CEO plan contract.
package ceoplan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
	"golang.org/x/text/cases"
)

var (
	ErrInvalidRequest = errors.New("invalid CEO request")
	ErrInvalidOutput  = errors.New("invalid CEO plan output")
	ErrInvalidPlan    = errors.New("invalid CEO plan")
)

// ParseFailureReason is a sanitized, non-identifying classification of why a
// Runner's raw output failed the CEO Plan contract. It never carries raw
// Provider text, so it can be retained for diagnostics after the raw output
// itself is discarded. It classifies existing ParseRunnerOutput/
// NormalizeCandidate validation branches; it does not add new validation.
type ParseFailureReason string

const (
	ParseFailureJSONDecodeFailed     ParseFailureReason = "json_decode_failed"
	ParseFailureUnknownField         ParseFailureReason = "unknown_field"
	ParseFailureTrailingContent      ParseFailureReason = "trailing_content"
	ParseFailureObjectRequired       ParseFailureReason = "object_required"
	ParseFailureMissingRequiredField ParseFailureReason = "missing_required_field"
	ParseFailureInvalidProjectName   ParseFailureReason = "invalid_project_name"
	ParseFailureInvalidTaskShape     ParseFailureReason = "invalid_task_shape"
	ParseFailureInvalidAssignee      ParseFailureReason = "invalid_assignee"
	ParseFailureInvalidDependency    ParseFailureReason = "invalid_dependency"
	ParseFailureDependencyCycle      ParseFailureReason = "dependency_cycle"
)

// ParseError pairs ErrInvalidOutput/ErrInvalidPlan with a sanitized
// ParseFailureReason. The wrapped error keeps the existing human-readable
// text for diagnostics; the Reason is the stable, machine-classifiable
// field callers should persist.
type ParseError struct {
	Reason ParseFailureReason
	err    error
}

func (parseErr *ParseError) Error() string { return parseErr.err.Error() }
func (parseErr *ParseError) Unwrap() error { return parseErr.err }

func newParseError(reason ParseFailureReason, err error) *ParseError {
	return &ParseError{Reason: reason, err: err}
}

type ProposedTask struct {
	ProposalID    string   `json:"proposal_id"`
	Title         string   `json:"title"`
	RequiredRole  string   `json:"required_role,omitempty"`
	AssigneeID    *string  `json:"assignee_id"`
	DependencyIDs []string `json:"dependency_ids"`
	Rationale     string   `json:"rationale"`
}

type Plan struct {
	ProjectName               string         `json:"project_name"`
	Objective                 string         `json:"objective"`
	Summary                   string         `json:"summary"`
	RequiredDepartments       []string       `json:"required_departments"`
	RequiredRoles             []string       `json:"required_roles"`
	AssignedExistingEmployees []string       `json:"assigned_existing_employees"`
	MissingRoles              []string       `json:"missing_roles"`
	ProposedTasks             []ProposedTask `json:"proposed_tasks"`
	Risks                     []string       `json:"risks"`
	CEOQuestions              []string       `json:"ceo_questions"`
	PlanOnly                  bool           `json:"plan_only"`
}

type candidateTask struct {
	Title         string   `json:"title"`
	RequiredRole  string   `json:"required_role,omitempty"`
	AssigneeID    *string  `json:"assignee_id"`
	DependencyIDs []string `json:"dependency_ids"`
	Rationale     string   `json:"rationale"`
}

type candidatePlan struct {
	ProjectName               string          `json:"project_name"`
	Objective                 string          `json:"objective"`
	Summary                   string          `json:"summary"`
	RequiredDepartments       []string        `json:"required_departments"`
	RequiredRoles             []string        `json:"required_roles"`
	AssignedExistingEmployees []string        `json:"assigned_existing_employees"`
	ProposedTasks             []candidateTask `json:"proposed_tasks"`
	Risks                     []string        `json:"risks"`
	CEOQuestions              []string        `json:"ceo_questions"`
}

var proposalIDPattern = regexp.MustCompile(`^PROPOSED-\d+$`)
var roleCaseFolder = cases.Fold()

func ParseRunnerOutput(content string, employees []organization.Identity) (Plan, error) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var candidate candidatePlan
	if err := decoder.Decode(&candidate); err != nil {
		reason := ParseFailureJSONDecodeFailed
		if strings.Contains(err.Error(), "unknown field") {
			reason = ParseFailureUnknownField
		}
		return Plan{}, newParseError(reason, fmt.Errorf("%w: JSON object", ErrInvalidOutput))
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Plan{}, newParseError(ParseFailureTrailingContent, fmt.Errorf("%w: trailing data", ErrInvalidOutput))
	}
	if first := bytes.TrimSpace([]byte(content)); len(first) == 0 || first[0] != '{' {
		return Plan{}, newParseError(ParseFailureObjectRequired, fmt.Errorf("%w: object required", ErrInvalidOutput))
	}
	return NormalizeCandidate(candidate, employees)
}

func NormalizeCandidate(candidate candidatePlan, employees []organization.Identity) (Plan, error) {
	employeesByID, roles, err := employeeIndex(employees)
	if err != nil {
		return Plan{}, err
	}
	projectName, err := requiredText(candidate.ProjectName, "project_name")
	if err != nil || strings.ContainsAny(projectName, "/\\\r\n|") || projectName == "." || projectName == ".." {
		return Plan{}, newParseError(ParseFailureInvalidProjectName, fmt.Errorf("%w: project_name", ErrInvalidPlan))
	}
	objective, err := requiredText(candidate.Objective, "objective")
	if err != nil {
		return Plan{}, newParseError(ParseFailureMissingRequiredField, err)
	}
	// summary is optional (ADR-0046): planDescription() already tolerates
	// a blank summary (falls back to objective alone), so the canonical
	// shape validation must not reject it here either.
	summary := strings.TrimSpace(candidate.Summary)
	departments, err := requiredStringList(candidate.RequiredDepartments, "required_departments")
	if err != nil {
		return Plan{}, newParseError(ParseFailureMissingRequiredField, err)
	}
	requiredRoles, err := requiredStringList(candidate.RequiredRoles, "required_roles")
	if err != nil {
		return Plan{}, newParseError(ParseFailureMissingRequiredField, err)
	}
	assigned, err := knownEmployeeIDs(candidate.AssignedExistingEmployees, employeesByID)
	if err != nil {
		return Plan{}, newParseError(ParseFailureInvalidAssignee, err)
	}
	if len(candidate.ProposedTasks) == 0 {
		return Plan{}, newParseError(ParseFailureMissingRequiredField, fmt.Errorf("%w: proposed_tasks", ErrInvalidPlan))
	}
	tasks := make([]ProposedTask, 0, len(candidate.ProposedTasks))
	for index, candidateTask := range candidate.ProposedTasks {
		title, err := requiredText(candidateTask.Title, "task title")
		if err != nil || strings.ContainsAny(title, "\r\n|") {
			return Plan{}, newParseError(ParseFailureInvalidTaskShape, fmt.Errorf("%w: task title", ErrInvalidPlan))
		}
		rationale, err := requiredText(candidateTask.Rationale, "task rationale")
		if err != nil {
			return Plan{}, newParseError(ParseFailureMissingRequiredField, err)
		}
		assignment, err := organization.ResolveTaskAssignment(organization.AssignmentRequest{
			RequiredRole: candidateTask.RequiredRole, ProposedEmployeeID: candidateTask.AssigneeID,
		}, employees)
		if err != nil {
			return Plan{}, newParseError(ParseFailureInvalidAssignee, fmt.Errorf("%w: task assignment: %v", ErrInvalidPlan, err))
		}
		requiredRole := assignment.RequiredRole
		assignee := cloneString(assignment.EmployeeID)
		if assignee != nil && !containsString(assigned, *assignee) {
			assigned = append(assigned, *assignee)
		}
		if requiredRole != "" && !containsCanonicalRole(requiredRoles, requiredRole) {
			requiredRoles = append(requiredRoles, requiredRole)
		}
		dependencies, err := optionalStringList(candidateTask.DependencyIDs, "dependency_ids")
		if err != nil {
			return Plan{}, newParseError(ParseFailureInvalidDependency, err)
		}
		tasks = append(tasks, ProposedTask{
			ProposalID: fmt.Sprintf("PROPOSED-%03d", index+1), Title: title,
			RequiredRole: requiredRole, AssigneeID: assignee, DependencyIDs: dependencies, Rationale: rationale,
		})
	}
	if err := validateDependencyGraph(tasks); err != nil {
		return Plan{}, err
	}
	missingRoles := make([]string, 0)
	for _, role := range requiredRoles {
		if _, exists := roles[roleCaseFolder.String(role)]; !exists {
			missingRoles = append(missingRoles, role)
		}
	}
	risks, err := optionalStringList(candidate.Risks, "risks")
	if err != nil {
		return Plan{}, newParseError(ParseFailureMissingRequiredField, err)
	}
	questions, err := optionalStringList(candidate.CEOQuestions, "ceo_questions")
	if err != nil {
		return Plan{}, newParseError(ParseFailureMissingRequiredField, err)
	}
	return Plan{
		ProjectName: projectName, Objective: objective, Summary: summary,
		RequiredDepartments: departments, RequiredRoles: requiredRoles,
		AssignedExistingEmployees: assigned, MissingRoles: missingRoles,
		ProposedTasks: tasks, Risks: risks, CEOQuestions: questions, PlanOnly: true,
	}, nil
}

func ValidateApprovedPlan(plan Plan, employees []organization.Identity) (Plan, error) {
	if !plan.PlanOnly {
		return Plan{}, fmt.Errorf("%w: plan_only", ErrInvalidPlan)
	}
	candidate := candidatePlan{
		ProjectName: plan.ProjectName, Objective: plan.Objective, Summary: plan.Summary,
		RequiredDepartments: plan.RequiredDepartments, RequiredRoles: plan.RequiredRoles,
		AssignedExistingEmployees: plan.AssignedExistingEmployees,
		Risks:                     plan.Risks, CEOQuestions: plan.CEOQuestions,
		ProposedTasks: make([]candidateTask, 0, len(plan.ProposedTasks)),
	}
	for index, task := range plan.ProposedTasks {
		if task.ProposalID != fmt.Sprintf("PROPOSED-%03d", index+1) || !proposalIDPattern.MatchString(task.ProposalID) {
			return Plan{}, fmt.Errorf("%w: proposal ID", ErrInvalidPlan)
		}
		candidate.ProposedTasks = append(candidate.ProposedTasks, candidateTask{Title: task.Title, RequiredRole: task.RequiredRole, AssigneeID: task.AssigneeID, DependencyIDs: task.DependencyIDs, Rationale: task.Rationale})
	}
	validated, err := NormalizeCandidate(candidate, employees)
	if err != nil {
		return Plan{}, err
	}
	return validated, nil
}

func employeeIndex(employees []organization.Identity) (map[string]organization.Identity, map[string]struct{}, error) {
	byID := make(map[string]organization.Identity, len(employees))
	roles := make(map[string]struct{}, len(employees))
	for _, employee := range employees {
		id, err := requiredText(employee.ID, "Employee ID")
		if err != nil {
			return nil, nil, err
		}
		if _, exists := byID[id]; exists {
			return nil, nil, fmt.Errorf("%w: duplicate Employee ID %s", ErrInvalidPlan, id)
		}
		role, err := requiredText(employee.Role, "Employee role")
		if err != nil {
			return nil, nil, err
		}
		byID[id] = employee
		roles[roleCaseFolder.String(role)] = struct{}{}
	}
	return byID, roles, nil
}

func containsCanonicalRole(values []string, candidate string) bool {
	canonical := roleCaseFolder.String(candidate)
	for _, value := range values {
		if roleCaseFolder.String(value) == canonical {
			return true
		}
	}
	return false
}

func validateDependencyGraph(tasks []ProposedTask) error {
	graph := make(map[string][]string, len(tasks))
	for _, task := range tasks {
		graph[task.ProposalID] = task.DependencyIDs
	}
	for id, dependencies := range graph {
		seen := map[string]bool{}
		for _, dependencyID := range dependencies {
			if _, exists := graph[dependencyID]; !exists || dependencyID == id || seen[dependencyID] {
				return newParseError(ParseFailureInvalidDependency, fmt.Errorf("%w: dependency %s", ErrInvalidPlan, dependencyID))
			}
			seen[dependencyID] = true
		}
	}
	states := map[string]uint8{}
	var visit func(string) error
	visit = func(id string) error {
		if states[id] == 1 {
			return newParseError(ParseFailureDependencyCycle, fmt.Errorf("%w: cyclic dependencies", ErrInvalidPlan))
		}
		if states[id] == 2 {
			return nil
		}
		states[id] = 1
		for _, dependencyID := range graph[id] {
			if err := visit(dependencyID); err != nil {
				return err
			}
		}
		states[id] = 2
		return nil
	}
	for id := range graph {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func requiredText(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%w: %s", ErrInvalidPlan, field)
	}
	return value, nil
}

func requiredStringList(values []string, field string) ([]string, error) {
	if values == nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidPlan, field)
	}
	return optionalStringList(values, field)
}

func optionalStringList(values []string, field string) ([]string, error) {
	if values == nil {
		return []string{}, nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized, err := requiredText(value, field)
		if err != nil {
			return nil, err
		}
		result = append(result, normalized)
	}
	return result, nil
}

func knownEmployeeIDs(values []string, employees map[string]organization.Identity) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, value := range values {
		id := strings.TrimSpace(value)
		if _, exists := employees[id]; !exists || id == "" {
			return nil, fmt.Errorf("%w: unknown Employee %s", ErrInvalidPlan, id)
		}
		if !containsString(result, id) {
			result = append(result, id)
		}
	}
	return result, nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
