package interaction

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
)

func TestSessionRequiresClarificationBeforePlanApproval(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	record, err := New("SESSION-001", "Webアプリを作りたい", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	plan := interactionTestPlan([]string{"対象端末はWebだけですか"})
	withPlan, err := record.RecordPlan(plan, at.Add(time.Minute))
	if err != nil || withPlan.State != StateClarificationRequired || withPlan.Version != 2 {
		t.Fatalf("RecordPlan() = %#v, %v", withPlan, err)
	}
	if _, err := withPlan.RecordApplied("PROJECT-001", plan.ProjectName, withPlan.Turns[0].PlanDigest, "", at.Add(2*time.Minute)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("unanswered plan apply error = %v", err)
	}
	answered, err := withPlan.RecordAnswers([]Answer{{Question: plan.CEOQuestions[0], Answer: "はい"}}, at.Add(2*time.Minute))
	if err != nil || answered.State != StatePlanGenerationApprovalRequired {
		t.Fatalf("RecordAnswers() = %#v, %v", answered, err)
	}
	request, err := answered.PlanningRequest()
	if err != nil || !strings.Contains(request, `"answer":"はい"`) {
		t.Fatalf("PlanningRequest() = %q, %v", request, err)
	}
	finalPlan := interactionTestPlan([]string{})
	readyForApproval, err := answered.RecordPlan(finalPlan, at.Add(3*time.Minute))
	if err != nil || readyForApproval.State != StatePlanApprovalRequired {
		t.Fatalf("second RecordPlan() = %#v, %v", readyForApproval, err)
	}
	_, digest, _ := readyForApproval.CurrentPlan()
	applied, err := readyForApproval.RecordApplied("PROJECT-001", finalPlan.ProjectName, digest, "", at.Add(4*time.Minute))
	if err != nil || applied.State != StateReadyToExecute || applied.Version != 5 || applied.Validate() != nil {
		t.Fatalf("RecordApplied() = %#v, %v", applied, err)
	}
}

func TestSessionRejectsStaleDigestIncompleteAnswersAndHistoryRewrite(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	record, _ := New("SESSION-001", "依頼", "Claude Sonnet 5", at)
	withPlan, _ := record.RecordPlan(interactionTestPlan([]string{"Q1", "Q2"}), at.Add(time.Minute))
	if _, err := withPlan.RecordAnswers([]Answer{{Question: "Q1", Answer: "A1"}}, at.Add(2*time.Minute)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("incomplete answers error = %v", err)
	}
	cleanPlan := interactionTestPlan([]string{})
	record, _ = New("SESSION-002", "依頼", "Claude Sonnet 5", at)
	withPlan, _ = record.RecordPlan(cleanPlan, at.Add(time.Minute))
	if _, err := withPlan.RecordApplied("PROJECT-001", cleanPlan.ProjectName, "sha256:"+strings.Repeat("0", 64), "", at.Add(2*time.Minute)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("stale digest error = %v", err)
	}
	next, _ := withPlan.RecordApplied("PROJECT-001", cleanPlan.ProjectName, withPlan.Turns[0].PlanDigest, "", at.Add(2*time.Minute))
	rewritten := next.Clone()
	rewritten.Turns[0].At = rewritten.Turns[0].At.Add(time.Second)
	if err := ValidateTransition(withPlan, rewritten, withPlan.Version); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("history rewrite error = %v", err)
	}
}

// TestRecordPlanAcceptsBlankSummaryPerADR0046 is the CMD-B0BFC132
// investigation's regression: RecordPlan's own validatePlanShape
// previously rejected a blank Summary with the bare ErrInvalidState
// sentinel (surfacing as INTERACTION_PLAN_FAILED/interaction_plan_validation
// with no diagnostic detail), directly contradicting ceoplan.NormalizeCandidate,
// which ADR-0046 already made tolerant of a blank Summary (see
// ceoplan/plan_test.go's own regression for that side). This is the fix:
// interaction's own shape check must accept exactly what ceoplan already
// declared valid.
func TestRecordPlanAcceptsBlankSummaryPerADR0046(t *testing.T) {
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	record, _ := New("SESSION-BLANK-SUMMARY", "依頼", "Claude Sonnet 5", at)
	plan := interactionTestPlan([]string{})
	plan.Summary = ""
	withPlan, err := record.RecordPlan(plan, at.Add(time.Minute))
	if err != nil || withPlan.State != StatePlanApprovalRequired {
		t.Fatalf("RecordPlan() with blank Summary = %#v, %v, want success per ADR-0046", withPlan, err)
	}
}

