package process

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/failure"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/interaction"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/organization"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

const ConversationVersion = "workcairn-conversation.v1"

// ConversationCategory is the closed set of ways a ConversationEntry may
// relate to a human actor. See ADR-0047: only Directed Communication may
// ever carry both a Speaker and a Recipient, and only when canonical
// evidence proves both sides — never inferred from a one-sided fact like
// Task assignment.
type ConversationCategory string

const (
	// CategoryCEOMessage is the CEO's own words: the original request, or a
	// confirmed clarification answer. Speaker/Recipient/Subject are always
	// nil — the Category itself is the speaker signal.
	CategoryCEOMessage ConversationCategory = "ceo_message"
	// CategoryDirectedCommunication requires canonical evidence naming both
	// a Speaker and a Recipient (e.g. Reviewer -> Maker Request Changes).
	CategoryDirectedCommunication ConversationCategory = "directed_communication"
	// CategoryCompanyFact is an event whose actor or subject is known but
	// whose direction (who told whom) is not canonically provable — e.g.
	// Task assignment (TaskService assigned it; no human "spoke").
	CategoryCompanyFact ConversationCategory = "company_fact"
	// CategorySystem is a WorkCairn runtime fact with no human actor.
	CategorySystem ConversationCategory = "system"
)

// ConversationKind is a closed, presentation-neutral classification of
// what happened. It never encodes a rendered sentence.
type ConversationKind string

const (
	KindCEORequest             ConversationKind = "ceo_request"
	KindClarificationRequested ConversationKind = "clarification_requested"
	KindCEOClarificationAnswer ConversationKind = "ceo_clarification_answer"
	KindTaskAssigned           ConversationKind = "task_assigned"
	KindDeliverableReady       ConversationKind = "deliverable_ready"
	KindReviewRequestChanges   ConversationKind = "review_request_changes"
	KindReviewApproved         ConversationKind = "review_approved"
	KindRevisionCompleted      ConversationKind = "revision_completed"
	KindTaskCompleted          ConversationKind = "task_completed"
	KindRequestCompleted       ConversationKind = "request_completed"
	KindFailure                ConversationKind = "failure"
)

// Audience is a presentation-relevant hint about who an entry matters to.
// It never changes what Speaker/Recipient/Subject/Category a caller may
// infer — it only helps a future UI decide default visibility.
type Audience string

const (
	AudienceCEO      Audience = "ceo"
	AudienceEmployee Audience = "employee"
	AudienceCompany  Audience = "company"
	AudienceSystem   Audience = "system"
)

// ActorRef names a canonical Employee by permanent ID (Article 10). Name is
// display-only and may be stale relative to the current Organization roster.
type ActorRef struct {
	EmployeeID string `json:"employee_id"`
	Name       string `json:"name,omitempty"`
}

