package interaction

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/ceoplan"
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
	if _, err := withPlan.RecordApplied("PROJECT-001", plan.ProjectName, withPlan.Turns[0].PlanDigest, at.Add(2*time.Minute)); !errors.Is(err, ErrInvalidState) {
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
	applied, err := readyForApproval.RecordApplied("PROJECT-001", finalPlan.ProjectName, digest, at.Add(4*time.Minute))
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
	if _, err := withPlan.RecordApplied("PROJECT-001", cleanPlan.ProjectName, "sha256:"+strings.Repeat("0", 64), at.Add(2*time.Minute)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("stale digest error = %v", err)
	}
	next, _ := withPlan.RecordApplied("PROJECT-001", cleanPlan.ProjectName, withPlan.Turns[0].PlanDigest, at.Add(2*time.Minute))
	rewritten := next.Clone()
	rewritten.Turns[0].At = rewritten.Turns[0].At.Add(time.Second)
	if err := ValidateTransition(withPlan, rewritten, withPlan.Version); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("history rewrite error = %v", err)
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

func interactionReadyRecord(t *testing.T, at time.Time) Record {
	t.Helper()
	record, _ := New("SESSION-WORKFLOW", "依頼", "Claude Sonnet 5", at)
	withPlan, _ := record.RecordPlan(interactionTestPlan([]string{}), at.Add(time.Minute))
	_, digest, _ := withPlan.CurrentPlan()
	ready, err := withPlan.RecordApplied("PROJECT-001", "案件", digest, at.Add(2*time.Minute))
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
