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
	"github.com/AkiraShimizu0/workspace-os/go/internal/review"
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
	StateWorkflowAttentionRequired      State = "workflow_attention_required"
	StateCompleted                      State = "completed"
)

func (state State) Valid() bool {
	return state == StatePlanGenerationApprovalRequired || state == StateClarificationRequired ||
		state == StatePlanApprovalRequired || state == StateReadyToExecute ||
		state == StateWorkflowAttentionRequired || state == StateCompleted
}

type TurnKind string

const (
	TurnPlanGenerated         TurnKind = "plan_generated"
	TurnClarificationAnswered TurnKind = "clarification_answered"
	TurnPlanApplied           TurnKind = "plan_applied"
	TurnWorkflowRecorded      TurnKind = "workflow_recorded"
)

type WorkflowStatus string

const (
	WorkflowStatusCompleted      WorkflowStatus = "completed"
	WorkflowStatusBlocked        WorkflowStatus = "blocked"
	WorkflowStatusLimitReached   WorkflowStatus = "limit_reached"
	WorkflowStatusFailed         WorkflowStatus = "failed"
	WorkflowStatusPartialFailure WorkflowStatus = "partial_failure"
)

func (status WorkflowStatus) Valid() bool {
	return status == WorkflowStatusCompleted || status == WorkflowStatusBlocked || status == WorkflowStatusLimitReached ||
		status == WorkflowStatusFailed || status == WorkflowStatusPartialFailure
}

type WorkflowTaskEvidence struct {
	TaskID             string         `json:"task_id"`
	TargetedRevision   bool           `json:"targeted_revision"`
	ExecutionCommandID string         `json:"execution_command_id"`
	ReviewCommandID    string         `json:"review_command_id,omitempty"`
	Verdict            review.Verdict `json:"verdict,omitempty"`
	RevisionCommandID  string         `json:"revision_command_id,omitempty"`
	RevisionTaskID     string         `json:"revision_task_id,omitempty"`
}

type WorkflowNextEvidence struct {
	Action          string   `json:"action"`
	TaskID          string   `json:"task_id,omitempty"`
	SourceTaskID    string   `json:"source_task_id,omitempty"`
	BlockingReasons []string `json:"blocking_reasons"`
}

type WorkflowFailure struct {
	Code    string `json:"code"`
	Stage   string `json:"stage"`
	Partial bool   `json:"partial"`
}

type WorkflowEvidence struct {
	SchemaVersion     int                    `json:"schema_version"`
	CommandID         string                 `json:"command_id"`
	WorkflowCommandID string                 `json:"workflow_command_id"`
	ProjectID         string                 `json:"project_id"`
	ProjectName       string                 `json:"project_name"`
	ReviewerID        string                 `json:"reviewer_id"`
	MaxTasks          int                    `json:"max_tasks"`
	Status            WorkflowStatus         `json:"status"`
	ResultDigest      string                 `json:"result_digest"`
	Tasks             []WorkflowTaskEvidence `json:"tasks"`
	Next              *WorkflowNextEvidence  `json:"next,omitempty"`
	Failure           *WorkflowFailure       `json:"failure,omitempty"`
}