// ConversationEntry is one typed fact, deterministically projected from
// existing canonical evidence (Interaction, Task, Review, Revision,
// Deliverable, Command Ledger). It never carries a finished, rendered
// message: CEOMessageText/ReviewSummary/ReviewIssues[].Description are
// canonical text passthroughs (the CEO's own words, or the Reviewer's own
// already-committed Decision text) — WorkCairn composes none of them, and
// a future UI owns all Japanese presentation templates (e.g. "@名前" or
// "修正をお願いします。").
type ConversationEntry struct {
	At       time.Time            `json:"at"`
	Category ConversationCategory `json:"category"`
	Kind     ConversationKind     `json:"kind"`

	// Speaker/Recipient are set only for CategoryDirectedCommunication,
	// and only when canonical evidence names both sides. Subject is set
	// for CategoryCompanyFact when a single actor is known, but never
	// implies a direction. All three are nil for CategoryCEOMessage (the
	// Category itself already means "the CEO said this") and
	// CategorySystem (no human actor).
	Speaker   *ActorRef `json:"speaker,omitempty"`
	Recipient *ActorRef `json:"recipient,omitempty"`
	Subject   *ActorRef `json:"subject,omitempty"`
	Audience  Audience  `json:"audience"`

	TaskTitle string `json:"task_title,omitempty"`
	// TaskID is the canonical Task identifier this entry's fact belongs to
	// (set only for the per-Task facts InspectTaskEvidence already reads:
	// deliverable_ready, review_request_changes, review_approved,
	// revision_completed, task_completed). A UI combines it with the
	// Session's already-applied Project name to reuse the existing
	// read-only /v1/projects/{project}/tasks/{task}/evidence projection --
	// this package never inlines Deliverable content itself.
	TaskID string `json:"task_id,omitempty"`

	ReviewVerdict review.Verdict `json:"review_verdict,omitempty"`
	ReviewIssues  []review.Issue `json:"review_issues,omitempty"`
	ReviewSummary string         `json:"review_summary,omitempty"`

	DeliverableReference string `json:"deliverable_reference,omitempty"`

	// Question is WorkCairn's own already-persisted clarification question
	// text (one of the CEO Plan Intent's CEOQuestions, committed as part of
	// the canonical Plan) -- set only for CategorySystem/KindClarificationRequested.
	// It is never composed or paraphrased here; it is the Provider's
	// original question text as canonically committed to the Plan.
	Question string `json:"question,omitempty"`

	// FailureDetails is the existing sanitized failure.Envelope, forwarded
	// unchanged from Command Ledger evidence (ADR-0041) — never
	// re-derived, never carrying raw Provider content or secrets.
	FailureDetails *failure.Envelope `json:"failure_details,omitempty"`

	// CEOMessageText is the CEO's own already-persisted text (the original
	// request, or a confirmed clarification answer) — set only for
	// CategoryCEOMessage.
	CEOMessageText string `json:"ceo_message_text,omitempty"`
}

// MentionAllowed is the single Go-owned source of truth for whether a UI
// may render this entry with a directed "@name" mention. It is true only
// when both a Speaker and a Recipient are canonically confirmed — never
// when only one side is known.
func (entry ConversationEntry) MentionAllowed() bool {
	return entry.Category == CategoryDirectedCommunication && entry.Speaker != nil && entry.Recipient != nil
}

// InspectConversation builds a read-only, deterministic Conversation
// Projection for one Interaction Session from existing canonical evidence
// (Interaction Turns, Organization inventory, Task/Deliverable/Review
// evidence, Command Ledger failure detail). It performs no write of any
// kind — no Vault write, no Task write, no Interaction write, no Ledger
// write, no Provider or LLM call, no new Domain Event — and never invents
// a Speaker, Recipient, or Subject beyond what the underlying evidence
// already proves. Same inputs always produce the same output.
func InspectConversation(ctx context.Context, vaultRoot, sessionID string) ([]ConversationEntry, error) {
	if ctx == nil {
		return nil, fmt.Errorf("inspect conversation: context is required")
	}
	record, err := InspectInteraction(ctx, vaultRoot, sessionID)
	if err != nil {
		return nil, err
	}
	organizationInspection, err := InspectOrganization(ctx, vaultRoot)
	if err != nil {
		return nil, err
	}
	employeesByID := employeeDirectory(organizationInspection.Inventory.Employees)

	entries := []ConversationEntry{ceoRequestEntry(record)}

	_, projectName, projectApplied := record.AppliedProject()
	var ledger *vault.CommandLedgerStore
	if projectApplied {
		ledger, err = vault.NewCommandLedgerStore(vaultRoot, projectName)
		if err != nil {
			return nil, err
		}
	}

	for turnIndex, turn := range record.Turns {
		switch turn.Kind {
		case interaction.TurnPlanGenerated:
			entries = append(entries, clarificationRequestedEntries(turn)...)
		case interaction.TurnClarificationAnswered:
			entries = append(entries, clarificationAnswerEntries(record.Turns, turnIndex)...)
		case interaction.TurnPlanApplied:
			if planTurn := latestPlanBefore(record.Turns, turnIndex); planTurn != nil && planTurn.Plan != nil {
				entries = append(entries, taskAssignedEntries(turn, *planTurn.Plan, employeesByID)...)
			}
		}
		if turn.Workflow != nil {
			reviewer := employeesByID[strings.TrimSpace(turn.Workflow.ReviewerID)]
			for _, workflowTask := range turn.Workflow.Tasks {
				taskEvidence, evidenceErr := InspectTaskEvidence(ctx, vaultRoot, projectName, workflowTask.TaskID)
				if evidenceErr != nil {
					return nil, evidenceErr
				}
				entries = append(entries, workflowTaskConversationEntries(turn.At, workflowTask, reviewer, employeesByID, taskEvidence)...)
			}
			if turn.Workflow.Failure != nil {
				envelope, envelopeErr := commandLedgerFailureDetails(ctx, ledger, turn.Workflow.WorkflowCommandID, turn.Workflow.Failure)
				if envelopeErr != nil {
					return nil, envelopeErr
				}
				if entry := failureEntry(turn.At, envelope); entry != nil {
					entries = append(entries, *entry)
				}
			}
		}
		if turn.Action != nil && turn.Action.Failure != nil {
			envelope, envelopeErr := commandLedgerFailureDetails(ctx, ledger, turn.Action.ActionCommandID, turn.Action.Failure)
			if envelopeErr != nil {
				return nil, envelopeErr
			}
			if entry := failureEntry(turn.At, envelope); entry != nil {
				entries = append(entries, *entry)
			}
		}
	}

	if entry := requestCompletedEntry(record); entry != nil {
		entries = append(entries, *entry)
	}

	sort.SliceStable(entries, func(left, right int) bool { return entries[left].At.Before(entries[right].At) })
	return entries, nil
}

