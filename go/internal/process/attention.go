package process

import (
	"context"
	"fmt"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/attention"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	"github.com/AkiraShimizu0/workcairn/go/internal/routine"
)

const AttentionVersion = "workcairn-attention.v1"

// InspectAttention is the Company Attention / Decision Feed's one read-only
// aggregation entrypoint. It is not a new source of truth: every call
// recomputes from the same canonical sources that already own this state
// (Routine + Schedule Store via InspectRoutineScheduleHealth, ADR-0064;
// Interaction Sessions via their own existing State/Next() projection) --
// nothing is persisted, claimed, published, or mutated (see
// docs/adr/ADR-0065-company-attention-feed.md). `now` is the caller's
// current time, used only as the ObservedAt for sources with no durable
// transition timestamp of their own (Routine Schedule health).
func InspectAttention(ctx context.Context, vaultRoot string, now time.Time) ([]attention.Item, error) {
	if ctx == nil {
		return nil, fmt.Errorf("inspect attention: context is required")
	}
	items := make([]attention.Item, 0)

	routineItems, err := inspectRoutineAttention(ctx, vaultRoot, now)
	if err != nil {
		return nil, err
	}
	items = append(items, routineItems...)

	interactionItems, err := inspectInteractionAttention(ctx, vaultRoot)
	if err != nil {
		return nil, err
	}
	items = append(items, interactionItems...)

	items = attention.Dedupe(items)
	attention.Sort(items)
	return items, nil
}

// inspectRoutineAttention scans only company-scope Routines: enumerating
// project-scope Routines workspace-wide would require a "list every
// Project" primitive that does not exist anywhere in this codebase today
// (see ADR-0065's own deferred-sources discussion) -- building one solely
// for this Feed would exceed this Checkpoint's read-aggregation scope.
func inspectRoutineAttention(ctx context.Context, vaultRoot string, now time.Time) ([]attention.Item, error) {
	records, err := InspectRoutines(ctx, vaultRoot, routine.ScopeCompany, "")
	if err != nil {
		return nil, err
	}
	items := make([]attention.Item, 0, len(records))
	for _, record := range records {
		if record.Status != routine.StatusActive {
			continue
		}
		healthy, err := InspectRoutineScheduleHealth(ctx, vaultRoot, record)
		if err != nil {
			return nil, err
		}
		if healthy {
			continue
		}
		items = append(items, attention.Item{
			Type: attention.TypeRoutineRecoveryRequired, EntityType: attention.EntityRoutine, EntityID: record.RoutineID,
			ProjectName: record.ProjectName, ResponsibilityID: record.ResponsibilityID,
			Summary:    "Routine " + record.RoutineID + " はActiveですが、次回実行予定のScheduleが見つかりません。",
			Action:     attention.Action{Kind: attention.ActionReconcile, Operation: "routine-reconcile"},
			ObservedAt: now,
		})
	}
	return items, nil
}

func inspectInteractionAttention(ctx context.Context, vaultRoot string) ([]attention.Item, error) {
	records, err := InspectInteractions(ctx, vaultRoot)
	if err != nil {
		return nil, err
	}
	items := make([]attention.Item, 0, len(records))
	for _, record := range records {
		// Archiving is the existing, explicit mechanism a Human already
		// uses to say "stop showing me this" -- the Feed respects that
		// decision rather than re-surfacing it.
		if record.IsArchived() {
			continue
		}
		item, ok, err := interactionAttentionItem(record)
		if err != nil {
			return nil, err
		}
		if ok {
			items = append(items, item)
		}
	}
	return items, nil
}