// TestRecordPlanRejectsMalformedShapeWithSanitizedDiagnostic locks the
// CMD-B0BFC132 investigation's diagnostic addition: every remaining
// validatePlanShape rule still rejects a malformed canonical Plan (no
// rule was relaxed beyond the Summary fix above), and now does so with a
// typed, sanitized *PlanValidationError (Reason/Field/TaskIndex) instead
// of the bare, undiagnosable ErrInvalidState the interaction_plan_validation
// stage previously carried. None of these rules ever changes Plan content —
// each subtest starts from a known-valid Plan and breaks exactly one field.
func TestRecordPlanRejectsMalformedShapeWithSanitizedDiagnostic(t *testing.T) {
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	intPtr := func(value int) *int { return &value }

	for _, test := range []struct {
		name      string
		mutate    func(ceoplan.Plan) ceoplan.Plan
		reason    PlanValidationFailureReason
		field     string
		taskIndex *int
	}{
		{"plan_only false", func(plan ceoplan.Plan) ceoplan.Plan { plan.PlanOnly = false; return plan }, PlanValidationMissingRequiredField, "plan_only", nil},
		{"blank project_name", func(plan ceoplan.Plan) ceoplan.Plan { plan.ProjectName = "   "; return plan }, PlanValidationMissingRequiredField, "project_name", nil},
		{"blank objective", func(plan ceoplan.Plan) ceoplan.Plan { plan.Objective = ""; return plan }, PlanValidationMissingRequiredField, "objective", nil},
		{"nil required_departments", func(plan ceoplan.Plan) ceoplan.Plan { plan.RequiredDepartments = nil; return plan }, PlanValidationMissingRequiredField, "required_departments", nil},
		{"nil required_roles", func(plan ceoplan.Plan) ceoplan.Plan { plan.RequiredRoles = nil; return plan }, PlanValidationMissingRequiredField, "required_roles", nil},
		{"nil assigned_existing_employees", func(plan ceoplan.Plan) ceoplan.Plan { plan.AssignedExistingEmployees = nil; return plan }, PlanValidationMissingRequiredField, "assigned_existing_employees", nil},
		{"nil missing_roles", func(plan ceoplan.Plan) ceoplan.Plan { plan.MissingRoles = nil; return plan }, PlanValidationMissingRequiredField, "missing_roles", nil},
		{"empty proposed_tasks", func(plan ceoplan.Plan) ceoplan.Plan { plan.ProposedTasks = []ceoplan.ProposedTask{}; return plan }, PlanValidationMissingRequiredField, "proposed_tasks", nil},
		{"nil risks", func(plan ceoplan.Plan) ceoplan.Plan { plan.Risks = nil; return plan }, PlanValidationMissingRequiredField, "risks", nil},
		{"nil ceo_questions", func(plan ceoplan.Plan) ceoplan.Plan { plan.CEOQuestions = nil; return plan }, PlanValidationMissingRequiredField, "ceo_questions", nil},
		{
			"duplicate proposal id (second task out of sequence)",
			func(plan ceoplan.Plan) ceoplan.Plan {
				assignee := "PLAN-002"
				plan.ProposedTasks = append(plan.ProposedTasks, ceoplan.ProposedTask{
					ProposalID: "PROPOSED-001", Title: "second", Rationale: "second reason",
					AssigneeID: &assignee, DependencyIDs: []string{"PROPOSED-001"},
				})
				return plan
			},
			PlanValidationInvalidProposalSequence, "proposed_tasks.proposal_id", intPtr(1),
		},
		{"task blank title", func(plan ceoplan.Plan) ceoplan.Plan { plan.ProposedTasks[0].Title = "   "; return plan }, PlanValidationMissingRequiredField, "proposed_tasks.title", intPtr(0)},
		{"task blank rationale", func(plan ceoplan.Plan) ceoplan.Plan { plan.ProposedTasks[0].Rationale = ""; return plan }, PlanValidationMissingRequiredField, "proposed_tasks.rationale", intPtr(0)},
		{"task nil dependency_ids", func(plan ceoplan.Plan) ceoplan.Plan { plan.ProposedTasks[0].DependencyIDs = nil; return plan }, PlanValidationMissingRequiredField, "proposed_tasks.dependency_ids", intPtr(0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			record, _ := New("SESSION-SHAPE-TEST", "依頼", "Claude Sonnet 5", at)
			plan := test.mutate(interactionTestPlan([]string{}))
			_, err := record.RecordPlan(plan, at.Add(time.Minute))
			var validationErr *PlanValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("err = %v, want *PlanValidationError", err)
			}
			if validationErr.Reason != test.reason || validationErr.Field != test.field {
				t.Fatalf("reason/field = %q/%q, want %q/%q", validationErr.Reason, validationErr.Field, test.reason, test.field)
			}
			if (validationErr.TaskIndex == nil) != (test.taskIndex == nil) ||
				(validationErr.TaskIndex != nil && *validationErr.TaskIndex != *test.taskIndex) {
				t.Fatalf("TaskIndex = %v, want %v", validationErr.TaskIndex, test.taskIndex)
			}
			if !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("err does not wrap ErrInvalidSession: %v", err)
			}
		})
	}
}