func ceoRequestEntry(record interaction.Record) ConversationEntry {
	return ConversationEntry{
		At: record.CreatedAt, Category: CategoryCEOMessage, Kind: KindCEORequest,
		Audience: AudienceCompany, CEOMessageText: strings.TrimSpace(record.Request),
	}
}

// clarificationRequestedEntries projects only the first of the CEO Plan
// Intent's own CEOQuestions -- already canonically committed as part of
// this TurnPlanGenerated Turn -- as a system entry. Every later question
// is revealed by clarificationAnswerEntries, one at a time, exactly when
// the question before it durably has an answer (interaction.RecordAnswers
// commits answers incrementally, in CEOQuestions order): this is what
// produces Q1,A1,Q2,A2,... instead of every question up front followed by
// every answer. This package composes none of the question text: it is
// the Provider's own already-persisted string, never paraphrased or
// re-derived.
func clarificationRequestedEntries(turn interaction.Turn) []ConversationEntry {
	if turn.Plan == nil || len(turn.Plan.CEOQuestions) == 0 {
		return nil
	}
	first := strings.TrimSpace(turn.Plan.CEOQuestions[0])
	if first == "" {
		return nil
	}
	return []ConversationEntry{{
		At: turn.At, Category: CategorySystem, Kind: KindClarificationRequested,
		Audience: AudienceCEO, Question: first,
	}}
}

// clarificationAnswerEntries projects one TurnClarificationAnswered
// Turn's answers, then -- if this batch did not durably complete every
// CEOQuestion of the active Plan -- appends the single next unanswered
// question at the same instant, so it sorts immediately after the answer
// that revealed it. The active Plan and how many of its CEOQuestions
// already had an answer strictly before this Turn are both derived from
// canonical Turn history (record.Turns), never from a client-supplied or
// interpolated question/answer pairing.
func clarificationAnswerEntries(turns []interaction.Turn, turnIndex int) []ConversationEntry {
	turn := turns[turnIndex]
	entries := make([]ConversationEntry, 0, len(turn.Answers)+1)
	for _, answer := range turn.Answers {
		text := strings.TrimSpace(answer.Answer)
		if text == "" {
			continue
		}
		entries = append(entries, ConversationEntry{
			At: turn.At, Category: CategoryCEOMessage, Kind: KindCEOClarificationAnswer,
			Audience: AudienceCompany, CEOMessageText: text,
		})
	}
	planIndex := -1
	for index := turnIndex - 1; index >= 0; index-- {
		if turns[index].Kind == interaction.TurnPlanGenerated && turns[index].Plan != nil {
			planIndex = index
			break
		}
	}
	if planIndex == -1 {
		return entries
	}
	answeredThrough := 0
	for index := planIndex + 1; index < turnIndex; index++ {
		if turns[index].Kind == interaction.TurnClarificationAnswered {
			answeredThrough += len(turns[index].Answers)
		}
	}
	answeredThrough += len(turn.Answers)
	questions := turns[planIndex].Plan.CEOQuestions
	if answeredThrough < len(questions) {
		next := strings.TrimSpace(questions[answeredThrough])
		if next != "" {
			entries = append(entries, ConversationEntry{
				At: turn.At, Category: CategorySystem, Kind: KindClarificationRequested,
				Audience: AudienceCEO, Question: next,
			})
		}
	}
	return entries
}

