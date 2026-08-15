package process

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

// TestCEORequestEntryProjectsCanonicalRequestVerbatim locks test item 1:
// the CEO's original request becomes a CEO Message, trimmed but never
// summarized, with no Speaker/Recipient/Subject (Category alone means
// "the CEO said this").
func TestCEORequestEntryProjectsCanonicalRequestVerbatim(t *testing.T) {
	at := time.Date(2026, time.August, 9, 9, 0, 0, 0, time.UTC)
	record := interaction.Record{Request: "  りんごの紹介文を作成して  ", CreatedAt: at}
	entry := ceoRequestEntry(record)
	if entry.Category != CategoryCEOMessage || entry.Kind != KindCEORequest || entry.CEOMessageText != "りんごの紹介文を作成して" ||
		!entry.At.Equal(at) || entry.Speaker != nil || entry.Recipient != nil || entry.Subject != nil {
		t.Fatalf("ceoRequestEntry() = %#v", entry)
	}
}

// TestClarificationAnswerEntriesProjectOnlyConfirmedAnswers locks test
// item 2: a confirmed clarification answer becomes a CEO Message; a blank
// answer (no canonical text) is not projected at all.
func TestClarificationAnswerEntriesProjectOnlyConfirmedAnswers(t *testing.T) {
	at := time.Date(2026, time.August, 9, 9, 5, 0, 0, time.UTC)
	turn := interaction.Turn{
		Kind: interaction.TurnClarificationAnswered, At: at,
		Answers: []interaction.Answer{
			{Question: "対象読者は？", Answer: "  20代女性向け  "},
			{Question: "予算は？", Answer: "   "},
		},
	}
	entries := clarificationAnswerEntries(turn)
	if len(entries) != 1 || entries[0].Category != CategoryCEOMessage || entries[0].Kind != KindCEOClarificationAnswer ||
		entries[0].CEOMessageText != "20代女性向け" || !entries[0].At.Equal(at) ||
		entries[0].Speaker != nil || entries[0].Recipient != nil {
		t.Fatalf("clarificationAnswerEntries() = %#v", entries)
	}
}

// TestClarificationRequestedEntriesProjectAllQuestionsVerbatim locks the
// Public Beta Conversation UX Fix's root-cause repair: InspectConversation
// previously never projected the clarification question itself (only the
// later answer), because its per-Turn switch had no case for
// TurnPlanGenerated. clarificationRequestedEntries projects every
// non-blank CEOQuestion from the already-committed canonical Plan verbatim
// -- never composed or paraphrased -- and skips a blank one exactly like
// clarificationAnswerEntries already skips a blank answer.
func TestClarificationRequestedEntriesProjectAllQuestionsVerbatim(t *testing.T) {
	at := time.Date(2026, time.August, 9, 9, 4, 0, 0, time.UTC)
	plan := ceoplan.Plan{CEOQuestions: []string{"対象読者は既存顧客と新規顧客のどちらですか？", "   "}}
	turn := interaction.Turn{Kind: interaction.TurnPlanGenerated, At: at, Plan: &plan}

	entries := clarificationRequestedEntries(turn)
	if len(entries) != 1 || entries[0].Category != CategorySystem || entries[0].Kind != KindClarificationRequested ||
		entries[0].Question != "対象読者は既存顧客と新規顧客のどちらですか？" || !entries[0].At.Equal(at) ||
		entries[0].Audience != AudienceCEO || entries[0].Speaker != nil || entries[0].Recipient != nil {
		t.Fatalf("clarificationRequestedEntries() = %#v", entries)
	}
	if entries := clarificationRequestedEntries(interaction.Turn{Kind: interaction.TurnPlanGenerated, At: at, Plan: nil}); entries != nil {
		t.Fatalf("clarificationRequestedEntries() with nil Plan = %#v, want nil", entries)
	}
}