// TestNextPointsPlanApprovalAtApproveAndExecute locks ADR-0049's core
// mechanism: the CEO's single "この内容で進める" approval is expressed as
// Record.Next() itself naming the merged operation, not a UI-level
// substitution -- any well-behaved client that reads next.operation
// dynamically (rather than hardcoding "interaction.plan.apply") is
// automatically routed onto the new chain.
func TestNextPointsPlanApprovalAtApproveAndExecute(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	record, _ := New("SESSION-001", "依頼", "Claude Sonnet 5", at)
	plan := interactionTestPlan([]string{})
	withPlan, err := record.RecordPlan(plan, at.Add(time.Minute))
	if err != nil || withPlan.State != StatePlanApprovalRequired {
		t.Fatalf("RecordPlan() = %#v, %v", withPlan, err)
	}
	next, err := withPlan.Next()
	if err != nil || next.Kind != NextApprovePlanApply || next.Operation != "interaction.plan.approve_and_execute" || !next.ApprovalRequired {
		t.Fatalf("Next() at plan_approval_required = %#v, %v", next, err)
	}
}

// TestPendingWorkflowPreAuthorizationDrivesNextWithoutSecondApproval covers
// tests 4-6 of the CP4 review: a TurnPlanApplied Turn carrying a
// pre-authorized Workflow Command ID (as ExecuteInteractionPlanApproveAndExecute
// itself records) leaves Next() pointing at that same Command for
// inspection, never asking for a fresh interaction.workflow.execute
// approval -- and the reverse case, the standalone interaction.plan.apply
// path (empty pre-authorization), keeps the pre-CP4 behavior of a fresh
// approval requirement unchanged.
func TestPendingWorkflowPreAuthorizationDrivesNextWithoutSecondApproval(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	plan := interactionTestPlan([]string{})

	record, _ := New("SESSION-PREAUTH-001", "依頼", "Claude Sonnet 5", at)
	withPlan, err := record.RecordPlan(plan, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_, digest, _ := withPlan.CurrentPlan()
	preAuthorized, err := withPlan.RecordApplied("PROJECT-001", plan.ProjectName, digest, "CMD-OUTER-APPROVE-AND-EXECUTE", at.Add(2*time.Minute))
	if err != nil || preAuthorized.State != StateReadyToExecute {
		t.Fatalf("pre-authorized RecordApplied() = %#v, %v", preAuthorized, err)
	}
	pendingCommandID, ok := preAuthorized.PendingWorkflowPreAuthorization()
	if !ok || pendingCommandID != "CMD-OUTER-APPROVE-AND-EXECUTE" {
		t.Fatalf("PendingWorkflowPreAuthorization() = %q, %t", pendingCommandID, ok)
	}
	next, err := preAuthorized.Next()
	if err != nil || next.Kind != NextInspectWorkflow || next.ApprovalRequired || next.Operation != "" ||
		len(next.Commands) != 1 || next.Commands[0].Scope != "workspace" || next.Commands[0].CommandID != "CMD-OUTER-APPROVE-AND-EXECUTE" {
		t.Fatalf("Next() with pending pre-authorization = %#v, %v", next, err)
	}

	standaloneRecord, _ := New("SESSION-PREAUTH-002", "依頼", "Claude Sonnet 5", at)
	standaloneWithPlan, err := standaloneRecord.RecordPlan(plan, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_, standaloneDigest, _ := standaloneWithPlan.CurrentPlan()
	standaloneApplied, err := standaloneWithPlan.RecordApplied("PROJECT-002", plan.ProjectName, standaloneDigest, "", at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := standaloneApplied.PendingWorkflowPreAuthorization(); ok {
		t.Fatalf("standalone plan.apply must not report a pending pre-authorization: %#v", standaloneApplied)
	}
	standaloneNext, err := standaloneApplied.Next()
	if err != nil || standaloneNext.Kind != NextApproveWorkflow || standaloneNext.Operation != "interaction.workflow.execute" || !standaloneNext.ApprovalRequired {
		t.Fatalf("standalone Next() after plan.apply = %#v, %v", standaloneNext, err)
	}
}

// TestRecordAppliedRejectsMalformedPreAuthorizationCommandID confirms the
// pre-authorization marker is validated exactly like every other durable
// Command ID reference in this package, not accepted as an opaque string.
func TestRecordAppliedRejectsMalformedPreAuthorizationCommandID(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	record, _ := New("SESSION-001", "依頼", "Claude Sonnet 5", at)
	plan := interactionTestPlan([]string{})
	withPlan, err := record.RecordPlan(plan, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_, digest, _ := withPlan.CurrentPlan()
	if _, err := withPlan.RecordApplied("PROJECT-001", plan.ProjectName, digest, "not a valid command id", at.Add(2*time.Minute)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("malformed pre-authorization Command ID error = %v", err)
	}
}

func TestSessionStartFixturePinsRequestDigest(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "interaction", "session_start_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		PlanRequest struct {
			SessionID   string    `json:"session_id"`
			Request     string    `json:"request"`
			Model       string    `json:"model"`
			CurrentTime time.Time `json:"current_time"`
		} `json:"plan_request"`
		ExpectedRequestDigest string `json:"expected_request_digest"`
	}
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatal(err)
	}
	record, err := New(fixture.PlanRequest.SessionID, fixture.PlanRequest.Request, fixture.PlanRequest.Model, fixture.PlanRequest.CurrentTime)
	if err != nil || record.RequestDigest != fixture.ExpectedRequestDigest {
		t.Fatalf("fixture digest = %s, record = %#v, err = %v", fixture.ExpectedRequestDigest, record, err)
	}
}

func TestAnswersAreCanonicalizedAndPreserveUnicodeSpecialCharacters(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	record, _ := New("SESSION-UNICODE", "日本語\n# 依頼 | 詳細", "Claude Sonnet 5", at)
	withPlan, err := record.RecordPlan(interactionTestPlan([]string{"Q1 | #", "質問二"}), at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	answered, err := withPlan.RecordAnswers([]Answer{
		{Question: "質問二", Answer: "回答二\n改行"},
		{Question: "Q1 | #", Answer: "A1 | # 😀"},
	}, at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	turn := answered.Turns[len(answered.Turns)-1]
	if turn.Answers[0].Question != "Q1 | #" || turn.Answers[1].Question != "質問二" {
		t.Fatalf("answer order = %#v", turn.Answers)
	}
	planningRequest, err := answered.PlanningRequest()
	if err != nil || !strings.Contains(planningRequest, "😀") || !strings.Contains(planningRequest, `\n`) {
		t.Fatalf("PlanningRequest() = %q, %v", planningRequest, err)
	}
}

func TestWorkflowEvidenceCompletesOrKeepsExplicitContinuation(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	ready := interactionReadyRecord(t, at)
	blocked := interactionWorkflowEvidence(WorkflowStatusBlocked)
	blocked.Next = &WorkflowNextEvidence{Action: "wait", TaskID: "TASK-002", BlockingReasons: []string{"dependency_incomplete"}}
	continued, err := ready.RecordWorkflow(blocked, at.Add(3*time.Minute))
	if err != nil || continued.State != StateReadyToExecute || continued.Version != ready.Version+1 {
		t.Fatalf("blocked RecordWorkflow() = %#v, %v", continued, err)
	}
	completed := interactionWorkflowEvidence(WorkflowStatusCompleted)
	finished, err := continued.RecordWorkflow(completed, at.Add(4*time.Minute))
	if err != nil || finished.State != StateCompleted || finished.Validate() != nil {
		t.Fatalf("completed RecordWorkflow() = %#v, %v", finished, err)
	}
	if _, err := finished.RecordWorkflow(completed, at.Add(5*time.Minute)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("terminal workflow append error = %v", err)
	}
	rewritten := finished.Clone()
	rewritten.Turns[len(rewritten.Turns)-2].Workflow.ResultDigest = "sha256:" + strings.Repeat("f", 64)
	if err := ValidateTransition(continued, rewritten, continued.Version); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("workflow evidence rewrite error = %v", err)
	}
}

func TestWorkflowFailureRequiresTypedEvidenceAndStopsSession(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	ready := interactionReadyRecord(t, at)
	failed := interactionWorkflowEvidence(WorkflowStatusPartialFailure)
	failed.Failure = &WorkflowFailure{Code: "REVIEWED_WORKFLOW_FAILED", Stage: "review", Partial: true}
	attention, err := ready.RecordWorkflow(failed, at.Add(3*time.Minute))
	if err != nil || attention.State != StateWorkflowAttentionRequired {
		t.Fatalf("partial RecordWorkflow() = %#v, %v", attention, err)
	}
	invalid := interactionWorkflowEvidence(WorkflowStatusFailed)
	if _, err := ready.RecordWorkflow(invalid, at.Add(3*time.Minute)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("missing failure evidence error = %v", err)
	}
}

func TestNextActionProjectionGuidesApprovalCompletionAndRecovery(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	ready := interactionReadyRecord(t, at)
	next, err := ready.Next()
	if err != nil || next.Kind != NextApproveWorkflow || next.Operation != "interaction.workflow.execute" || !next.ApprovalRequired ||
		next.ExpectedVersion != ready.Version || next.ProjectID != "PROJECT-001" {
		t.Fatalf("ready Next() = %#v, %v", next, err)
	}
	completedEvidence := interactionWorkflowEvidence(WorkflowStatusCompleted)
	completedEvidence.Tasks = []WorkflowTaskEvidence{{
		TaskID: "TASK-001", ExecutionCommandID: "CMD-TASK-001", ReviewCommandID: "CMD-REVIEW-001", Verdict: review.VerdictApprove,
	}}
	completed, err := ready.RecordWorkflow(completedEvidence, at.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	next, err = completed.Next()
	if err != nil || next.Kind != NextOptionalAction || len(next.EligibleTaskIDs) != 1 || next.EligibleTaskIDs[0] != "TASK-001" {
		t.Fatalf("completed Next() = %#v, %v", next, err)
	}
	actionAttention, err := completed.RecordAction(ActionEvidence{
		SchemaVersion: 1, CommandID: "CMD-INTERACTION-ACTION", ActionCommandID: "CMD-ACTION-CHILD",
		ProjectID: "PROJECT-001", ProjectName: "案件", TaskID: "TASK-001", TargetID: "site-main",
		SourceSHA256: strings.Repeat("0", 64), Status: ActionStatusFailed,
		ResultDigest: "sha256:" + strings.Repeat("1", 64),
		Failure:      &WorkflowFailure{Code: "ACTION_CONFIG_INVALID", Stage: "action_configuration", Partial: false},
	}, at.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	next, err = actionAttention.Next()
	if err != nil || next.Kind != NextInspectAction || len(next.Commands) != 2 || next.Commands[1].Scope != "project" {
		t.Fatalf("Action attention Next() = %#v, %v", next, err)
	}
}

func interactionReadyRecord(t *testing.T, at time.Time) Record {
	t.Helper()
	record, _ := New("SESSION-WORKFLOW", "依頼", "Claude Sonnet 5", at)
	withPlan, _ := record.RecordPlan(interactionTestPlan([]string{}), at.Add(time.Minute))
	_, digest, _ := withPlan.CurrentPlan()
	ready, err := withPlan.RecordApplied("PROJECT-001", "案件", digest, "", at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return ready
}

func interactionWorkflowEvidence(status WorkflowStatus) WorkflowEvidence {
	return WorkflowEvidence{
		SchemaVersion: 1, CommandID: "CMD-INTERACTION-WORKFLOW-001", WorkflowCommandID: "CMD-WORKFLOW-CHILD-001",
		ProjectID: "PROJECT-001", ProjectName: "案件", ReviewerID: "QA-001", MaxTasks: 10,
		Status: status, ResultDigest: "sha256:" + strings.Repeat("0", 64), Tasks: []WorkflowTaskEvidence{},
	}
}

func interactionTestPlan(questions []string) ceoplan.Plan {
	assignee := "PLAN-001"
	return ceoplan.Plan{
		ProjectName: "案件", Objective: "目的", Summary: "概要",
		RequiredDepartments: []string{"企画部"}, RequiredRoles: []string{"Planner"},
		AssignedExistingEmployees: []string{assignee}, MissingRoles: []string{},
		ProposedTasks: []ceoplan.ProposedTask{{
			ProposalID: "PROPOSED-001", Title: "計画する", AssigneeID: &assignee,
			DependencyIDs: []string{}, Rationale: "必要なため",
		}},
		Risks: []string{}, CEOQuestions: questions, PlanOnly: true,
	}
}
