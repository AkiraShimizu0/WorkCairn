// Package attention defines the Company Attention / Decision Feed's typed
// read model: a closed set of item Types, entity references, and a small
// closed action vocabulary. It owns no business state -- every Item is a
// projection computed on demand from other Domains' own canonical records
// (Routine + Schedule, Interaction), never a new source of truth. It never
// imports another Domain package -- the read-only aggregation that
// produces Items lives in process.InspectAttention, not here (see
// docs/adr/ADR-0065-company-attention-feed.md).
package attention

import (
	"sort"
	"time"
)

// Type is the closed set of v1 attention reasons. Each Type corresponds to
// exactly one already-existing, safely-judged signal -- never a new
// inference invented for this Feed.
type Type string

const (
	// TypeApprovalRequired means an Interaction Session is waiting on an
	// explicit Human approval to continue its own existing pipeline (Plan
	// generation, Plan apply, or Workflow execution).
	TypeApprovalRequired Type = "approval_required"
	// TypeHumanInputRequired means an Interaction Session is waiting on a
	// substantive answer (a CEO clarification question), not merely an
	// approval.
	TypeHumanInputRequired Type = "human_input_required"
	// TypeInteractionAttentionRequired means an Interaction Session's
	// Reviewed Workflow or External Action reached a durable "attention
	// required" state (interaction.StateWorkflowAttentionRequired /
	// StateActionAttentionRequired) -- a failure a Human must look at.
	TypeInteractionAttentionRequired Type = "interaction_attention_required"
	// TypeRoutineRecoveryRequired means an Active Routine currently has no
	// durable future occurrence (routine.InspectRoutineScheduleHealth
	// reports unhealthy, ADR-0064).
	TypeRoutineRecoveryRequired Type = "routine_recovery_required"
)

// typeOrder is the fixed, deterministic ordering key used by Sort -- not
// an urgency score. It exists only to make repeated scans produce a stable
// item order; it carries no meaning beyond "this Type sorts before that
// one when ObservedAt ties."
var typeOrder = map[Type]int{
	TypeApprovalRequired:             0,
	TypeHumanInputRequired:           1,
	TypeInteractionAttentionRequired: 2,
	TypeRoutineRecoveryRequired:      3,
}

// EntityType names which Domain's own ID EntityID refers to.
type EntityType string

const (
	EntityInteraction EntityType = "interaction"
	EntityRoutine     EntityType = "routine"
)

// ActionKind is the closed set of what a Human can do about an Item. It is
// never free text -- a fixed, small vocabulary a client can safely switch
// on without parsing anything.
type ActionKind string

const (
	ActionApprove   ActionKind = "approve"
	ActionAnswer    ActionKind = "answer"
	ActionInspect   ActionKind = "inspect"
	ActionResume    ActionKind = "resume"
	ActionReconcile ActionKind = "reconcile"
)

// Action names what a Human can do next. Operation, when present, is a
// safe typed reference to an existing Command Ledger operation string
// (e.g. "interaction.answer") or CLI operation name (e.g.
// "routine-reconcile") copied verbatim from the source record's own
// already-validated field -- never generated as free text.
type Action struct {
	Kind      ActionKind `json:"kind"`
	Operation string     `json:"operation,omitempty"`
}

// Item is one row of the Attention Feed -- deliberately not a large union
// struct. EntityID always names the record identified by EntityType;
// ResponsibilityID is the one additive secondary reference this v1 actually
// needs (Routine items trace back to their owning Responsibility). Every
// field here is either copied verbatim from an existing Domain record or
// computed by an existing, already-shipped projection (InspectRoutineScheduleHealth,
// interaction.Record.Next/State) -- Item itself decides nothing.
type Item struct {
	Type             Type       `json:"type"`
	EntityType       EntityType `json:"entity_type"`
	EntityID         string     `json:"entity_id"`
	ProjectName      string     `json:"project_name,omitempty"`
	ResponsibilityID string     `json:"responsibility_id,omitempty"`
	Summary          string     `json:"summary"`
	Action           Action     `json:"action"`
	// ObservedAt is when this fact was last known true -- the source
	// record's own last-transition time when one exists (Interaction's
	// latest Turn), or the scan's own "now" when no such durable timestamp
	// exists (Routine Schedule health is a computed boolean, not a tracked
	// transition). It is never fabricated history.
	ObservedAt time.Time `json:"observed_at"`
}

func (item Item) dedupeKey() string {
	return string(item.Type) + "|" + item.EntityID
}

// Dedupe keeps the first Item for each (Type, EntityID) pair, in input
// order. v1's own sources can each produce at most one Item per entity, so
// this is defense-in-depth for future sources, not something today's
// aggregation actually exercises.
func Dedupe(items []Item) []Item {
	seen := make(map[string]struct{}, len(items))
	deduped := make([]Item, 0, len(items))
	for _, item := range items {
		key := item.dedupeKey()
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, item)
	}
	return deduped
}

// Sort orders Items deterministically: Type's fixed class order, then
// ObservedAt (oldest first), then EntityID as the final tie-break. This is
// not a priority/urgency ranking -- it exists only so repeated scans of
// unchanged state produce byte-identical output.
func Sort(items []Item) {
	sort.SliceStable(items, func(left, right int) bool {
		a, b := items[left], items[right]
		if typeOrder[a.Type] != typeOrder[b.Type] {
			return typeOrder[a.Type] < typeOrder[b.Type]
		}
		if !a.ObservedAt.Equal(b.ObservedAt) {
			return a.ObservedAt.Before(b.ObservedAt)
		}
		return a.EntityID < b.EntityID
	})
}