// TestInspectConversationKeepsBothClarificationQuestionAndAnswerAfterReload
// is the read-only, Vault-backed regression lock for the same fix: a
// Session that asked one clarification question and received the CEO's
// confirmed answer must show both as separate entries, in the order
// WorkCairn actually asked and the CEO actually answered -- and this must
// survive a completely fresh InspectConversation call against the same
// Vault (the read-only equivalent of a page reload or daemon restart,
// since this package holds no in-process cache).
func TestInspectConversationKeepsBothClarificationQuestionAndAnswerAfterReload(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "社員"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, time.August, 9, 9, 0, 0, 0, time.UTC)
	record, err := interaction.New("SESSION-CLARIFY-RELOAD-001", "りんごについて100文字程度で説明して", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	withQuestion, err := record.RecordPlan(ceoplan.Plan{
		ProjectName: "りんご説明文", Objective: "目的", Summary: "概要",
		RequiredDepartments: []string{}, RequiredRoles: []string{}, AssignedExistingEmployees: []string{}, MissingRoles: []string{},
		ProposedTasks: []ceoplan.ProposedTask{{ProposalID: "PROPOSED-001", Title: "説明文を作成する", DependencyIDs: []string{}, Rationale: "必要"}},
		Risks:         []string{}, CEOQuestions: []string{"対象読者は既存顧客と新規顧客のどちらですか？"}, PlanOnly: true,
	}, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	answered, err := withQuestion.RecordAnswers([]interaction.Answer{
		{Question: "対象読者は既存顧客と新規顧客のどちらですか？", Answer: "新規顧客向けでお願いします。"},
	}, at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	store, err := vault.NewInteractionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), withQuestion, record.Version); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), answered, withQuestion.Version); err != nil {
		t.Fatal(err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		entries, err := InspectConversation(context.Background(), root, "SESSION-CLARIFY-RELOAD-001")
		if err != nil {
			t.Fatal(err)
		}
		question := findKind(entries, KindClarificationRequested)
		answer := findKind(entries, KindCEOClarificationAnswer)
		if question == nil || answer == nil {
			t.Fatalf("attempt %d: question or answer entry missing: %#v", attempt, entries)
		}
		if question.Question != "対象読者は既存顧客と新規顧客のどちらですか？" || question.Category != CategorySystem {
			t.Fatalf("attempt %d: question entry = %#v", attempt, question)
		}
		if answer.CEOMessageText != "新規顧客向けでお願いします。" || answer.Category != CategoryCEOMessage {
			t.Fatalf("attempt %d: answer entry = %#v", attempt, answer)
		}
		if question.At.After(answer.At) {
			t.Fatalf("attempt %d: question must not sort after its own answer: question=%v answer=%v", attempt, question.At, answer.At)
		}
		questionIndex, answerIndex := -1, -1
		for index, entry := range entries {
			if entry.Kind == KindClarificationRequested {
				questionIndex = index
			}
			if entry.Kind == KindCEOClarificationAnswer {
				answerIndex = index
			}
		}
		if questionIndex < 0 || answerIndex < 0 || questionIndex > answerIndex {
			t.Fatalf("attempt %d: question entry (index %d) must precede its answer entry (index %d)", attempt, questionIndex, answerIndex)
		}
	}
}

// TestTaskAssignedEntriesNeverFabricatesSpeaker locks test item 7: Task
// assignment is TaskService's own act (Article 6), never a human's
// utterance — this must always be a Company Fact with Subject only, never
// a Speaker.
func TestTaskAssignedEntriesNeverFabricatesSpeaker(t *testing.T) {
	at := time.Date(2026, time.August, 9, 10, 0, 0, 0, time.UTC)
	assignee := "CONTENT-001"
	plan := ceoplan.Plan{ProposedTasks: []ceoplan.ProposedTask{{Title: "紹介文を作成する", AssigneeID: &assignee}}}
	employeesByID := map[string]organization.Identity{assignee: {ID: assignee, Name: "佐藤 葵"}}
	entries := taskAssignedEntries(interaction.Turn{At: at}, plan, employeesByID)
	if len(entries) != 1 || entries[0].Category != CategoryCompanyFact || entries[0].Kind != KindTaskAssigned ||
		entries[0].Speaker != nil || entries[0].Recipient != nil ||
		entries[0].Subject == nil || entries[0].Subject.EmployeeID != assignee {
		t.Fatalf("taskAssignedEntries() = %#v", entries)
	}
}

