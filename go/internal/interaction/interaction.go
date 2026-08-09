// Package interaction defines storage- and provider-neutral state for a
// single-user request, clarification, and approval session.
package interaction

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
)

const SchemaVersion = 1

var (
	ErrInvalidSession  = errors.New("invalid interaction session")
	ErrAlreadyExists   = errors.New("interaction session already exists")
	ErrNotFound        = errors.New("interaction session not found")
	ErrVersionConflict = errors.New("interaction session version conflict")
	ErrInvalidState    = errors.New("invalid interaction session state")
)

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type State string

const (
	StatePlanGenerationApprovalRequired State = "plan_generation_approval_required"
	StateClarificationRequired          State = "clarification_required"
	StatePlanApprovalRequired           State = "plan_approval_required"
	StateReadyToExecute                 State = "ready_to_execute"
)

func (state State) Valid() bool {
	return state == StatePlanGenerationApprovalRequired || state == StateClarificationRequired ||
		state == StatePlanApprovalRequired || state == StateReadyToExecute
}

type TurnKind string

const (
	TurnPlanGenerated         TurnKind = "plan_generated"
	TurnClarificationAnswered TurnKind = "clarification_answered"
	TurnPlanApplied           TurnKind = "plan_applied"
)

type Answer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type Turn struct {
	Kind        TurnKind      `json:"kind"`
	At          time.Time     `json:"at"`
	Plan        *ceoplan.Plan `json:"plan,omitempty"`
	PlanDigest  string        `json:"plan_digest,omitempty"`
	Answers     []Answer      `json:"answers,omitempty"`
	ProjectID   string        `json:"project_id,omitempty"`
	ProjectName string        `json:"project_name,omitempty"`
}

type Record struct {
	SchemaVersion int       `json:"schema_version"`
	SessionID     string    `json:"session_id"`
	Request       string    `json:"request"`
	RequestDigest string    `json:"request_digest"`
	Model         string    `json:"model"`
	CreatedAt     time.Time `json:"created_at"`
	State         State     `json:"state"`
	Version       uint64    `json:"version"`
	Turns         []Turn    `json:"turns"`
}

func New(sessionID, request, model string, createdAt time.Time) (Record, error) {
	sessionID, request, model = strings.TrimSpace(sessionID), strings.TrimSpace(request), strings.TrimSpace(model)
	if !sessionIDPattern.MatchString(sessionID) || request == "" || len(request) > 32<<10 || model == "" ||
		createdAt.IsZero() || strings.ContainsAny(model, "\r\n") {
		return Record{}, ErrInvalidSession
	}
	digest, err := requestDigest(sessionID, request, model, createdAt)
	if err != nil {
		return Record{}, err
	}
	record := Record{
		SchemaVersion: SchemaVersion, SessionID: sessionID, Request: request, RequestDigest: digest,
		Model: model, CreatedAt: createdAt, State: StatePlanGenerationApprovalRequired, Version: 1, Turns: []Turn{},
	}
	return record, record.Validate()
}

func ValidateSessionID(sessionID string) error {
	if !sessionIDPattern.MatchString(strings.TrimSpace(sessionID)) {
		return ErrInvalidSession
	}
	return nil
}

func ValidateDigest(value string) error {
	if !validDigest(strings.TrimSpace(value)) {
		return ErrInvalidSession
	}
	return nil
}

func ValidateAnswerPayload(answers []Answer) error {
	if len(answers) == 0 {
		return ErrInvalidSession
	}
	seen := make(map[string]bool, len(answers))
	for _, answer := range answers {
		if strings.TrimSpace(answer.Question) == "" || answer.Question != strings.TrimSpace(answer.Question) ||
			strings.TrimSpace(answer.Answer) == "" || len(answer.Answer) > 16<<10 || seen[answer.Question] {
			return ErrInvalidSession
		}
		seen[answer.Question] = true
	}
	return nil
}

