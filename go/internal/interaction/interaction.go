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

	"github.com/AkiraShimizu0/WorkCairn/go/internal/action"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/autonomy"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/revision"
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
	StateActionCompleted                State = "action_completed"
	StateActionAttentionRequired        State = "action_attention_required"
)

func (state State) Valid() bool {
	return state == StatePlanGenerationApprovalRequired || state == StateClarificationRequired ||
		state == StatePlanApprovalRequired || state == StateReadyToExecute ||
		state == StateWorkflowAttentionRequired || state == StateCompleted ||
		state == StateActionCompleted || state == StateActionAttentionRequired
}

type TurnKind string

const (
	TurnPlanGenerated         TurnKind = "plan_generated"
	TurnClarificationAnswered TurnKind = "clarification_answered"
	TurnPlanApplied           TurnKind = "plan_applied"
	TurnWorkflowRecorded      TurnKind = "workflow_recorded"
	TurnActionRecorded        TurnKind = "action_recorded"
	// TurnArchived and TurnUnarchived record a CEO's visibility decision
	// for this Session -- "hide from the active request list" and its
	// reverse. They are deliberately orthogonal to every other Turn Kind
	// above: they carry no Plan/Answers/Workflow/Action evidence, never
	// gate on or change Record.State (see Validate()'s own case for these
	// two Kinds), and can be appended regardless of which workflow state
	// the Session is currently in. archived-ness itself is never stored
	// as a field -- it is always derived from the most recent of these
	// two Turn Kinds (see IsArchived()), so the append-only Turn history
	// remains the single source of truth.
	TurnArchived   TurnKind = "archived"
	TurnUnarchived TurnKind = "unarchived"
	// TurnRevisionRecoveryStarted records the CEO's own recovery action
	// (Revision Limit Recovery) from StateWorkflowAttentionRequired: it
	// names the specific Task the CEO wants revised again and carries an
	// optional fresh instruction (RecoveryGuidance). It carries no Plan/
	// Answers/Workflow/Action evidence of its own -- the Reviewed Workflow
	// continuation this authorizes still produces its own ordinary
	// TurnWorkflowRecorded, exactly like every other Workflow round.
	TurnRevisionRecoveryStarted TurnKind = "revision_recovery_started"
)

type ActionStatus string

const (
	ActionStatusPublished      ActionStatus = "published"
	ActionStatusFailed         ActionStatus = "failed"
	ActionStatusPartialFailure ActionStatus = "partial_failure"
)

func (status ActionStatus) Valid() bool {
	return status == ActionStatusPublished || status == ActionStatusFailed || status == ActionStatusPartialFailure
}

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
	Autonomy          *autonomy.Contract     `json:"autonomy_contract,omitempty"`
	Status            WorkflowStatus         `json:"status"`
	ResultDigest      string                 `json:"result_digest"`
	Tasks             []WorkflowTaskEvidence `json:"tasks"`
	Next              *WorkflowNextEvidence  `json:"next,omitempty"`
	Failure           *WorkflowFailure       `json:"failure,omitempty"`
}

type ActionEvidence struct {
	SchemaVersion   int                 `json:"schema_version"`
	CommandID       string              `json:"command_id"`
	ActionCommandID string              `json:"action_command_id"`
	ProjectID       string              `json:"project_id"`
	ProjectName     string              `json:"project_name"`
	TaskID          string              `json:"task_id"`
	TargetID        string              `json:"target_id"`
	SourceSHA256    string              `json:"source_sha256"`
	Status          ActionStatus        `json:"status"`
	ResultDigest    string              `json:"result_digest"`
	Intent          *action.Evidence    `json:"intent,omitempty"`
	Publication     *action.Publication `json:"publication,omitempty"`
	Outcome         *action.Evidence    `json:"outcome,omitempty"`
	EventID         string              `json:"event_id,omitempty"`
	EventPublished  bool                `json:"event_published"`
	Failure         *WorkflowFailure    `json:"failure,omitempty"`
}

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
	// PreAuthorizedWorkflowCommandID is set only on a TurnPlanApplied turn
	// produced by the Go-owned interaction.plan.approve_and_execute chain
	// (ADR-0049). It durably records that the single CEO approval behind
	// this Command ID already covers the Reviewed Workflow execution that
	// follows -- Next() must not ask for a second approval while it is set.
	// Empty for a TurnPlanApplied turn produced by the standalone
	// interaction.plan.apply command, which still requires a separate
	// interaction.workflow.execute approval exactly as before.
	PreAuthorizedWorkflowCommandID string            `json:"pre_authorized_workflow_command_id,omitempty"`
	Workflow                       *WorkflowEvidence `json:"workflow,omitempty"`
	Action                         *ActionEvidence   `json:"action,omitempty"`
	// RecoveryTaskID and RecoveryGuidance are set only on a
	// TurnRevisionRecoveryStarted turn (Revision Limit Recovery). See that
	// Kind's own doc comment.
	RecoveryTaskID   string `json:"recovery_task_id,omitempty"`
	RecoveryGuidance string `json:"recovery_guidance,omitempty"`
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

type NextActionKind string