func requestChangesEvidence(makerID string) TaskEvidenceInspection {
	assignee := makerID
	return TaskEvidenceInspection{
		Task:        task.Task{ID: "TASK-001", Title: "紹介文を作成する", AssigneeID: &assignee, Status: task.StatusInProgress},
		Deliverable: &DeliverableInspection{RelativePath: "Deliverables/TASK-001.md"},
		Reviews: []ReviewInspection{{
			CanonicalPath: "Reviews/TASK-001.review.json",
			Decision: review.Decision{
				Verdict: review.VerdictRequestChanges,
				Issues: []review.Issue{{
					Category: "requirements", Severity: "medium",
					Description: "要件が不足しています。", SuggestedAction: "要件を追記してください。",
				}},
				Summary: "要件不足のため修正を依頼します。",
			},
		}},
	}
}

func findKind(entries []ConversationEntry, kind ConversationKind) *ConversationEntry {
	for index := range entries {
		if entries[index].Kind == kind {
			return &entries[index]
		}
	}
	return nil
}

// TestWorkflowTaskConversationEntriesRequestChangesIsDirectedCommunication
// locks test items 3 and 5: when both Reviewer and Maker are canonically
// confirmed, Request Changes becomes Directed Communication
// (Reviewer -> Maker), and Review issues/suggested actions/summary are
// carried through losslessly (byte-identical to the canonical Decision).
func TestWorkflowTaskConversationEntriesRequestChangesIsDirectedCommunication(t *testing.T) {
	at := time.Date(2026, time.August, 9, 10, 0, 0, 0, time.UTC)
	maker := organization.Identity{ID: "CONTENT-001", Name: "佐藤 葵", Role: "Content Writer"}
	reviewer := organization.Identity{ID: "QA-001", Name: "伊藤 健太", Role: "QA Engineer"}
	employeesByID := map[string]organization.Identity{maker.ID: maker, reviewer.ID: reviewer}
	evidence := requestChangesEvidence(maker.ID)
	workflowTask := interaction.WorkflowTaskEvidence{TaskID: "TASK-001", ExecutionCommandID: "CMD-EXEC-1", ReviewCommandID: "CMD-REVIEW-1", Verdict: review.VerdictRequestChanges}

	entries := workflowTaskConversationEntries(at, workflowTask, reviewer, employeesByID, evidence)
	directed := findKind(entries, KindReviewRequestChanges)
	if directed == nil {
		t.Fatal("no review_request_changes entry produced")
	}
	if directed.Category != CategoryDirectedCommunication || directed.Speaker == nil || directed.Recipient == nil ||
		directed.Speaker.EmployeeID != reviewer.ID || directed.Recipient.EmployeeID != maker.ID || !directed.MentionAllowed() {
		t.Fatalf("directed entry = %#v", directed)
	}
	wantIssues := evidence.Reviews[0].Decision.Issues
	if !reflect.DeepEqual(directed.ReviewIssues, wantIssues) || directed.ReviewSummary != evidence.Reviews[0].Decision.Summary {
		t.Fatalf("review issues/summary not projected losslessly: got issues=%#v summary=%q, want issues=%#v summary=%q",
			directed.ReviewIssues, directed.ReviewSummary, wantIssues, evidence.Reviews[0].Decision.Summary)
	}
}