// taskAssignedEntries projects Task assignment as a Company Fact only.
// TaskService — not a human — performs assignment (Article 6), so this
// never carries a Speaker: "the company assigned this task to X" is a
// one-sided fact, never "someone told X to do this."
func taskAssignedEntries(turn interaction.Turn, plan ceoplan.Plan, employeesByID map[string]organization.Identity) []ConversationEntry {
	entries := make([]ConversationEntry, 0, len(plan.ProposedTasks))
	for _, proposed := range plan.ProposedTasks {
		if proposed.AssigneeID == nil {
			continue
		}
		entries = append(entries, ConversationEntry{
			At: turn.At, Category: CategoryCompanyFact, Kind: KindTaskAssigned,
			Subject: actorRefByID(employeesByID, *proposed.AssigneeID), Audience: AudienceCompany,
			TaskTitle: strings.TrimSpace(proposed.Title),
		})
	}
	return entries
}

// workflowTaskConversationEntries projects the facts belonging to one
// Reviewed Workflow task run (original execution or a targeted revision):
// Deliverable ready, Revision completed, the Review verdict, and Task
// completion. Reviewer is threaded in from WorkflowEvidence.ReviewerID,
// the single canonical Reviewer already resolved for the whole Reviewed
// Workflow run (ADR-0040) — including any revision's re-review, so
// attributing a revision's re-review to this same Reviewer is not a guess.
func workflowTaskConversationEntries(at time.Time, workflowTask interaction.WorkflowTaskEvidence, reviewer organization.Identity, employeesByID map[string]organization.Identity, evidence TaskEvidenceInspection) []ConversationEntry {
	entries := make([]ConversationEntry, 0, 4)
	var makerRef *ActorRef
	if evidence.Task.AssigneeID != nil {
		makerRef = actorRefByID(employeesByID, *evidence.Task.AssigneeID)
	}
	reviewerRef := actorRef(reviewer)
	title := strings.TrimSpace(evidence.Task.Title)
	if title == "" {
		title = workflowTask.TaskID
	}

	if evidence.Deliverable != nil {
		entries = append(entries, ConversationEntry{
			At: at, Category: CategoryCompanyFact, Kind: KindDeliverableReady,
			Subject: makerRef, Audience: AudienceCompany, TaskTitle: title, TaskID: workflowTask.TaskID,
			DeliverableReference: evidence.Deliverable.RelativePath,
		})
	}

	if workflowTask.TargetedRevision {
		entry := ConversationEntry{At: at, Kind: KindRevisionCompleted, Audience: AudienceEmployee, TaskTitle: title, TaskID: workflowTask.TaskID}
		if makerRef != nil && reviewerRef != nil {
			entry.Category = CategoryDirectedCommunication
			entry.Speaker = makerRef
			entry.Recipient = reviewerRef
		} else {
			entry.Category = CategoryCompanyFact
			entry.Subject = makerRef
		}
		entries = append(entries, entry)
	}

	if workflowTask.Verdict != "" {
		reviewEntry := ConversationEntry{At: at, TaskTitle: title, TaskID: workflowTask.TaskID, ReviewVerdict: workflowTask.Verdict, Audience: AudienceEmployee}
		if decision := latestReviewDecision(evidence.Reviews); decision != nil {
			reviewEntry.ReviewIssues = decision.Issues
			reviewEntry.ReviewSummary = decision.Summary
		}
		if workflowTask.Verdict == review.VerdictRequestChanges {
			reviewEntry.Kind = KindReviewRequestChanges
			if reviewerRef != nil && makerRef != nil {
				reviewEntry.Category = CategoryDirectedCommunication
				reviewEntry.Speaker = reviewerRef
				reviewEntry.Recipient = makerRef
			} else {
				reviewEntry.Category = CategoryCompanyFact
				reviewEntry.Subject = reviewerRef
			}
		} else {
			// Approve is a completion fact, not actionable feedback aimed at
			// a specific recipient — Company Fact, never Directed
			// Communication, even when both parties are known.
			reviewEntry.Kind = KindReviewApproved
			reviewEntry.Category = CategoryCompanyFact
			reviewEntry.Subject = reviewerRef
		}
		entries = append(entries, reviewEntry)
	}

	if evidence.Task.Status == task.StatusCompleted {
		entries = append(entries, ConversationEntry{
			At: at, Category: CategoryCompanyFact, Kind: KindTaskCompleted,
			Subject: makerRef, Audience: AudienceCompany, TaskTitle: title, TaskID: workflowTask.TaskID,
		})
	}
	return entries
}