const (
	NextApprovePlanGeneration NextActionKind = "approve_plan_generation"
	NextAnswerClarifications  NextActionKind = "answer_clarifications"
	NextApprovePlanApply      NextActionKind = "approve_plan_apply"
	NextApproveWorkflow       NextActionKind = "approve_workflow"
	NextInspectWorkflow       NextActionKind = "inspect_workflow_recovery"
	NextOptionalAction        NextActionKind = "optional_external_action_or_done"
	NextInspectAction         NextActionKind = "inspect_action_recovery"
	NextDone                  NextActionKind = "done"
)

type CommandReference struct {
	Scope       string `json:"scope"`
	ProjectName string `json:"project_name,omitempty"`
	CommandID   string `json:"command_id"`
}

type NextAction struct {
	Kind             NextActionKind `json:"kind"`
	Operation        string         `json:"operation,omitempty"`
	SessionID        string         `json:"session_id"`
	ExpectedVersion  uint64         `json:"expected_version"`
	ApprovalRequired bool           `json:"approval_required"`
	RequiredFields   []string       `json:"required_fields"`
	Questions        []string       `json:"questions,omitempty"`
	PlanDigest       string         `json:"plan_digest,omitempty"`
	ProjectID        string         `json:"project_id,omitempty"`
	ProjectName      string         `json:"project_name,omitempty"`
	EligibleTaskIDs  []string       `json:"eligible_task_ids,omitempty"`
	// EvidenceTaskID is the Task whose already-committed Deliverable/Review
	// explains a Recovery action. It can differ from the executable target:
	// Budget continuation executes an already-created Revision Task while
	// presenting its completed source Task's evidence.
	EvidenceTaskID string             `json:"evidence_task_id,omitempty"`
	Commands       []CommandReference `json:"commands,omitempty"`
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
	// answeredCount tracks how many of activePlan.CEOQuestions (in order)
	// already have a durable answer, across one or more
	// TurnClarificationAnswered Turns since activePlan was generated. It
	// resets to 0 every time a new TurnPlanGenerated Turn is processed, so
	// a Plan regeneration's own (possibly different) CEOQuestions are
	// never confused with a prior round's progress.
	answeredCount := 0
	activeProjectID, activeProjectName := "", ""
	var latestWorkflow *WorkflowEvidence
	// archived tracks visibility independently of state (see TurnArchived's
	// own doc comment): it is validated for correct alternation here
	// (archive only from active, unarchive only from archived) but never
	// participates in the workflow state machine below.
	archived := false
	for _, turn := range record.Turns {
		if turn.At.IsZero() || turn.At.Before(lastAt) {
			return ErrInvalidSession
		}
		lastAt = turn.At
		switch turn.Kind {
		case TurnPlanGenerated:
			if state != StatePlanGenerationApprovalRequired || turn.Plan == nil || len(turn.Answers) != 0 ||
				turn.ProjectID != "" || turn.ProjectName != "" || turn.Workflow != nil || turn.Action != nil || validatePlanShape(*turn.Plan) != nil {
				return ErrInvalidSession
			}
			digest, err := DigestPlan(*turn.Plan)
			if err != nil || turn.PlanDigest != digest {
				return ErrInvalidSession
			}
			plan := clonePlan(*turn.Plan)
			activePlan, activeDigest = &plan, digest
			answeredCount = 0
			state = StatePlanApprovalRequired
			if len(plan.CEOQuestions) > 0 {
				state = StateClarificationRequired
			}
		case TurnClarificationAnswered:
			if state != StateClarificationRequired || turn.Plan != nil || turn.PlanDigest != activeDigest ||
				turn.ProjectID != "" || turn.ProjectName != "" || turn.Workflow != nil || turn.Action != nil || activePlan == nil ||
				validateIncrementalAnswers(activePlan.CEOQuestions, answeredCount, turn.Answers) != nil {
				return ErrInvalidSession
			}
			answeredCount += len(turn.Answers)
			if answeredCount == len(activePlan.CEOQuestions) {
				state = StatePlanGenerationApprovalRequired
			}
		case TurnPlanApplied:
			if state != StatePlanApprovalRequired || turn.Plan != nil || len(turn.Answers) != 0 ||
				turn.PlanDigest != activeDigest || strings.TrimSpace(turn.ProjectID) == "" || turn.ProjectID != strings.TrimSpace(turn.ProjectID) ||
				strings.TrimSpace(turn.ProjectName) == "" || turn.ProjectName != strings.TrimSpace(turn.ProjectName) || turn.Workflow != nil || turn.Action != nil ||
				turn.PreAuthorizedWorkflowCommandID != "" && commandledger.ValidateCommandID(turn.PreAuthorizedWorkflowCommandID) != nil {
				return ErrInvalidSession
			}
			activeProjectID, activeProjectName = turn.ProjectID, turn.ProjectName
			state = StateReadyToExecute
		case TurnWorkflowRecorded:
			if state != StateReadyToExecute || turn.Plan != nil || turn.PlanDigest != "" || len(turn.Answers) != 0 ||
				turn.ProjectID != "" || turn.ProjectName != "" || turn.Workflow == nil || turn.Action != nil ||
				validateWorkflowEvidence(*turn.Workflow, activeProjectID, activeProjectName) != nil {
				return ErrInvalidSession
			}
			workflow := cloneWorkflowEvidence(*turn.Workflow)
			latestWorkflow = &workflow
			switch turn.Workflow.Status {
			case WorkflowStatusCompleted:
				state = StateCompleted
			case WorkflowStatusBlocked, WorkflowStatusLimitReached:
				state = StateReadyToExecute
			case WorkflowStatusFailed, WorkflowStatusPartialFailure:
				state = StateWorkflowAttentionRequired
			}
		case TurnActionRecorded:
			if state != StateCompleted || turn.Plan != nil || turn.PlanDigest != "" || len(turn.Answers) != 0 ||
				turn.ProjectID != "" || turn.ProjectName != "" || turn.Workflow != nil || turn.Action == nil || latestWorkflow == nil ||
				validateActionEvidence(*turn.Action, activeProjectID, activeProjectName, *latestWorkflow) != nil {
				return ErrInvalidSession
			}
			if turn.Action.Status == ActionStatusPublished {
				state = StateActionCompleted
			} else {
				state = StateActionAttentionRequired
			}
		case TurnArchived:
			if archived || turn.Plan != nil || turn.PlanDigest != "" || len(turn.Answers) != 0 ||
				turn.ProjectID != "" || turn.ProjectName != "" || turn.PreAuthorizedWorkflowCommandID != "" ||
				turn.Workflow != nil || turn.Action != nil {
				return ErrInvalidSession
			}
			archived = true
		case TurnUnarchived:
			if !archived || turn.Plan != nil || turn.PlanDigest != "" || len(turn.Answers) != 0 ||
				turn.ProjectID != "" || turn.ProjectName != "" || turn.PreAuthorizedWorkflowCommandID != "" ||
				turn.Workflow != nil || turn.Action != nil {
				return ErrInvalidSession
			}
			archived = false
		case TurnRevisionRecoveryStarted:
			if state != StateWorkflowAttentionRequired || turn.Plan != nil || turn.PlanDigest != "" || len(turn.Answers) != 0 ||
				turn.ProjectID != "" || turn.ProjectName != "" || turn.Workflow != nil || turn.Action != nil ||
				strings.TrimSpace(turn.RecoveryTaskID) != turn.RecoveryTaskID || turn.RecoveryTaskID == "" ||
				strings.ContainsAny(turn.RecoveryTaskID, "\r\n") ||
				(turn.RecoveryGuidance != "" && (strings.ContainsAny(turn.RecoveryGuidance, "\r\n") || len(turn.RecoveryGuidance) > revision.MaxAdditionalGuidanceLength)) {
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
	if record.Validate() != nil || record.State != StatePlanGenerationApprovalRequired || at.IsZero() {
		return Record{}, ErrInvalidState
	}
	// validatePlanShape's own error (a *PlanValidationError, sanitized
	// reason/field/task-index only) is returned directly here, not
	// collapsed into the bare ErrInvalidState above -- this is the one
	// call site whose error actually reaches the CEO via the
	// interaction_plan_validation stage, so it is the one place this
	// diagnostic detail is worth preserving.
	if err := validatePlanShape(plan); err != nil {
		return Record{}, err
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

// RecordAnswers durably commits one batch of clarification answers as its
// own append-only Turn. The batch may cover as few as a single question --
// it is not required to complete every remaining CEOQuestion in one call
// (see validateIncrementalAnswers) -- so a CEO answering questions one at
// a time gets each answer committed the moment it is sent, never held in
// memory pending a later batch submit. State advances to
// StatePlanGenerationApprovalRequired only once every CEOQuestion has a
// recorded answer; otherwise it stays StateClarificationRequired so
// Next() can name the single next unanswered question.
func (record Record) RecordAnswers(answers []Answer, at time.Time) (Record, error) {
	plan, digest, ok := record.CurrentPlan()
	answeredCount := record.answeredCountSinceCurrentPlan()
	if record.Validate() != nil || record.State != StateClarificationRequired || !ok || at.IsZero() ||
		validateIncrementalAnswers(plan.CEOQuestions, answeredCount, answers) != nil {
		return Record{}, ErrInvalidState
	}
	trimmed := make([]Answer, len(answers))
	for index, answer := range answers {
		trimmed[index] = Answer{Question: answer.Question, Answer: strings.TrimSpace(answer.Answer)}
	}
	next := record.Clone()
	next.Turns = append(next.Turns, Turn{
		Kind: TurnClarificationAnswered, At: at, PlanDigest: digest, Answers: trimmed,
	})
	next.Version++
	next.State = StateClarificationRequired
	if answeredCount+len(answers) == len(plan.CEOQuestions) {
		next.State = StatePlanGenerationApprovalRequired
	}
	return next, next.Validate()
}

// answeredCountSinceCurrentPlan returns how many of CurrentPlan's
// CEOQuestions (in order) already have a durable answer, by summing the
// Answers of every TurnClarificationAnswered Turn recorded since the most
// recent TurnPlanGenerated Turn. Zero when no Plan has been generated yet.
func (record Record) answeredCountSinceCurrentPlan() int {
	planIndex := -1
	for index := len(record.Turns) - 1; index >= 0; index-- {
		if record.Turns[index].Kind == TurnPlanGenerated && record.Turns[index].Plan != nil {
			planIndex = index
			break
		}
	}
	if planIndex == -1 {
		return 0
	}
	count := 0
	for index := planIndex + 1; index < len(record.Turns); index++ {
		if record.Turns[index].Kind == TurnClarificationAnswered {
			count += len(record.Turns[index].Answers)
		}
	}
	return count
}

// RecordApplied appends the canonical Plan-apply outcome as a durable Turn.
// preAuthorizedWorkflowCommandID is empty for the standalone
// interaction.plan.apply command (unchanged, pre-CP4 semantics: Workflow
// execution still needs its own interaction.workflow.execute approval). A
// non-empty, well-formed Command ID marks this Plan apply as performed under
// the Go-owned interaction.plan.approve_and_execute chain (ADR-0049) -- the
// single CEO approval behind that outer Command ID already covers the
// Reviewed Workflow execution that Next() will resume without asking again.
func (record Record) RecordApplied(projectID, projectName, planDigest, preAuthorizedWorkflowCommandID string, at time.Time) (Record, error) {
	_, currentDigest, ok := record.CurrentPlan()
	projectID, projectName, planDigest = strings.TrimSpace(projectID), strings.TrimSpace(projectName), strings.TrimSpace(planDigest)
	preAuthorizedWorkflowCommandID = strings.TrimSpace(preAuthorizedWorkflowCommandID)
	if record.Validate() != nil || record.State != StatePlanApprovalRequired || !ok || currentDigest != planDigest ||
		projectID == "" || projectName == "" || at.IsZero() ||
		preAuthorizedWorkflowCommandID != "" && commandledger.ValidateCommandID(preAuthorizedWorkflowCommandID) != nil {
		return Record{}, ErrInvalidState
	}
	next := record.Clone()
	next.Turns = append(next.Turns, Turn{
		Kind: TurnPlanApplied, At: at, PlanDigest: planDigest, ProjectID: projectID, ProjectName: projectName,
		PreAuthorizedWorkflowCommandID: preAuthorizedWorkflowCommandID,
	})
	next.Version++
	next.State = StateReadyToExecute
	return next, next.Validate()
}

// PendingWorkflowPreAuthorization reports the outer Command ID a not-yet-
// started Reviewed Workflow execution is already durably approved under, so
// Next() can resume the same chain instead of asking for a second approval.
// It only ever looks at the single most recent Turn: if the session reached
// StateReadyToExecute through a Blocked/LimitReached WorkflowEvidence turn
// (an existing, unrelated continuation path this checkpoint does not
// change), that most recent Turn is a TurnWorkflowRecorded, not a
// TurnPlanApplied, and this correctly reports no pending pre-authorization --
// continuing that case still requires a fresh interaction.workflow.execute
// approval, exactly as before CP4.
func (record Record) PendingWorkflowPreAuthorization() (string, bool) {
	if record.State != StateReadyToExecute || len(record.Turns) == 0 {
		return "", false
	}
	last := record.Turns[len(record.Turns)-1]
	if last.Kind != TurnPlanApplied || last.PreAuthorizedWorkflowCommandID == "" {
		return "", false
	}
	return last.PreAuthorizedWorkflowCommandID, true
}

// stalledRevisionTaskID finds the one Task, if any, whose Review verdict
// was Request Changes but which never received a follow-up Revision --
// exactly and only the state the Revision Guard's stop leaves behind (every
// other Request Changes verdict in a completed round was either
// auto-revised, matching a non-empty RevisionCommandID, or belongs to a
// still-running branch that would not appear in a terminal Workflow
// Turn's evidence at all). No new field is needed to identify it: this is
// a pure read of WorkflowTaskEvidence, the same evidence Conversation
// Projection already reads.
func stalledRevisionTaskID(tasks []WorkflowTaskEvidence) (string, bool) {
	for index := len(tasks) - 1; index >= 0; index-- {
		current := tasks[index]
		if current.Verdict == review.VerdictRequestChanges && current.RevisionCommandID == "" {
			return current.TaskID, true
		}
	}
	return "", false
}

// budgetContinuationTaskID identifies exactly one Revision Task that was
// committed by this Workflow result but never executed by it. A Revision
// Task appears first as RevisionTaskID on its source attempt and, once
// execution starts, as a later TaskID. Set subtraction therefore derives
// the pending continuation from canonical Workflow evidence without asking
// the UI to choose or infer a target. Ambiguous (zero or multiple) states
// are default-denied.
func budgetContinuationTask(tasks []WorkflowTaskEvidence) (revisionTaskID, sourceTaskID string, found bool) {
	executed := make(map[string]bool, len(tasks))
	candidates := make(map[string]string)
	candidateCount := make(map[string]int)
	for _, current := range tasks {
		executed[current.TaskID] = true
		if current.Verdict == review.VerdictRequestChanges && current.RevisionCommandID != "" && current.RevisionTaskID != "" {
			candidates[current.RevisionTaskID] = current.TaskID
			candidateCount[current.RevisionTaskID]++
		}
	}
	for taskID := range executed {
		delete(candidates, taskID)
		delete(candidateCount, taskID)
	}
	if len(candidates) != 1 {
		return "", "", false
	}
	for taskID, sourceID := range candidates {
		if candidateCount[taskID] != 1 {
			return "", "", false
		}
		return taskID, sourceID, true
	}
	return "", "", false
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

// RecordRevisionRecoveryStarted is the CEO's own explicit recovery action
// after a Revision Limit, No Progress, or recoverable Budget stop
// (StateWorkflowAttentionRequired): it durably records which source or
// already-created Revision Task is continued and any fresh instruction,
// then reopens the Session for exactly one more Reviewed Workflow round --
// mirroring how WorkflowStatusBlocked/LimitReached already leave the
// Session in StateReadyToExecute for the same "continue via a new Command"
// pattern (RecordWorkflow above), just reached from
// StateWorkflowAttentionRequired instead. This is not automatic retry: the
// caller (ExecuteInteractionRecoverRevision) only ever calls this in
// response to a fresh, explicitly approved Command carrying its own new
// Command ID -- it is never invoked by anything inside the Reviewed
// Workflow's own automatic dispatch.
func (record Record) RecordRevisionRecoveryStarted(taskID, guidance string, at time.Time) (Record, error) {
	taskID = strings.TrimSpace(taskID)
	guidance = strings.TrimSpace(guidance)
	if record.Validate() != nil || record.State != StateWorkflowAttentionRequired || at.IsZero() ||
		taskID == "" || strings.ContainsAny(taskID, "\r\n") ||
		(guidance != "" && (strings.ContainsAny(guidance, "\r\n") || len(guidance) > revision.MaxAdditionalGuidanceLength)) {
		return Record{}, ErrInvalidState
	}
	next := record.Clone()
	next.Turns = append(next.Turns, Turn{
		Kind: TurnRevisionRecoveryStarted, At: at, RecoveryTaskID: taskID, RecoveryGuidance: guidance,
	})
	next.Version++
	next.State = StateReadyToExecute
	return next, next.Validate()
}

func (record Record) RecordAction(evidence ActionEvidence, at time.Time) (Record, error) {
	projectID, projectName, projectOK := record.AppliedProject()
	workflow, workflowOK := record.LatestWorkflow()
	if record.Validate() != nil || record.State != StateCompleted || !projectOK || !workflowOK || at.IsZero() ||
		validateActionEvidence(evidence, projectID, projectName, workflow) != nil {
		return Record{}, ErrInvalidState
	}
	next := record.Clone()
	cloned := cloneActionEvidence(evidence)
	next.Turns = append(next.Turns, Turn{Kind: TurnActionRecorded, At: at, Action: &cloned})
	next.Version++
	if evidence.Status == ActionStatusPublished {
		next.State = StateActionCompleted
	} else {
		next.State = StateActionAttentionRequired
	}
	return next, next.Validate()
}

// RecordArchive durably hides this Session from the active request list by
// appending a TurnArchived Turn. It never touches Record.State: archiving a
// Session that is mid-clarification, awaiting approval, or already
// completed all work identically -- only the append-only Turn history
// changes. Rejected (ErrInvalidState) when the Session is already
// archived, matching every other Record-mutation method's existing
// precondition-gated design (RecordPlan/RecordAnswers/RecordApplied all
// reject when the Session is not in the exact state they expect) rather
// than introducing a new idempotent-success shape unique to this method.
// Cross-request idempotency for a genuinely repeated archive request is
// the Command Ledger's job (same Command ID replays the cached result
// without ever reaching this method a second time), not this method's.
func (record Record) RecordArchive(at time.Time) (Record, error) {
	if record.Validate() != nil || at.IsZero() || record.IsArchived() {
		return Record{}, ErrInvalidState
	}
	next := record.Clone()
	next.Turns = append(next.Turns, Turn{Kind: TurnArchived, At: at})
	next.Version++
	return next, next.Validate()
}

// RecordUnarchive is RecordArchive's exact reverse: it appends a
// TurnUnarchived Turn and is rejected when the Session is not currently
// archived, for the same reasons documented on RecordArchive.
func (record Record) RecordUnarchive(at time.Time) (Record, error) {
	if record.Validate() != nil || at.IsZero() || !record.IsArchived() {
		return Record{}, ErrInvalidState
	}
	next := record.Clone()
	next.Turns = append(next.Turns, Turn{Kind: TurnUnarchived, At: at})
	next.Version++
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

// IsArchived is the sole source of truth for whether this Session is
// currently hidden from the active request list. It never reads a stored
// boolean field -- archived-ness is not persisted as state at all -- it
// deterministically scans backward for the most recent TurnArchived or
// TurnUnarchived Turn and reports which one it was. No Turns of either
// Kind means never archived. This is safe to call on any Record that has
// already passed Validate() (which itself confirms the two Kinds
// alternate correctly), including read-model use after a fresh reload or
// daemon restart, since the append-only Turn history is the only thing
// this method depends on.
func (record Record) IsArchived() bool {
	for index := len(record.Turns) - 1; index >= 0; index-- {
		switch record.Turns[index].Kind {
		case TurnArchived:
			return true
		case TurnUnarchived:
			return false
		}
	}
	return false
}

func (record Record) LatestWorkflow() (WorkflowEvidence, bool) {
	for index := len(record.Turns) - 1; index >= 0; index-- {
		if record.Turns[index].Kind == TurnWorkflowRecorded && record.Turns[index].Workflow != nil {
			return cloneWorkflowEvidence(*record.Turns[index].Workflow), true
		}
	}
	return WorkflowEvidence{}, false
}

func (record Record) Next() (NextAction, error) {
	if record.Validate() != nil {
		return NextAction{}, ErrInvalidSession
	}
	next := NextAction{
		SessionID: record.SessionID, ExpectedVersion: record.Version,
		RequiredFields: []string{}, Commands: []CommandReference{},
	}
	projectID, projectName, _ := record.AppliedProject()
	next.ProjectID, next.ProjectName = projectID, projectName
	switch record.State {
	case StatePlanGenerationApprovalRequired:
		next.Kind, next.Operation, next.ApprovalRequired = NextApprovePlanGeneration, "interaction.plan.generate", true
		next.RequiredFields = []string{"command_id", "current_time"}
	case StateClarificationRequired:
		plan, _, ok := record.CurrentPlan()
		if !ok {
			return NextAction{}, ErrInvalidSession
		}
		next.Kind, next.Operation, next.ApprovalRequired = NextAnswerClarifications, "interaction.answer", true
		next.RequiredFields = []string{"answers", "command_id", "current_time"}
		// Questions names only the single next unanswered CEOQuestion, not
		// the full original list: answers are recorded one durable Turn
		// at a time (RecordAnswers), in order, so this is always the exact
		// next one a client should ask and submit.
		answeredCount := record.answeredCountSinceCurrentPlan()
		if answeredCount < len(plan.CEOQuestions) {
			next.Questions = []string{plan.CEOQuestions[answeredCount]}
		}
	case StatePlanApprovalRequired:
		plan, digest, ok := record.CurrentPlan()
		if !ok {
			return NextAction{}, ErrInvalidSession
		}
		// ADR-0049: the CEO's single "この内容で進める" approval covers both
		// canonical Plan apply and the Reviewed Workflow execution that
		// follows -- Go, not the client, owns continuing the chain after
		// apply succeeds. The standalone interaction.plan.apply command
		// (unchanged, still Ledger-tracked) remains available for operator
		// and Recovery use; the normal product path only ever reaches it
		// through this Next() operation now.
		next.Kind, next.Operation, next.ApprovalRequired = NextApprovePlanApply, "interaction.plan.approve_and_execute", true
		next.RequiredFields = []string{"project_id", "command_id", "current_time"}
		next.PlanDigest = digest
		next.ProjectName = plan.ProjectName
	case StateReadyToExecute:
		if preAuthCommandID, ok := record.PendingWorkflowPreAuthorization(); ok {
			// The Reviewed Workflow execution behind this Turn is already
			// durably pre-authorized (ADR-0049) -- observed only in the brief
			// window while the same synchronous chain is still running, or
			// after a daemon crash left it there. Never ask for a second
			// approval; point at the pre-authorizing outer Command so a human
			// can inspect what actually happened instead of guessing.
			next.Kind = NextInspectWorkflow
			next.Commands = []CommandReference{{Scope: "workspace", CommandID: preAuthCommandID}}
		} else {
			next.Kind, next.Operation, next.ApprovalRequired = NextApproveWorkflow, "interaction.workflow.execute", true
			next.RequiredFields = []string{"reviewer_id", "max_tasks", "workflow_plan_digest", "command_id", "current_time"}
		}
	case StateWorkflowAttentionRequired:
		next.Kind = NextInspectWorkflow
		workflow, ok := record.LatestWorkflow()
		if !ok {
			return NextAction{}, ErrInvalidSession
		}
		next.Commands = []CommandReference{
			{Scope: "workspace", CommandID: workflow.CommandID},
			{Scope: "project", ProjectName: workflow.ProjectName, CommandID: workflow.WorkflowCommandID},
		}
		// Recovery is offered only when the recorded stop has exactly one
		// evidence-derived target. Revision Limit / No Progress create a new
		// Revision from the stalled source Task; Budget continuation resumes
		// the one already-created Revision Task that this Workflow never
		// executed. Both share one CEO-facing operation and composer, while
		// the process layer validates and executes their distinct semantics.
		if workflow.Failure != nil {
			taskID, evidenceTaskID, found := "", "", false
			switch workflow.Failure.Code {
			case "REVISION_LIMIT_REACHED", "NO_PROGRESS_DETECTED":
				taskID, found = stalledRevisionTaskID(workflow.Tasks)
				evidenceTaskID = taskID
			case "BUDGET_EXCEEDED":
				taskID, evidenceTaskID, found = budgetContinuationTask(workflow.Tasks)
			}
			if found {
				next.Operation, next.ApprovalRequired = "interaction.workflow.recover_revision", true
				next.RequiredFields = []string{"task_id", "command_id", "current_time"}
				next.EligibleTaskIDs = []string{taskID}
				next.EvidenceTaskID = evidenceTaskID
			}
		}
	case StateCompleted:
		next.Kind, next.Operation, next.ApprovalRequired = NextOptionalAction, "interaction.action.wordpress.publish", true
		next.RequiredFields = []string{"task_id", "target_id", "action_plan_digest", "command_id", "current_time"}
		workflow, ok := record.LatestWorkflow()
		if !ok {
			return NextAction{}, ErrInvalidSession
		}
		next.EligibleTaskIDs = make([]string, 0, len(workflow.Tasks))
		for _, current := range workflow.Tasks {
			next.EligibleTaskIDs = append(next.EligibleTaskIDs, current.TaskID)
		}
	case StateActionAttentionRequired:
		next.Kind = NextInspectAction
		for index := len(record.Turns) - 1; index >= 0; index-- {
			if record.Turns[index].Action != nil {
				actionEvidence := record.Turns[index].Action
				next.Commands = []CommandReference{
					{Scope: "workspace", CommandID: actionEvidence.CommandID},
					{Scope: "project", ProjectName: actionEvidence.ProjectName, CommandID: actionEvidence.ActionCommandID},
				}
				break
			}
		}
	case StateActionCompleted:
		next.Kind = NextDone
	}
	return next, nil
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
		if turn.Action != nil {
			actionEvidence := cloneActionEvidence(*turn.Action)
			cloned.Turns[index].Action = &actionEvidence
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

// PlanValidationFailureReason is a sanitized, non-identifying
// classification of why validatePlanShape rejected an already-Normalized
// canonical ceoplan.Plan. It never carries raw Plan content.
type PlanValidationFailureReason string

const (
	PlanValidationMissingRequiredField    PlanValidationFailureReason = "missing_required_field"
	PlanValidationInvalidProposalSequence PlanValidationFailureReason = "invalid_proposal_sequence"
)

// PlanValidationError pairs ErrInvalidSession with a sanitized
// PlanValidationFailureReason, following the same pattern as
// ceoplan.ParseError/IntentParseError. Field is a sanitized contract field
// identifier (e.g. "proposed_tasks.title") and never carries an array
// index, a field value, or raw Plan content -- TaskIndex carries the
// index separately, set only when Field is itself scoped to one
// ProposedTask.
type PlanValidationError struct {
	Reason    PlanValidationFailureReason
	Field     string
	TaskIndex *int
	err       error
}

func (validationErr *PlanValidationError) Error() string { return validationErr.err.Error() }
func (validationErr *PlanValidationError) Unwrap() error { return validationErr.err }

func newPlanValidationError(reason PlanValidationFailureReason, field string, taskIndex *int) *PlanValidationError {
	return &PlanValidationError{
		Reason: reason, Field: field, TaskIndex: taskIndex,
		err: fmt.Errorf("%w: %s", ErrInvalidSession, field),
	}
}

// classifyPlanShapeFailure is validatePlanShape's single source of truth
// for both the pass/fail decision and the sanitized diagnostic explaining
// a failure -- there is no separate, duplicated condition list to drift
// out of sync. Check order matches the shape this package has always
// validated in, so which failure is reported first for a Plan violating
// multiple rules simultaneously is unchanged from before this diagnostic
// existed.
//
// Summary is deliberately not checked here: ADR-0046 made ceoplan's own
// NormalizeCandidate accept a blank Summary (the LLM may legitimately omit
// it, with no Go-owned fallback), and this package's shape check must
// accept exactly what ceoplan already declared valid rather than
// re-imposing a stricter, unsynced rule of its own.
func classifyPlanShapeFailure(plan ceoplan.Plan) *PlanValidationError {
	switch {
	case !plan.PlanOnly:
		return newPlanValidationError(PlanValidationMissingRequiredField, "plan_only", nil)
	case strings.TrimSpace(plan.ProjectName) == "":
		return newPlanValidationError(PlanValidationMissingRequiredField, "project_name", nil)
	case strings.TrimSpace(plan.Objective) == "":
		return newPlanValidationError(PlanValidationMissingRequiredField, "objective", nil)
	case plan.RequiredDepartments == nil:
		return newPlanValidationError(PlanValidationMissingRequiredField, "required_departments", nil)
	case plan.RequiredRoles == nil:
		return newPlanValidationError(PlanValidationMissingRequiredField, "required_roles", nil)
	case plan.AssignedExistingEmployees == nil:
		return newPlanValidationError(PlanValidationMissingRequiredField, "assigned_existing_employees", nil)
	case plan.MissingRoles == nil:
		return newPlanValidationError(PlanValidationMissingRequiredField, "missing_roles", nil)
	case plan.ProposedTasks == nil || len(plan.ProposedTasks) == 0:
		return newPlanValidationError(PlanValidationMissingRequiredField, "proposed_tasks", nil)
	case plan.Risks == nil:
		return newPlanValidationError(PlanValidationMissingRequiredField, "risks", nil)
	case plan.CEOQuestions == nil:
		return newPlanValidationError(PlanValidationMissingRequiredField, "ceo_questions", nil)
	}
	for index, task := range plan.ProposedTasks {
		taskIndex := index
		switch {
		case task.ProposalID != fmt.Sprintf("PROPOSED-%03d", index+1):
			return newPlanValidationError(PlanValidationInvalidProposalSequence, "proposed_tasks.proposal_id", &taskIndex)
		case strings.TrimSpace(task.Title) == "":
			return newPlanValidationError(PlanValidationMissingRequiredField, "proposed_tasks.title", &taskIndex)
		case strings.TrimSpace(task.Rationale) == "":
			return newPlanValidationError(PlanValidationMissingRequiredField, "proposed_tasks.rationale", &taskIndex)
		case task.DependencyIDs == nil:
			return newPlanValidationError(PlanValidationMissingRequiredField, "proposed_tasks.dependency_ids", &taskIndex)
		}
	}
	return nil
}

func validatePlanShape(plan ceoplan.Plan) error {
	if err := classifyPlanShapeFailure(plan); err != nil {
		return err
	}
	return nil
}

// validateIncrementalAnswers checks one durable batch of clarification
// answers against the questions still unanswered as of answeredCount
// (how many of questions[], in order, already have a prior recorded
// answer). A batch may answer as few as one question -- it no longer
// needs to cover every remaining question in a single call -- but must
// still be a contiguous, in-order continuation starting exactly at
// questions[answeredCount]: this is what lets Next() and the Conversation
// Projection always name a single, unambiguous "next question" from
// canonical evidence alone, with no reordering or gap-filling.
func validateIncrementalAnswers(questions []string, answeredCount int, answers []Answer) error {
	if len(questions) == 0 || answeredCount < 0 || answeredCount >= len(questions) ||
		len(answers) == 0 || answeredCount+len(answers) > len(questions) || ValidateAnswerPayload(answers) != nil {
		return ErrInvalidSession
	}
	for index, answer := range answers {
		if answer.Question != questions[answeredCount+index] {
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
	if evidence.Autonomy != nil && (evidence.Autonomy.Validate() != nil || evidence.Autonomy.ExecutionLimit != evidence.MaxTasks ||
		!evidence.Autonomy.AllowsEmployee(evidence.ReviewerID)) {
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

func validateActionEvidence(evidence ActionEvidence, projectID, projectName string, workflow WorkflowEvidence) error {
	if evidence.SchemaVersion != SchemaVersion || commandledger.ValidateCommandID(evidence.CommandID) != nil ||
		commandledger.ValidateCommandID(evidence.ActionCommandID) != nil || evidence.ProjectID != projectID || evidence.ProjectName != projectName ||
		strings.TrimSpace(evidence.TaskID) == "" || evidence.TaskID != strings.TrimSpace(evidence.TaskID) ||
		strings.TrimSpace(evidence.TargetID) == "" || evidence.TargetID != strings.TrimSpace(evidence.TargetID) ||
		action.ValidateSourceDigest(evidence.SourceSHA256) != nil || !evidence.Status.Valid() || !validDigest(evidence.ResultDigest) {
		return ErrInvalidSession
	}
	taskFound := false
	for _, current := range workflow.Tasks {
		if current.TaskID == evidence.TaskID {
			taskFound = true
			break
		}
	}
	if !taskFound {
		return ErrInvalidSession
	}
	requiresFailure := evidence.Status == ActionStatusFailed || evidence.Status == ActionStatusPartialFailure
	if requiresFailure != (evidence.Failure != nil) {
		return ErrInvalidSession
	}
	if evidence.Status == ActionStatusPublished {
		if evidence.Intent == nil || !evidence.Intent.Committed || evidence.Outcome == nil || !evidence.Outcome.Committed ||
			evidence.Publication == nil || strings.TrimSpace(evidence.Publication.Provider) == "" ||
			strings.TrimSpace(evidence.Publication.ExternalID) == "" || strings.TrimSpace(evidence.Publication.URL) == "" ||
			evidence.Publication.Status != "published" || strings.TrimSpace(evidence.EventID) == "" || !evidence.EventPublished {
			return ErrInvalidSession
		}
	}
	if evidence.Failure != nil {
		if strings.TrimSpace(evidence.Failure.Code) == "" || evidence.Failure.Code != strings.TrimSpace(evidence.Failure.Code) ||
			strings.TrimSpace(evidence.Failure.Stage) == "" || evidence.Failure.Stage != strings.TrimSpace(evidence.Failure.Stage) ||
			evidence.Failure.Partial != (evidence.Status == ActionStatusPartialFailure) {
			return ErrInvalidSession
		}
	}
	return nil
}

func cloneAnswers(answers []Answer) []Answer {
	if answers == nil {
		return nil
	}
	return append(make([]Answer, 0, len(answers)), answers...)
}

func cloneWorkflowEvidence(evidence WorkflowEvidence) WorkflowEvidence {
	cloned := evidence
	if evidence.Autonomy != nil {
		contract := evidence.Autonomy.Clone()
		cloned.Autonomy = &contract
	}
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

func cloneActionEvidence(evidence ActionEvidence) ActionEvidence {
	cloned := evidence
	if evidence.Intent != nil {
		intent := *evidence.Intent
		cloned.Intent = &intent
	}
	if evidence.Publication != nil {
		publication := *evidence.Publication
		cloned.Publication = &publication
	}
	if evidence.Outcome != nil {
		outcome := *evidence.Outcome
		cloned.Outcome = &outcome
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