// TestWorkflowTaskConversationEntriesApprovedIsCompanyFact locks test item
// 4: Approve is a completion fact, not actionable feedback addressed at a
// specific recipient — it stays Company Fact even when both Reviewer and
// Maker are confirmed.
func TestWorkflowTaskConversationEntriesApprovedIsCompanyFact(t *testing.T) {
	at := time.Date(2026, time.August, 9, 11, 0, 0, 0, time.UTC)
	maker := organization.Identity{ID: "CONTENT-001", Name: "佐藤 葵"}
	reviewer := organization.Identity{ID: "QA-001", Name: "伊藤 健太"}
	employeesByID := map[string]organization.Identity{maker.ID: maker, reviewer.ID: reviewer}
	assignee := maker.ID
	evidence := TaskEvidenceInspection{
		Task:        task.Task{ID: "TASK-001", Title: "紹介文を作成する", AssigneeID: &assignee, Status: task.StatusCompleted},
		Deliverable: &DeliverableInspection{RelativePath: "Deliverables/TASK-001.md"},
		Reviews:     []ReviewInspection{{Decision: review.Decision{Verdict: review.VerdictApprove, Issues: []review.Issue{}, Summary: "問題ありません。"}}},
	}
	workflowTask := interaction.WorkflowTaskEvidence{TaskID: "TASK-001", Verdict: review.VerdictApprove}

	entries := workflowTaskConversationEntries(at, workflowTask, reviewer, employeesByID, evidence)
	approved := findKind(entries, KindReviewApproved)
	if approved == nil {
		t.Fatal("no review_approved entry produced")
	}
	if approved.Category != CategoryCompanyFact || approved.Speaker != nil || approved.Recipient != nil || approved.MentionAllowed() ||
		approved.Subject == nil || approved.Subject.EmployeeID != reviewer.ID {
		t.Fatalf("approved entry = %#v", approved)
	}
	// Pins the exact canonical review_summary value that web/app.js's
	// companyFactText() reads for the "review_approved" case -- this is the
	// field a UI regression could silently swap for an internal status/kind
	// value (Public Beta Conversation UX Fix, item 2).
	if approved.ReviewSummary != "問題ありません。" {
		t.Fatalf("review_summary not projected verbatim: got %q, want %q", approved.ReviewSummary, "問題ありません。")
	}
}

// TestWorkflowTaskConversationEntriesUnknownSpeakerPreventsDirected locks
// test item 6: Recipient (Maker) confirmed but Speaker (Reviewer)
// unresolved must never become Directed Communication.
func TestWorkflowTaskConversationEntriesUnknownSpeakerPreventsDirected(t *testing.T) {
	at := time.Date(2026, time.August, 9, 10, 0, 0, 0, time.UTC)
	maker := organization.Identity{ID: "CONTENT-001", Name: "佐藤 葵"}
	employeesByID := map[string]organization.Identity{maker.ID: maker}
	unresolvedReviewer := organization.Identity{}
	evidence := requestChangesEvidence(maker.ID)
	workflowTask := interaction.WorkflowTaskEvidence{TaskID: "TASK-001", Verdict: review.VerdictRequestChanges}

	entries := workflowTaskConversationEntries(at, workflowTask, unresolvedReviewer, employeesByID, evidence)
	entry := findKind(entries, KindReviewRequestChanges)
	if entry == nil {
		t.Fatal("no review_request_changes entry produced")
	}
	if entry.Category == CategoryDirectedCommunication || entry.MentionAllowed() || entry.Speaker != nil || entry.Recipient != nil {
		t.Fatalf("Directed Communication fabricated despite unknown Speaker: %#v", entry)
	}
	// Falling back to Company Fact must still carry the real canonical
	// issues/summary -- web/app.js's companyFactText() "review_request_changes"
	// case renders exactly these fields, never a bare status/kind value
	// (Public Beta Conversation UX Fix, item 2).
	wantIssues := evidence.Reviews[0].Decision.Issues
	if !reflect.DeepEqual(entry.ReviewIssues, wantIssues) || entry.ReviewSummary != evidence.Reviews[0].Decision.Summary {
		t.Fatalf("review issues/summary not projected losslessly on Company Fact fallback: got issues=%#v summary=%q, want issues=%#v summary=%q",
			entry.ReviewIssues, entry.ReviewSummary, wantIssues, evidence.Reviews[0].Decision.Summary)
	}
}