// interactionAttentionItem classifies directly off record.State -- the
// same durable state machine interaction.Record.Next() already owns -- and
// never re-derives or guesses a classification of its own. StateCompleted
// and StateActionCompleted are deliberately excluded: their only next step
// (interaction.NextOptionalAction, external publish) is optional, not a
// required decision.
func interactionAttentionItem(record interaction.Record) (attention.Item, bool, error) {
	observedAt := interactionObservedAt(record)
	_, projectName, _ := record.AppliedProject()
	switch record.State {
	case interaction.StatePlanGenerationApprovalRequired:
		return attention.Item{
			Type: attention.TypeApprovalRequired, EntityType: attention.EntityInteraction, EntityID: record.SessionID,
			Summary:    "Interaction " + record.SessionID + " のPlan生成に承認が必要です。",
			Action:     attention.Action{Kind: attention.ActionApprove, Operation: "interaction.plan.generate"},
			ObservedAt: observedAt,
		}, true, nil
	case interaction.StateClarificationRequired:
		return attention.Item{
			Type: attention.TypeHumanInputRequired, EntityType: attention.EntityInteraction, EntityID: record.SessionID,
			Summary:    "Interaction " + record.SessionID + " はCEOへの確認質問への回答待ちです。",
			Action:     attention.Action{Kind: attention.ActionAnswer, Operation: "interaction.answer"},
			ObservedAt: observedAt,
		}, true, nil
	case interaction.StatePlanApprovalRequired:
		plan, _, ok := record.CurrentPlan()
		if !ok {
			return attention.Item{}, false, interaction.ErrInvalidSession
		}
		return attention.Item{
			Type: attention.TypeApprovalRequired, EntityType: attention.EntityInteraction, EntityID: record.SessionID,
			ProjectName: plan.ProjectName,
			Summary:     "Interaction " + record.SessionID + " の生成済みPlanに適用の承認が必要です。",
			Action:      attention.Action{Kind: attention.ActionApprove, Operation: "interaction.plan.approve_and_execute"},
			ObservedAt:  observedAt,
		}, true, nil
	case interaction.StateReadyToExecute:
		if _, preAuthorized := record.PendingWorkflowPreAuthorization(); preAuthorized {
			// Already durably pre-authorized and mid-execution (ADR-0049) --
			// transient, never actionable; matches Next()'s own reasoning.
			return attention.Item{}, false, nil
		}
		return attention.Item{
			Type: attention.TypeApprovalRequired, EntityType: attention.EntityInteraction, EntityID: record.SessionID,
			ProjectName: projectName,
			Summary:     "Interaction " + record.SessionID + " はWorkflow実行の承認待ちです。",
			Action:      attention.Action{Kind: attention.ActionApprove, Operation: "interaction.workflow.execute"},
			ObservedAt:  observedAt,
		}, true, nil
	case interaction.StateWorkflowAttentionRequired:
		next, err := record.Next()
		if err != nil {
			return attention.Item{}, false, err
		}
		kind, summary := attention.ActionInspect, "Interaction "+record.SessionID+" のWorkflowが対応を必要とする状態です。"
		if next.Operation != "" {
			kind, summary = attention.ActionResume, "Interaction "+record.SessionID+" のWorkflowが停止しました。Revision Recoveryで再開できます。"
		}
		return attention.Item{
			Type: attention.TypeInteractionAttentionRequired, EntityType: attention.EntityInteraction, EntityID: record.SessionID,
			ProjectName: projectName,
			Summary:     summary,
			Action:      attention.Action{Kind: kind, Operation: next.Operation},
			ObservedAt:  observedAt,
		}, true, nil
	case interaction.StateActionAttentionRequired:
		return attention.Item{
			Type: attention.TypeInteractionAttentionRequired, EntityType: attention.EntityInteraction, EntityID: record.SessionID,
			ProjectName: projectName,
			Summary:     "Interaction " + record.SessionID + " の外部公開Actionが対応を必要とする状態です。",
			Action:      attention.Action{Kind: attention.ActionInspect},
			ObservedAt:  observedAt,
		}, true, nil
	default:
		// StateCompleted, StateActionCompleted: nothing required.
		return attention.Item{}, false, nil
	}
}

// interactionObservedAt is the Session's own last-transition time -- the
// latest Turn's timestamp, or CreatedAt for a brand-new Session with no
// Turns yet. Never fabricated.
func interactionObservedAt(record interaction.Record) time.Time {
	if len(record.Turns) == 0 {
		return record.CreatedAt
	}
	return record.Turns[len(record.Turns)-1].At
}