func requestCompletedEntry(record interaction.Record) *ConversationEntry {
	if record.State != interaction.StateCompleted && record.State != interaction.StateActionCompleted {
		return nil
	}
	if len(record.Turns) == 0 {
		return nil
	}
	at := record.Turns[len(record.Turns)-1].At
	return &ConversationEntry{At: at, Category: CategoryCompanyFact, Kind: KindRequestCompleted, Audience: AudienceCEO}
}

// commandLedgerFailureDetails prefers the full sanitized failure.Envelope
// already committed to the Command Ledger (ADR-0041) for the given
// Command ID, forwarded unchanged. When that Envelope is unavailable (a
// pre-ADR-0041 record, or the Ledger entry itself is missing), it falls
// back to re-expressing the bounded Interaction-level summary
// (interaction.WorkflowFailure{Code,Stage,Partial}) as an Envelope using
// only fields that summary already carries — never inventing new
// diagnostic content.
func commandLedgerFailureDetails(ctx context.Context, store *vault.CommandLedgerStore, commandID string, bounded *interaction.WorkflowFailure) (*failure.Envelope, error) {
	commandID = strings.TrimSpace(commandID)
	if store != nil && commandID != "" {
		record, err := store.Get(ctx, commandID)
		switch {
		case err == nil:
			if record.Failure != nil && record.Failure.Details != nil {
				return record.Failure.Details, nil
			}
		case errors.Is(err, commandledger.ErrNotFound):
			// fall through to the bounded summary below
		default:
			return nil, err
		}
	}
	if bounded == nil || strings.TrimSpace(bounded.Code) == "" || strings.TrimSpace(bounded.Stage) == "" {
		return nil, nil
	}
	envelope := failure.New(bounded.Code, bounded.Stage)
	envelope.Partial = bounded.Partial
	return &envelope, nil
}

func failureEntry(at time.Time, envelope *failure.Envelope) *ConversationEntry {
	if envelope == nil {
		return nil
	}
	return &ConversationEntry{At: at, Category: CategorySystem, Kind: KindFailure, Audience: AudienceCEO, FailureDetails: envelope}
}

func latestReviewDecision(reviews []ReviewInspection) *review.Decision {
	if len(reviews) == 0 {
		return nil
	}
	decision := reviews[len(reviews)-1].Decision
	return &decision
}

func actorRef(employee organization.Identity) *ActorRef {
	if strings.TrimSpace(employee.ID) == "" {
		return nil
	}
	return &ActorRef{EmployeeID: employee.ID, Name: employeeDisplayName(employee)}
}

func actorRefByID(employeesByID map[string]organization.Identity, employeeID string) *ActorRef {
	employeeID = strings.TrimSpace(employeeID)
	if employeeID == "" {
		return nil
	}
	if employee, ok := employeesByID[employeeID]; ok {
		return actorRef(employee)
	}
	return &ActorRef{EmployeeID: employeeID}
}