// TestWorkflowTaskConversationEntriesDeliverableNeverFabricatesCEORecipient
// locks test item 8: Deliverable ready never carries a Recipient — there
// is no canonical evidence of the deliverable being "addressed to" the CEO.
func TestWorkflowTaskConversationEntriesDeliverableNeverFabricatesCEORecipient(t *testing.T) {
	at := time.Date(2026, time.August, 9, 10, 0, 0, 0, time.UTC)
	maker := organization.Identity{ID: "CONTENT-001", Name: "佐藤 葵"}
	employeesByID := map[string]organization.Identity{maker.ID: maker}
	assignee := maker.ID
	evidence := TaskEvidenceInspection{
		Task:        task.Task{ID: "TASK-001", AssigneeID: &assignee, Status: task.StatusInProgress},
		Deliverable: &DeliverableInspection{RelativePath: "Deliverables/TASK-001.md"},
	}
	workflowTask := interaction.WorkflowTaskEvidence{TaskID: "TASK-001"}

	entries := workflowTaskConversationEntries(at, workflowTask, organization.Identity{}, employeesByID, evidence)
	entry := findKind(entries, KindDeliverableReady)
	if entry == nil {
		t.Fatal("no deliverable_ready entry produced")
	}
	if entry.Category != CategoryCompanyFact || entry.Recipient != nil || entry.Speaker != nil {
		t.Fatalf("deliverable_ready entry = %#v", entry)
	}
}

// TestWorkflowTaskConversationEntriesRevisionWithoutConfirmedReviewer
// locks test item 9: a Revision-submitted fact must not become a
// mentionable Directed Communication when the Reviewer could not be
// resolved.
func TestWorkflowTaskConversationEntriesRevisionWithoutConfirmedReviewer(t *testing.T) {
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	maker := organization.Identity{ID: "CONTENT-001", Name: "佐藤 葵"}
	employeesByID := map[string]organization.Identity{maker.ID: maker}
	unresolvedReviewer := organization.Identity{}
	assignee := maker.ID
	evidence := TaskEvidenceInspection{Task: task.Task{ID: "TASK-003", AssigneeID: &assignee, Status: task.StatusInProgress}}
	workflowTask := interaction.WorkflowTaskEvidence{TaskID: "TASK-003", TargetedRevision: true}

	entries := workflowTaskConversationEntries(at, workflowTask, unresolvedReviewer, employeesByID, evidence)
	entry := findKind(entries, KindRevisionCompleted)
	if entry == nil {
		t.Fatal("no revision_completed entry produced")
	}
	if entry.Category == CategoryDirectedCommunication || entry.MentionAllowed() || entry.Speaker != nil || entry.Recipient != nil {
		t.Fatalf("Directed Communication fabricated despite unresolved Reviewer: %#v", entry)
	}
	if entry.Category != CategoryCompanyFact || entry.Subject == nil || entry.Subject.EmployeeID != maker.ID {
		t.Fatalf("revision_completed Company Fact = %#v", entry)
	}
}

// TestFailureEntryForwardsSanitizedEnvelopeUnchanged locks test item 10:
// the existing sanitized failure.Envelope is forwarded verbatim, never
// re-derived or enriched, and a nil Envelope produces no entry.
func TestFailureEntryForwardsSanitizedEnvelopeUnchanged(t *testing.T) {
	at := time.Date(2026, time.August, 9, 13, 0, 0, 0, time.UTC)
	envelope := &failure.Envelope{SchemaVersion: failure.SchemaVersion, Code: "PROVIDER_RATE_LIMITED", Stage: "review_provider", Partial: true}
	entry := failureEntry(at, envelope)
	if entry == nil || entry.Category != CategorySystem || entry.Kind != KindFailure || entry.FailureDetails != envelope {
		t.Fatalf("failureEntry() = %#v", entry)
	}
	if failureEntry(at, nil) != nil {
		t.Fatal("failureEntry(nil) must return nil, not a System entry with no Details")
	}
}

