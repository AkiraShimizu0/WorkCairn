package process

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/autonomy"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

const WorkReportVersion = "workcairn-work-report.v1"

type ApprovalProof struct {
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Confirmed bool   `json:"confirmed"`
	Count     int    `json:"count"`
}

type CommandProof struct {
	CommandID string              `json:"command_id"`
	Operation string              `json:"operation,omitempty"`
	State     commandledger.State `json:"state,omitempty"`
	Verified  bool                `json:"verified"`
}

type DeliverableProof struct {
	Committed bool   `json:"committed"`
	Reference string `json:"reference,omitempty"`
	Title     string `json:"title,omitempty"`
}

type ReviewProof struct {
	ReviewerID         string         `json:"reviewer_id"`
	Verdict            review.Verdict `json:"verdict,omitempty"`
	CanonicalCommitted bool           `json:"canonical_committed"`
	CanonicalReference string         `json:"canonical_reference,omitempty"`
	RequestChanges     bool           `json:"request_changes"`
}

type RevisionProof struct {
	Occurred        bool   `json:"occurred"`
	RevisionTaskID  string `json:"revision_task_id,omitempty"`
	IntentCommitted bool   `json:"intent_committed"`
	IntentReference string `json:"intent_reference,omitempty"`
}

type TaskProof struct {
	TaskID      string           `json:"task_id"`
	Title       string           `json:"title"`
	MakerID     string           `json:"maker_id,omitempty"`
	Status      task.Status      `json:"status"`
	Deliverable DeliverableProof `json:"deliverable"`
	Review      ReviewProof      `json:"review"`
	Revision    RevisionProof    `json:"revision"`
	Commands    []CommandProof   `json:"commands"`
	Verified    bool             `json:"verified"`
}

type ExternalActionProof struct {
	Requested        bool   `json:"requested"`
	TargetID         string `json:"target_id,omitempty"`
	IntentCommitted  bool   `json:"intent_committed"`
	OutcomeCommitted bool   `json:"outcome_committed"`
	Published        bool   `json:"published"`
	URL              string `json:"url,omitempty"`
	EventPublished   bool   `json:"event_published"`
	Verified         bool   `json:"verified"`
}

type AuditProof struct {
	Readable       bool   `json:"readable"`
	RecordedEvents int    `json:"recorded_events"`
	Problem        string `json:"problem,omitempty"`
}

type ProofOfWork struct {
	Approvals      []ApprovalProof     `json:"approvals"`
	Tasks          []TaskProof         `json:"tasks"`
	ExternalAction ExternalActionProof `json:"external_action"`
	Audit          AuditProof          `json:"audit"`
	VerifiedTasks  int                 `json:"verified_tasks"`
	FullyVerified  bool                `json:"fully_verified"`
}

type CEOAttention struct {
	CompanySteps              int  `json:"company_steps"`
	DelegatedSteps            int  `json:"delegated_steps"`
	ClarificationQuestions    int  `json:"clarification_questions"`
	ApprovalMoments           int  `json:"approval_moments"`
	RecoveryAttentionRequired int  `json:"recovery_attention_required"`
	NoActionNeeded            bool `json:"no_action_needed"`
}

type WorkReport struct {
	Version   string             `json:"version"`
	SessionID string             `json:"session_id"`
	Status    interaction.State  `json:"status"`
	Autonomy  *autonomy.Contract `json:"autonomy_contract,omitempty"`
	Proof     ProofOfWork        `json:"proof_of_work"`
	Attention CEOAttention       `json:"ceo_attention"`
}