type Answer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type Turn struct {
	Kind        TurnKind          `json:"kind"`
	At          time.Time         `json:"at"`
	Plan        *ceoplan.Plan     `json:"plan,omitempty"`
	PlanDigest  string            `json:"plan_digest,omitempty"`
	Answers     []Answer          `json:"answers,omitempty"`
	ProjectID   string            `json:"project_id,omitempty"`
	ProjectName string            `json:"project_name,omitempty"`
	Workflow    *WorkflowEvidence `json:"workflow,omitempty"`
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
	activeProjectID, activeProjectName := "", ""
	for _, turn := range record.Turns {
		if turn.At.IsZero() || turn.At.Before(lastAt) {
			return ErrInvalidSession
		}
		lastAt = turn.At
		switch turn.Kind {
		case TurnPlanGenerated:
			if state != StatePlanGenerationApprovalRequired || turn.Plan == nil || len(turn.Answers) != 0 ||
				turn.ProjectID != "" || turn.ProjectName != "" || turn.Workflow != nil || validatePlanShape(*turn.Plan) != nil {
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
				turn.ProjectID != "" || turn.ProjectName != "" || turn.Workflow != nil || activePlan == nil || validateAnswers(activePlan.CEOQuestions, turn.Answers) != nil {
				return ErrInvalidSession
			}
			state = StatePlanGenerationApprovalRequired
		case TurnPlanApplied:
			if state != StatePlanApprovalRequired || turn.Plan != nil || len(turn.Answers) != 0 ||
				turn.PlanDigest != activeDigest || strings.TrimSpace(turn.ProjectID) == "" || turn.ProjectID != strings.TrimSpace(turn.ProjectID) ||
				strings.TrimSpace(turn.ProjectName) == "" || turn.ProjectName != strings.TrimSpace(turn.ProjectName) || turn.Workflow != nil {
				return ErrInvalidSession
			}
			activeProjectID, activeProjectName = turn.ProjectID, turn.ProjectName
			state = StateReadyToExecute
		case TurnWorkflowRecorded:
			if state != StateReadyToExecute || turn.Plan != nil || turn.PlanDigest != "" || len(turn.Answers) != 0 ||
				turn.ProjectID != "" || turn.ProjectName != "" || turn.Workflow == nil ||
				validateWorkflowEvidence(*turn.Workflow, activeProjectID, activeProjectName) != nil {
				return ErrInvalidSession
			}
			switch turn.Workflow.Status {
			case WorkflowStatusCompleted:
				state = StateCompleted
			case WorkflowStatusBlocked, WorkflowStatusLimitReached:
				state = StateReadyToExecute
			case WorkflowStatusFailed, WorkflowStatusPartialFailure:
				state = StateWorkflowAttentionRequired
			}
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

func (record Record) RecordWorkflow(evidence WorkflowEvidence, at time.Time) (Record, error) {
	projectID, projectName, ok := record.AppliedProject()
	if record.Validate() != nil || record.State != StateReadyToExecute || !ok || at.IsZero() ||
		validateWorkflowEvidence(evidence, projectID, projectName) != nil {
		return Record{}, ErrInvalidState
	}
	next := record.Clone()
	cloned := cloneWorkflowEvidence(evidence)
	next.Turns = append(next.Turns, Turn{Kind: TurnWorkflowRecorded, At: at, Workflow: &cloned})
	next.Version++
	switch evidence.Status {
	case WorkflowStatusCompleted:
		next.State = StateCompleted
	case WorkflowStatusBlocked, WorkflowStatusLimitReached:
		next.State = StateReadyToExecute
	case WorkflowStatusFailed, WorkflowStatusPartialFailure:
		next.State = StateWorkflowAttentionRequired
	}
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

func (record Record) AppliedProject() (string, string, bool) {
	for index := len(record.Turns) - 1; index >= 0; index-- {
		if record.Turns[index].Kind == TurnPlanApplied {
			return record.Turns[index].ProjectID, record.Turns[index].ProjectName, true
		}
	}
	return "", "", false
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
		if turn.Workflow != nil {
			workflow := cloneWorkflowEvidence(*turn.Workflow)
			cloned.Turns[index].Workflow = &workflow
		}
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

func validateWorkflowEvidence(evidence WorkflowEvidence, projectID, projectName string) error {
	if evidence.SchemaVersion != SchemaVersion || commandledger.ValidateCommandID(evidence.CommandID) != nil ||
		commandledger.ValidateCommandID(evidence.WorkflowCommandID) != nil || strings.TrimSpace(evidence.ProjectID) == "" ||
		evidence.ProjectID != strings.TrimSpace(evidence.ProjectID) || evidence.ProjectID != projectID ||
		strings.TrimSpace(evidence.ProjectName) == "" || evidence.ProjectName != strings.TrimSpace(evidence.ProjectName) || evidence.ProjectName != projectName ||
		strings.TrimSpace(evidence.ReviewerID) == "" || evidence.ReviewerID != strings.TrimSpace(evidence.ReviewerID) ||
		strings.ContainsAny(evidence.ReviewerID, "\r\n") || evidence.MaxTasks <= 0 || evidence.MaxTasks > 100 ||
		!evidence.Status.Valid() || !validDigest(evidence.ResultDigest) || evidence.Tasks == nil || len(evidence.Tasks) > evidence.MaxTasks {
		return ErrInvalidSession
	}
	for _, taskEvidence := range evidence.Tasks {
		if strings.TrimSpace(taskEvidence.TaskID) == "" || taskEvidence.TaskID != strings.TrimSpace(taskEvidence.TaskID) ||
			commandledger.ValidateCommandID(taskEvidence.ExecutionCommandID) != nil ||
			taskEvidence.ReviewCommandID != "" && commandledger.ValidateCommandID(taskEvidence.ReviewCommandID) != nil ||
			taskEvidence.RevisionCommandID != "" && commandledger.ValidateCommandID(taskEvidence.RevisionCommandID) != nil ||
			taskEvidence.Verdict != "" && taskEvidence.Verdict != review.VerdictApprove && taskEvidence.Verdict != review.VerdictRequestChanges ||
			taskEvidence.Verdict != "" && taskEvidence.ReviewCommandID == "" ||
			taskEvidence.RevisionTaskID != "" && (taskEvidence.RevisionTaskID != strings.TrimSpace(taskEvidence.RevisionTaskID) || taskEvidence.RevisionCommandID == "") {
			return ErrInvalidSession
		}
	}
	if evidence.Next != nil {
		if evidence.Next.BlockingReasons == nil || strings.TrimSpace(evidence.Next.TaskID) == "" || evidence.Next.TaskID != strings.TrimSpace(evidence.Next.TaskID) ||
			evidence.Next.Action != "wait" && evidence.Next.Action != "execute_task" && evidence.Next.Action != "execute_revision_task" {
			return ErrInvalidSession
		}
		for _, reason := range evidence.Next.BlockingReasons {
			if strings.TrimSpace(reason) == "" || reason != strings.TrimSpace(reason) {
				return ErrInvalidSession
			}
		}
	}
	requiresFailure := evidence.Status == WorkflowStatusFailed || evidence.Status == WorkflowStatusPartialFailure
	if requiresFailure != (evidence.Failure != nil) || evidence.Status == WorkflowStatusCompleted && evidence.Next != nil ||
		(evidence.Status == WorkflowStatusBlocked || evidence.Status == WorkflowStatusLimitReached) && evidence.Next == nil {
		return ErrInvalidSession
	}
	if evidence.Failure != nil {
		if strings.TrimSpace(evidence.Failure.Code) == "" || evidence.Failure.Code != strings.TrimSpace(evidence.Failure.Code) ||
			strings.TrimSpace(evidence.Failure.Stage) == "" || evidence.Failure.Stage != strings.TrimSpace(evidence.Failure.Stage) ||
			evidence.Failure.Partial != (evidence.Status == WorkflowStatusPartialFailure) {
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

func cloneWorkflowEvidence(evidence WorkflowEvidence) WorkflowEvidence {
	cloned := evidence
	cloned.Tasks = append(make([]WorkflowTaskEvidence, 0, len(evidence.Tasks)), evidence.Tasks...)
	if evidence.Next != nil {
		next := *evidence.Next
		next.BlockingReasons = cloneStrings(evidence.Next.BlockingReasons)
		cloned.Next = &next
	}
	if evidence.Failure != nil {
		failure := *evidence.Failure
		cloned.Failure = &failure
	}
	return cloned
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