// TestWorkflowTaskConversationEntriesOrderIsFixedForSharedTimestamp locks
// point 1 of the CP3 review: multiple entries sharing the exact same
// canonical timestamp (Deliverable ready, the Review verdict, Task
// completed all share one Turn.At) must still appear in a fixed,
// reproducible order — the canonical Task/Review array order, never a Go
// map iteration order. Runs the pure function many times to surface any
// hidden non-determinism (Go deliberately randomizes map iteration order
// per range, so a lurking map-order dependency would very likely show up
// across repeated calls in the same process).
func TestWorkflowTaskConversationEntriesOrderIsFixedForSharedTimestamp(t *testing.T) {
	at := time.Date(2026, time.August, 9, 10, 0, 0, 0, time.UTC)
	maker := organization.Identity{ID: "CONTENT-001", Name: "佐藤 葵"}
	reviewer := organization.Identity{ID: "QA-001", Name: "伊藤 健太"}
	employeesByID := map[string]organization.Identity{maker.ID: maker, reviewer.ID: reviewer}
	assignee := maker.ID
	evidence := TaskEvidenceInspection{
		Task:        task.Task{ID: "TASK-001", Title: "紹介文を作成する", AssigneeID: &assignee, Status: task.StatusCompleted},
		Deliverable: &DeliverableInspection{RelativePath: "Deliverables/TASK-001.md"},
		Reviews: []ReviewInspection{{
			Decision: review.Decision{
				Verdict: review.VerdictRequestChanges,
				Issues:  []review.Issue{{Category: "requirements", Severity: "medium", Description: "要件が不足しています。", SuggestedAction: "要件を追記してください。"}},
				Summary: "要件不足のため修正を依頼します。",
			},
		}},
	}
	workflowTask := interaction.WorkflowTaskEvidence{TaskID: "TASK-001", Verdict: review.VerdictRequestChanges}
	wantKinds := []ConversationKind{KindDeliverableReady, KindReviewRequestChanges, KindTaskCompleted}

	for iteration := 0; iteration < 20; iteration++ {
		entries := workflowTaskConversationEntries(at, workflowTask, reviewer, employeesByID, evidence)
		kinds := make([]ConversationKind, len(entries))
		for index, entry := range entries {
			kinds[index] = entry.Kind
			if !entry.At.Equal(at) {
				t.Fatalf("iteration %d: entry %d At = %v, want %v (all entries in this call share one Turn timestamp)", iteration, index, entry.At, at)
			}
		}
		if !reflect.DeepEqual(kinds, wantKinds) {
			t.Fatalf("iteration %d: Kind order = %v, want %v", iteration, kinds, wantKinds)
		}
	}
}