func (record Record) Validate() error {
	expectedRequestDigest, digestErr := requestDigest(record.SessionID, record.Request, record.Model, record.CreatedAt)
	if record.SchemaVersion != SchemaVersion || !sessionIDPattern.MatchString(record.SessionID) ||
		record.Request == "" || record.Request != strings.TrimSpace(record.Request) || len(record.Request) > 32<<10 ||
		record.Model == "" || record.Model != strings.TrimSpace(record.Model) || strings.ContainsAny(record.Model, "\r\n") ||
		record.CreatedAt.IsZero() || !validDigest(record.RequestDigest) || digestErr != nil || record.RequestDigest != expectedRequestDigest ||
		!record.State.Valid() || record.Version != uint64(len(record.Turns))+1 || record.Turns == nil {
		return ErrInvalidSession
	}
	state := StatePlanGenerationApprovalRequired
	lastAt := record.CreatedAt
	var activePlan *ceoplan.Plan
	activeDigest := ""
	for _, turn := range record.Turns {
		if turn.At.IsZero() || turn.At.Before(lastAt) {
			return ErrInvalidSession
		}
		lastAt = turn.At
		switch turn.Kind {
		case TurnPlanGenerated:
			if state != StatePlanGenerationApprovalRequired || turn.Plan == nil || len(turn.Answers) != 0 ||
				turn.ProjectID != "" || turn.ProjectName != "" || validatePlanShape(*turn.Plan) != nil {
				return ErrInvalidSession
			}
			digest, err := DigestPlan(*turn.Plan)
			if err != nil || turn.PlanDigest != digest {
				return ErrInvalidSession
			}
			plan := clonePlan(*turn.Plan)
			activePlan, activeDigest = &plan, digest
			state = StatePlanApprovalRequired
			if len(plan.CEOQuestions) > 0 {
				state = StateClarificationRequired
			}
		case TurnClarificationAnswered:
			if state != StateClarificationRequired || turn.Plan != nil || turn.PlanDigest != activeDigest ||
				turn.ProjectID != "" || turn.ProjectName != "" || activePlan == nil || validateAnswers(activePlan.CEOQuestions, turn.Answers) != nil {
				return ErrInvalidSession
			}
			state = StatePlanGenerationApprovalRequired
		case TurnPlanApplied:
			if state != StatePlanApprovalRequired || turn.Plan != nil || len(turn.Answers) != 0 ||
				turn.PlanDigest != activeDigest || strings.TrimSpace(turn.ProjectID) == "" || turn.ProjectID != strings.TrimSpace(turn.ProjectID) ||
				strings.TrimSpace(turn.ProjectName) == "" || turn.ProjectName != strings.TrimSpace(turn.ProjectName) {
				return ErrInvalidSession
			}
			state = StateReadyToExecute
		default:
			return ErrInvalidSession
		}
	}
	if state != record.State {
		return ErrInvalidSession
	}
	return nil
}

func (record Record) RecordPlan(plan ceoplan.Plan, at time.Time) (Record, error) {
	if record.Validate() != nil || record.State != StatePlanGenerationApprovalRequired || at.IsZero() || validatePlanShape(plan) != nil {
		return Record{}, ErrInvalidState
	}
	digest, err := DigestPlan(plan)
	if err != nil {
		return Record{}, err
	}
	next := record.Clone()
	cloned := clonePlan(plan)
	next.Turns = append(next.Turns, Turn{Kind: TurnPlanGenerated, At: at, Plan: &cloned, PlanDigest: digest})
	next.Version++
	next.State = StatePlanApprovalRequired
	if len(plan.CEOQuestions) > 0 {
		next.State = StateClarificationRequired
	}
	return next, next.Validate()
}

func (record Record) RecordAnswers(answers []Answer, at time.Time) (Record, error) {
	plan, digest, ok := record.CurrentPlan()
	if record.Validate() != nil || record.State != StateClarificationRequired || !ok || at.IsZero() || validateAnswers(plan.CEOQuestions, answers) != nil {
		return Record{}, ErrInvalidState
	}
	next := record.Clone()
	next.Turns = append(next.Turns, Turn{
		Kind: TurnClarificationAnswered, At: at, PlanDigest: digest, Answers: orderAnswers(plan.CEOQuestions, answers),
	})
	next.Version++
	next.State = StatePlanGenerationApprovalRequired
	return next, next.Validate()
}

func (record Record) RecordApplied(projectID, projectName, planDigest string, at time.Time) (Record, error) {
	_, currentDigest, ok := record.CurrentPlan()
	projectID, projectName, planDigest = strings.TrimSpace(projectID), strings.TrimSpace(projectName), strings.TrimSpace(planDigest)
	if record.Validate() != nil || record.State != StatePlanApprovalRequired || !ok || currentDigest != planDigest ||
		projectID == "" || projectName == "" || at.IsZero() {
		return Record{}, ErrInvalidState
	}
	next := record.Clone()
	next.Turns = append(next.Turns, Turn{
		Kind: TurnPlanApplied, At: at, PlanDigest: planDigest, ProjectID: projectID, ProjectName: projectName,
	})
	next.Version++
	next.State = StateReadyToExecute
	return next, next.Validate()
}

func (record Record) CurrentPlan() (ceoplan.Plan, string, bool) {
	for index := len(record.Turns) - 1; index >= 0; index-- {
		if record.Turns[index].Kind == TurnPlanGenerated && record.Turns[index].Plan != nil {
			return clonePlan(*record.Turns[index].Plan), record.Turns[index].PlanDigest, true
		}
	}
	return ceoplan.Plan{}, "", false
}

func (record Record) PlanningRequest() (string, error) {
	if record.Validate() != nil || record.State != StatePlanGenerationApprovalRequired {
		return "", ErrInvalidState
	}
	answers := make([]Answer, 0)
	for _, turn := range record.Turns {
		if turn.Kind == TurnClarificationAnswered {
			answers = append(answers, cloneAnswers(turn.Answers)...)
		}
	}
	if len(answers) == 0 {
		return record.Request, nil
	}
	encoded, err := json.Marshal(answers)
	if err != nil {
		return "", err
	}
	return record.Request + "\n\n確認済みCEO回答(JSON):\n" + string(encoded), nil
}