// InspectWorkReport builds a read-only projection from existing Interaction,
// Task, Deliverable, Review, Revision intent, and Command Ledger evidence. It
// never repairs missing evidence or turns a partial state into success.
func InspectWorkReport(ctx context.Context, vaultRoot, sessionID string) (WorkReport, error) {
	record, err := InspectInteraction(ctx, vaultRoot, sessionID)
	if err != nil {
		return WorkReport{}, err
	}
	report := WorkReport{
		Version: WorkReportVersion, SessionID: record.SessionID, Status: record.State,
		Proof: ProofOfWork{Approvals: []ApprovalProof{}, Tasks: []TaskProof{}},
	}
	_, projectName, projectApplied := record.AppliedProject()
	workflowTasks := make([]struct {
		evidence interaction.WorkflowTaskEvidence
		reviewer string
	}, 0)
	planGenerations, planApplications, workflowApprovals, actionApprovals := 0, 0, 0, 0
	clarifications, recoveryAttention := 0, 0
	seenTask := make(map[string]bool)
	for _, turn := range record.Turns {
		switch turn.Kind {
		case interaction.TurnPlanGenerated:
			planGenerations++
		case interaction.TurnClarificationAnswered:
			clarifications += len(turn.Answers)
		case interaction.TurnPlanApplied:
			planApplications++
		case interaction.TurnWorkflowRecorded:
			workflowApprovals++
			if turn.Workflow.Autonomy != nil {
				contract := turn.Workflow.Autonomy.Clone()
				report.Autonomy = &contract
			}
			if turn.Workflow.Failure != nil {
				recoveryAttention++
			}
			for _, item := range turn.Workflow.Tasks {
				if seenTask[item.TaskID] {
					continue
				}
				seenTask[item.TaskID] = true
				workflowTasks = append(workflowTasks, struct {
					evidence interaction.WorkflowTaskEvidence
					reviewer string
				}{item, turn.Workflow.ReviewerID})
			}
		case interaction.TurnActionRecorded:
			actionApprovals++
			if turn.Action.Failure != nil {
				recoveryAttention++
			}
			report.Proof.ExternalAction = externalActionProof(*turn.Action)
		}
	}
	report.Proof.Approvals = approvalProofs(planGenerations, planApplications, workflowApprovals, actionApprovals)
	if projectApplied {
		if err := populateTaskProof(ctx, vaultRoot, projectName, workflowTasks, &report); err != nil {
			return WorkReport{}, err
		}
		reader, readerErr := vault.NewRecoverySnapshotReader(vaultRoot, projectName)
		if readerErr != nil {
			return WorkReport{}, readerErr
		}
		snapshot, snapshotErr := reader.Load(ctx)
		if snapshotErr != nil {
			return WorkReport{}, snapshotErr
		}
		report.Proof.Audit = AuditProof{Readable: snapshot.AuditValid, RecordedEvents: len(snapshot.Audit), Problem: snapshot.AuditProblem}
	}
	report.Proof.FullyVerified = len(report.Proof.Tasks) > 0 && report.Proof.VerifiedTasks == len(report.Proof.Tasks) &&
		report.Proof.Audit.Readable && report.Proof.Audit.RecordedEvents > 0 && recoveryAttention == 0 &&
		(!report.Proof.ExternalAction.Requested || report.Proof.ExternalAction.Verified)
	companySteps, delegatedSteps := planGenerations, 0
	for _, current := range workflowTasks {
		companySteps++
		delegatedSteps++
		if current.evidence.ReviewCommandID != "" {
			companySteps++
			delegatedSteps++
		}
		if current.evidence.RevisionCommandID != "" {
			companySteps++
			delegatedSteps++
		}
	}
	if report.Proof.ExternalAction.Requested {
		companySteps++
	}
	report.Attention = CEOAttention{
		CompanySteps: companySteps, DelegatedSteps: delegatedSteps, ClarificationQuestions: clarifications,
		ApprovalMoments:           1 + planGenerations + planApplications + workflowApprovals + actionApprovals,
		RecoveryAttentionRequired: recoveryAttention,
		NoActionNeeded: report.Proof.FullyVerified &&
			(record.State == interaction.StateCompleted || record.State == interaction.StateActionCompleted),
	}
	if err := report.Validate(); err != nil {
		return WorkReport{}, err
	}
	return report, nil
}