// TestWorkflowTaskConversationEntriesReviewerNeverCarriesAcrossTurns locks
// point 2 of the CP3 review: WorkflowEvidence.ReviewerID is fixed only
// for the single Command execution recorded in one Turn (verified against
// process/reviewed_workflow.go: the reviewer closure captures exactly one
// ReviewerID value for the whole ExecuteReviewedWorkflow call). This test
// proves the caller-facing contract InspectConversation actually relies
// on: each Turn's own Reviewer must be used for that Turn's entries,
// never a Reviewer resolved for a different Turn. workflowTaskConversationEntries
// itself takes Reviewer as an explicit per-call argument specifically so
// InspectConversation's per-Turn loop can never accidentally reuse a
// stale value from an earlier Turn.
func TestWorkflowTaskConversationEntriesReviewerNeverCarriesAcrossTurns(t *testing.T) {
	at := time.Date(2026, time.August, 9, 10, 0, 0, 0, time.UTC)
	maker := organization.Identity{ID: "CONTENT-001", Name: "佐藤 葵"}
	firstReviewer := organization.Identity{ID: "QA-001", Name: "伊藤 健太"}
	secondReviewer := organization.Identity{ID: "QA-002", Name: "山田 太郎"}
	employeesByID := map[string]organization.Identity{maker.ID: maker, firstReviewer.ID: firstReviewer, secondReviewer.ID: secondReviewer}
	assignee := maker.ID
	evidence := TaskEvidenceInspection{Task: task.Task{ID: "TASK-003", AssigneeID: &assignee, Status: task.StatusInProgress}}
	workflowTask := interaction.WorkflowTaskEvidence{TaskID: "TASK-003", TargetedRevision: true}

	firstTurnEntries := workflowTaskConversationEntries(at, workflowTask, firstReviewer, employeesByID, evidence)
	secondTurnEntries := workflowTaskConversationEntries(at, workflowTask, secondReviewer, employeesByID, evidence)

	firstRevision := findKind(firstTurnEntries, KindRevisionCompleted)
	secondRevision := findKind(secondTurnEntries, KindRevisionCompleted)
	if firstRevision == nil || secondRevision == nil {
		t.Fatal("revision_completed entry missing")
	}
	if firstRevision.Recipient == nil || firstRevision.Recipient.EmployeeID != firstReviewer.ID {
		t.Fatalf("first Turn's revision_completed Recipient = %#v, want %s", firstRevision.Recipient, firstReviewer.ID)
	}
	if secondRevision.Recipient == nil || secondRevision.Recipient.EmployeeID != secondReviewer.ID {
		t.Fatalf("second Turn's revision_completed Recipient = %#v, want %s (must not carry the first Turn's Reviewer forward)", secondRevision.Recipient, secondReviewer.ID)
	}
}