func (record Record) Clone() Record {
	cloned := record
	cloned.Turns = make([]Turn, len(record.Turns))
	for index, turn := range record.Turns {
		cloned.Turns[index] = turn
		if turn.Plan != nil {
			plan := clonePlan(*turn.Plan)
			cloned.Turns[index].Plan = &plan
		}
		cloned.Turns[index].Answers = cloneAnswers(turn.Answers)
	}
	return cloned
}

func ValidateTransition(current, next Record, expectedVersion uint64) error {
	if current.Validate() != nil || next.Validate() != nil || current.SessionID != next.SessionID ||
		current.RequestDigest != next.RequestDigest || current.Version != expectedVersion || next.Version != current.Version+1 ||
		len(next.Turns) != len(current.Turns)+1 {
		return ErrVersionConflict
	}
	currentJSON, _ := json.Marshal(current.Turns)
	nextPrefixJSON, _ := json.Marshal(next.Turns[:len(current.Turns)])
	if string(currentJSON) != string(nextPrefixJSON) {
		return ErrVersionConflict
	}
	return nil
}

type Store interface {
	Create(ctx context.Context, record Record) error
	Get(ctx context.Context, sessionID string) (Record, error)
	List(ctx context.Context) ([]Record, error)
	Update(ctx context.Context, next Record, expectedVersion uint64) error
}

func DigestPlan(plan ceoplan.Plan) (string, error) {
	if validatePlanShape(plan) != nil {
		return "", ErrInvalidSession
	}
	return commandledger.RequestDigest(clonePlan(plan))
}

func requestDigest(sessionID, request, model string, createdAt time.Time) (string, error) {
	return commandledger.RequestDigest(struct {
		SessionID string    `json:"session_id"`
		Request   string    `json:"request"`
		Model     string    `json:"model"`
		CreatedAt time.Time `json:"created_at"`
	}{sessionID, request, model, createdAt})
}

func validatePlanShape(plan ceoplan.Plan) error {
	if !plan.PlanOnly || strings.TrimSpace(plan.ProjectName) == "" || strings.TrimSpace(plan.Objective) == "" ||
		strings.TrimSpace(plan.Summary) == "" || plan.RequiredDepartments == nil || plan.RequiredRoles == nil ||
		plan.AssignedExistingEmployees == nil || plan.MissingRoles == nil || plan.ProposedTasks == nil || len(plan.ProposedTasks) == 0 ||
		plan.Risks == nil || plan.CEOQuestions == nil {
		return ErrInvalidSession
	}
	for index, task := range plan.ProposedTasks {
		if task.ProposalID != fmt.Sprintf("PROPOSED-%03d", index+1) || strings.TrimSpace(task.Title) == "" ||
			strings.TrimSpace(task.Rationale) == "" || task.DependencyIDs == nil {
			return ErrInvalidSession
		}
	}
	return nil
}

func validateAnswers(questions []string, answers []Answer) error {
	if len(questions) == 0 || len(answers) != len(questions) || ValidateAnswerPayload(answers) != nil {
		return ErrInvalidSession
	}
	seen := make(map[string]bool, len(answers))
	for _, answer := range answers {
		seen[answer.Question] = true
	}
	for _, question := range questions {
		if !seen[question] {
			return ErrInvalidSession
		}
	}
	return nil
}

func orderAnswers(questions []string, answers []Answer) []Answer {
	byQuestion := make(map[string]string, len(answers))
	for _, answer := range answers {
		byQuestion[answer.Question] = strings.TrimSpace(answer.Answer)
	}
	ordered := make([]Answer, 0, len(questions))
	for _, question := range questions {
		ordered = append(ordered, Answer{Question: question, Answer: byQuestion[question]})
	}
	return ordered
}

func cloneAnswers(answers []Answer) []Answer {
	if answers == nil {
		return nil
	}
	return append(make([]Answer, 0, len(answers)), answers...)
}

func clonePlan(plan ceoplan.Plan) ceoplan.Plan {
	cloned := plan
	cloned.RequiredDepartments = cloneStrings(plan.RequiredDepartments)
	cloned.RequiredRoles = cloneStrings(plan.RequiredRoles)
	cloned.AssignedExistingEmployees = cloneStrings(plan.AssignedExistingEmployees)
	cloned.MissingRoles = cloneStrings(plan.MissingRoles)
	cloned.Risks = cloneStrings(plan.Risks)
	cloned.CEOQuestions = cloneStrings(plan.CEOQuestions)
	cloned.ProposedTasks = make([]ceoplan.ProposedTask, len(plan.ProposedTasks))
	for index, task := range plan.ProposedTasks {
		cloned.ProposedTasks[index] = task
		cloned.ProposedTasks[index].DependencyIDs = cloneStrings(task.DependencyIDs)
		if task.AssigneeID != nil {
			assignee := *task.AssigneeID
			cloned.ProposedTasks[index].AssigneeID = &assignee
		}
	}
	return cloned
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append(make([]string, 0, len(values)), values...)
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