func populateTaskProof(ctx context.Context, vaultRoot, projectName string, items []struct {
	evidence interaction.WorkflowTaskEvidence
	reviewer string
}, report *WorkReport) error {
	revisionStore, err := vault.NewRevisionIntentStore(vaultRoot, projectName)
	if err != nil {
		return err
	}
	references, err := revisionStore.ListReferences(ctx)
	if err != nil {
		return err
	}
	revisions := make(map[string]vault.RevisionIntentReference, len(references))
	for _, reference := range references {
		revisions[reference.SourceTaskID] = reference
	}
	ledger, err := vault.NewCommandLedgerStore(vaultRoot, projectName)
	if err != nil {
		return err
	}
	for _, item := range items {
		inspection, inspectErr := InspectTaskEvidence(ctx, vaultRoot, projectName, item.evidence.TaskID)
		if inspectErr != nil {
			return inspectErr
		}
		proof := TaskProof{TaskID: inspection.Task.ID, Title: inspection.Task.Title, Status: inspection.Task.Status, Review: ReviewProof{ReviewerID: item.reviewer}, Commands: []CommandProof{}}
		if inspection.Task.AssigneeID != nil {
			proof.MakerID = *inspection.Task.AssigneeID
		}
		if inspection.Deliverable != nil {
			proof.Deliverable = DeliverableProof{Committed: true, Reference: inspection.Deliverable.RelativePath, Title: inspection.Deliverable.Title}
		}
		if len(inspection.Reviews) > 0 {
			latest := inspection.Reviews[len(inspection.Reviews)-1]
			proof.Review.Verdict = latest.Decision.Verdict
			proof.Review.CanonicalCommitted = true
			proof.Review.CanonicalReference = latest.CanonicalPath
			for _, reviewed := range inspection.Reviews {
				proof.Review.RequestChanges = proof.Review.RequestChanges || reviewed.Decision.Verdict == review.VerdictRequestChanges
			}
		}
		if reference, ok := revisions[item.evidence.TaskID]; ok {
			proof.Revision = RevisionProof{Occurred: true, RevisionTaskID: reference.RevisionTaskID, IntentCommitted: true, IntentReference: reference.RelativePath}
		} else if item.evidence.RevisionTaskID != "" {
			proof.Revision = RevisionProof{Occurred: true, RevisionTaskID: item.evidence.RevisionTaskID}
		}
		for _, commandID := range []string{item.evidence.ExecutionCommandID, item.evidence.ReviewCommandID, item.evidence.RevisionCommandID} {
			if strings.TrimSpace(commandID) == "" {
				continue
			}
			command, commandErr := inspectCommandProof(ctx, ledger, commandID)
			if commandErr != nil {
				return commandErr
			}
			proof.Commands = append(proof.Commands, command)
		}
		commandsVerified := len(proof.Commands) >= 2
		for _, command := range proof.Commands {
			commandsVerified = commandsVerified && command.Verified
		}
		reviewResolved := proof.Review.Verdict == review.VerdictApprove || proof.Review.RequestChanges && proof.Revision.IntentCommitted
		proof.Verified = inspection.Task.Status == task.StatusCompleted && proof.Deliverable.Committed &&
			proof.Review.CanonicalCommitted && reviewResolved && commandsVerified &&
			(!proof.Revision.Occurred || proof.Revision.IntentCommitted)
		if proof.Verified {
			report.Proof.VerifiedTasks++
		}
		report.Proof.Tasks = append(report.Proof.Tasks, proof)
	}
	return nil
}

func inspectCommandProof(ctx context.Context, store *vault.CommandLedgerStore, commandID string) (CommandProof, error) {
	record, err := store.Get(ctx, commandID)
	if errors.Is(err, commandledger.ErrNotFound) {
		return CommandProof{CommandID: commandID}, nil
	}
	if err != nil {
		return CommandProof{}, err
	}
	return CommandProof{CommandID: record.CommandID, Operation: record.Operation, State: record.State, Verified: record.State == commandledger.StateSucceeded}, nil
}

func approvalProofs(planGenerations, planApplications, workflows, actions int) []ApprovalProof {
	proofs := []ApprovalProof{{Kind: "request", Label: "依頼の開始", Confirmed: true, Count: 1}}
	for _, proof := range []ApprovalProof{
		{Kind: "plan_generation", Label: "Plan作成", Confirmed: planGenerations > 0, Count: planGenerations},
		{Kind: "plan_apply", Label: "Project・Task作成", Confirmed: planApplications > 0, Count: planApplications},
		{Kind: "workflow", Label: "Autonomy Contractの範囲", Confirmed: workflows > 0, Count: workflows},
		{Kind: "external_action", Label: "外部公開", Confirmed: actions > 0, Count: actions},
	} {
		if proof.Count > 0 {
			proofs = append(proofs, proof)
		}
	}
	return proofs
}

func externalActionProof(evidence interaction.ActionEvidence) ExternalActionProof {
	proof := ExternalActionProof{Requested: true, TargetID: evidence.TargetID, EventPublished: evidence.EventPublished}
	proof.IntentCommitted = evidence.Intent != nil && evidence.Intent.Committed
	proof.OutcomeCommitted = evidence.Outcome != nil && evidence.Outcome.Committed
	if evidence.Publication != nil {
		proof.Published = evidence.Publication.Status == "published"
		proof.URL = evidence.Publication.URL
	}
	proof.Verified = evidence.Status == interaction.ActionStatusPublished && proof.IntentCommitted && proof.OutcomeCommitted && proof.Published && proof.EventPublished
	return proof
}

func (report WorkReport) Validate() error {
	if report.Version != WorkReportVersion || interaction.ValidateSessionID(report.SessionID) != nil || !report.Status.Valid() ||
		report.Proof.Approvals == nil || report.Proof.Tasks == nil || report.Proof.VerifiedTasks < 0 || report.Proof.VerifiedTasks > len(report.Proof.Tasks) ||
		report.Proof.Audit.RecordedEvents < 0 || report.Attention.CompanySteps < 0 || report.Attention.DelegatedSteps < 0 ||
		report.Attention.ClarificationQuestions < 0 || report.Attention.ApprovalMoments < 0 || report.Attention.RecoveryAttentionRequired < 0 {
		return fmt.Errorf("invalid Work Report")
	}
	return nil
}