// TestInspectConversationProjectsFullReviewedWorkflowDeterministically is
// the read-only, Vault-backed integration test covering test items 1, 2,
// 11, 12, and 13: it drives the real product path (Interaction Workflow
// Plan -> Execute, with a genuine Request Changes -> Revision -> re-Review
// -> Approve sequence, reusing the same fixture as
// TestInteractionWorkflowTemporaryVaultReviewRevisionAndReplay), then
// proves InspectConversation performs no Vault write, orders entries
// deterministically, and produces byte-identical output across repeated
// calls against the same unchanged canonical evidence.
func TestInspectConversationProjectsFullReviewedWorkflowDeterministically(t *testing.T) {
	root := writeReviewedWorkflowVault(t)
	at := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	ready := writeReadyInteractionSession(t, root, "SESSION-CONVERSATION-001", at.Add(-time.Hour))
	planInput := InteractionWorkflowPlanInput{
		VaultRoot: root, SessionID: ready.SessionID, ExpectedVersion: ready.Version,
		ReviewerID: "QA-001", CurrentTime: at, MaxTasks: 10,
	}
	plan, err := PlanInteractionWorkflow(context.Background(), planInput)
	if err != nil {
		t.Fatal(err)
	}
	providerOutputs := []string{
		"# TASK-001 deliverable\n\n本文",
		reviewProviderOutput(review.VerdictRequestChanges),
		"# TASK-003 revision deliverable\n\n修正版",
		reviewProviderOutput(review.VerdictApprove),
		"# TASK-002 deliverable\n\n本文",
		reviewProviderOutput(review.VerdictApprove),
	}
	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		output := providerOutputs[providerCalls]
		providerCalls++
		encoded, _ := json.Marshal(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": output}},
			"usage": map[string]int{"input_tokens": 1, "output_tokens": 1},
		})
		response.Header().Set("content-type", "application/json")
		_, _ = response.Write(encoded)
	}))
	defer server.Close()
	input := ExecuteInteractionWorkflowInput{
		InteractionWorkflowPlanInput: planInput, WorkflowPlanDigest: plan.WorkflowPlanDigest,
		ApprovalReference: "approval-conversation", CommandID: "CMD-CONVERSATION-WORKFLOW-001",
	}
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}
	result, err := ExecuteInteractionWorkflow(context.Background(), input, provider, server.Client(), true)
	if err != nil || result.Session.State != interaction.StateCompleted {
		t.Fatalf("ExecuteInteractionWorkflow() = %#v, %v", result, err)
	}

	beforeSnapshot := planVaultSnapshot(t, root)
	entries, err := InspectConversation(context.Background(), root, ready.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeSnapshot, planVaultSnapshot(t, root)) {
		t.Fatal("InspectConversation must not write to the Vault (read-only guarantee)")
	}

	if len(entries) == 0 || entries[0].Category != CategoryCEOMessage || entries[0].Kind != KindCEORequest ||
		entries[0].CEOMessageText != "ToDoアプリを完成させる" {
		t.Fatalf("first entry = %#v", entries[0])
	}
	for index := 1; index < len(entries); index++ {
		if entries[index].At.Before(entries[index-1].At) {
			t.Fatalf("entries not ordered by At (deterministic ordering): %#v then %#v", entries[index-1], entries[index])
		}
	}

	var directedRequestChanges, directedRevision, approvedFacts, taskAssigned, deliverableReady, taskCompleted, requestCompleted int
	for _, entry := range entries {
		switch entry.Kind {
		case KindReviewRequestChanges:
			if entry.Category == CategoryDirectedCommunication && entry.MentionAllowed() {
				directedRequestChanges++
				if entry.Speaker.EmployeeID != "QA-001" || entry.ReviewSummary != "要件不足のため修正を依頼します。" {
					t.Fatalf("request changes entry = %#v", entry)
				}
			}
		case KindRevisionCompleted:
			if entry.Category == CategoryDirectedCommunication && entry.MentionAllowed() {
				directedRevision++
				if entry.Recipient.EmployeeID != "QA-001" {
					t.Fatalf("revision entry = %#v", entry)
				}
			}
		case KindReviewApproved:
			if entry.Category == CategoryCompanyFact && entry.Speaker == nil && entry.Recipient == nil {
				approvedFacts++
			}
		case KindTaskAssigned:
			if entry.Speaker == nil && entry.Recipient == nil {
				taskAssigned++
			}
		case KindDeliverableReady:
			if entry.Recipient == nil {
				deliverableReady++
			}
		case KindTaskCompleted:
			taskCompleted++
		case KindRequestCompleted:
			requestCompleted++
		}
	}
	if directedRequestChanges != 1 || directedRevision != 1 || approvedFacts != 2 || taskAssigned != 1 ||
		deliverableReady != 3 || taskCompleted != 3 || requestCompleted != 1 {
		t.Fatalf("unexpected fact counts: requestChanges=%d revision=%d approved=%d assigned=%d deliverable=%d completed=%d requestCompleted=%d",
			directedRequestChanges, directedRevision, approvedFacts, taskAssigned, deliverableReady, taskCompleted, requestCompleted)
	}

	// Repeated calls (not just twice) stress-test ordering: Go deliberately
	// randomizes map iteration order per range, so a lurking dependency on
	// employeesByID's iteration order (rather than the canonical Turn/Task
	// array order this file's loops actually use) would very likely surface
	// as a mismatch somewhere across these calls within the same process.
	entriesJSON, _ := json.Marshal(entries)
	for iteration := 0; iteration < 10; iteration++ {
		replayed, err := InspectConversation(context.Background(), root, ready.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		replayedJSON, _ := json.Marshal(replayed)
		if string(entriesJSON) != string(replayedJSON) {
			t.Fatalf("iteration %d: InspectConversation is not deterministic across repeated calls on unchanged canonical evidence", iteration)
		}
	}
}
