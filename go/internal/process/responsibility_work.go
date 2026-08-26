package process

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/goal"
	"github.com/AkiraShimizu0/workcairn/go/internal/responsibility"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

var (
	ErrResponsibilityPlanApprovalRequired = errors.New("explicit Responsibility Planning approval is required")
	// ErrResponsibilityInactiveForPlanning means the Responsibility exists
	// but is currently Inactive. This does not auto-activate it -- the
	// caller must explicitly Activate first (a separate, already-existing
	// Command) if new work generation is actually wanted.
	ErrResponsibilityInactiveForPlanning = errors.New("responsibility is inactive; activate it before generating work")
)

// ResponsibilityPlanInput is Human-instruction-driven (Model B from this
// Checkpoint's own design comparison): Responsibility supplies standing
// context (Title, linked Goals), Instruction supplies what the CEO/Operator
// actually wants planned right now. Neither alone is sufficient -- a
// Responsibility's Title is never expanded into a work request on its own,
// avoiding exactly the kind of unstated-assumption fabrication this
// session's Planning/Synthesis Quality work exists to catch.
type ResponsibilityPlanInput struct {
	VaultRoot        string
	ResponsibilityID string
	Scope            responsibility.Scope
	ProjectName      string
	Instruction      string
	Model            string
}

// ResponsibilityPlanningResult wraps the unmodified, existing
// service.CEOPlanResult with Responsibility/Goal/Binding traceability --
// deliberately not a change to ceoplan.Plan's own schema (see
// docs/adr/ADR-0062-responsibility-work-generation.md's traceability
// discussion). BoundEmployeeID is the Responsibility's current owner for
// display only -- it never overrides ceoplan's own RequiredRole-based Task
// assignment resolution.
type ResponsibilityPlanningResult struct {
	ResponsibilityID    string                `json:"responsibility_id"`
	ResponsibilityTitle string                `json:"responsibility_title"`
	Scope               responsibility.Scope  `json:"scope"`
	ProjectName         string                `json:"project_name,omitempty"`
	GoalRefs            []string              `json:"goal_refs,omitempty"`
	BoundEmployeeID     string                `json:"bound_employee_id,omitempty"`
	Generation          service.CEOPlanResult `json:"generation"`
}

// GenerateResponsibilityPlan resolves a Responsibility's standing context
// (and its linked Goals, and its current Binding, if any) and hands off to
// the existing, unmodified production Planning path (GenerateCEOPlan ->
// CEOPlanService.Generate -> ceoplan.BuildPrompt/ParseIntent/NormalizeIntent).
// It never creates a Task, executes a Workflow, or creates a Schedule --
// this is Plan generation only, exactly like GenerateCEOPlan itself. It
// never wraps Command Ledger claim-before-effect, matching GenerateCEOPlan's
// own precedent exactly: Plan generation is a real-time, non-replayable
// Provider call, not a durable idempotent write.
func GenerateResponsibilityPlan(ctx context.Context, input ResponsibilityPlanInput, approved bool, provider ClaudeProcessConfig, httpClient claude.HTTPDoer) (ResponsibilityPlanningResult, error) {
	if !approved {
		return ResponsibilityPlanningResult{}, ErrResponsibilityPlanApprovalRequired
	}
	if strings.TrimSpace(input.Instruction) == "" {
		return ResponsibilityPlanningResult{}, fmt.Errorf("%w: explicit Human instruction is required", responsibility.ErrInvalidResponsibility)
	}
	store, err := responsibilityStoreFor(input.VaultRoot, input.Scope, input.ProjectName)
	if err != nil {
		return ResponsibilityPlanningResult{}, err
	}
	record, err := store.Get(ctx, input.ResponsibilityID)
	if err != nil {
		return ResponsibilityPlanningResult{}, err
	}
	if record.Status != responsibility.StatusActive {
		return ResponsibilityPlanningResult{}, ErrResponsibilityInactiveForPlanning
	}
	goals, err := resolveResponsibilityGoals(ctx, input.VaultRoot, input.Scope, input.ProjectName, record.GoalRefs)
	if err != nil {
		return ResponsibilityPlanningResult{}, err
	}
	// Binding is best-effort context only (display/traceability) -- an
	// unassigned Responsibility may still be planned. See this
	// Checkpoint's own "Binding requirement" investigation: nothing in
	// ceoplan's assignment resolution reads an "owner" at all (RequiredRole
	// resolution is independent of who holds the Responsibility), and
	// gatekeeping Planning on Binding presence would be an invented
	// restriction with no existing Company OS principle behind it.
	boundEmployeeID := ""
	if binding, bindingErr := store.GetBinding(ctx, input.ResponsibilityID); bindingErr == nil {
		boundEmployeeID = binding.EmployeeID
	} else if !errors.Is(bindingErr, responsibility.ErrNotFound) {
		return ResponsibilityPlanningResult{}, bindingErr
	}

	request := composeResponsibilityPlanningRequest(record, goals, input.Instruction)
	generation, genErr := GenerateCEOPlan(ctx, CEOPlanGenerationInput{
		VaultRoot: input.VaultRoot, Request: request, Model: input.Model, Approved: true,
	}, provider, httpClient)
	result := ResponsibilityPlanningResult{
		ResponsibilityID: record.ResponsibilityID, ResponsibilityTitle: record.Title, Scope: record.Scope,
		ProjectName: record.ProjectName, GoalRefs: record.GoalRefs, BoundEmployeeID: boundEmployeeID,
		Generation: generation,
	}
	// genErr, if any, is returned unwrapped -- it is already a
	// *service.CEOPlanError (with its existing Stage taxonomy and, for
	// CEOPlanIntentStage, its existing Parse *failure.ParseDiagnostic) or
	// ErrCEOPlanGenerationApproval. Reclassifying it here would duplicate
	// Phase T-2/T-6's already-shipped work.
	return result, genErr
}

func resolveResponsibilityGoals(ctx context.Context, vaultRoot string, scope responsibility.Scope, projectName string, goalRefs []string) ([]goal.Record, error) {
	if len(goalRefs) == 0 {
		return nil, nil
	}
	goalStore, err := goalStoreFor(vaultRoot, goalScopeFrom(scope), projectName)
	if err != nil {
		return nil, err
	}
	goals := make([]goal.Record, 0, len(goalRefs))
	for _, goalID := range goalRefs {
		record, err := goalStore.Get(ctx, goalID)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", service.ErrGoalRefNotFound, goalID)
		}
		goals = append(goals, record)
	}
	return goals, nil
}

// composeResponsibilityPlanningRequest builds the same kind of plain,
// deterministic request text interaction.Record.PlanningRequest() already
// builds for the Interaction flow (Request + "\n\n確認済みCEO回答(JSON):\n" +
// answers) -- Go string concatenation only, never an LLM call. It produces
// the "Request" argument ceoplan.BuildPrompt has always accepted as a plain
// string; System/User Prompt construction itself stays entirely
// ceoplan.BuildPrompt's responsibility, untouched. Achieved/Abandoned Goals
// are included exactly like Active ones -- no automatic filtering or
// cascade, matching ADR-0060/ADR-0061's explicit "no automatic cascade"
// decisions.
func composeResponsibilityPlanningRequest(record responsibility.Record, goals []goal.Record, instruction string) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(instruction))
	builder.WriteString("\n\n継続担当Responsibility: ")
	builder.WriteString(record.Title)
	for _, linkedGoal := range goals {
		builder.WriteString("\n関連Goal: ")
		builder.WriteString(linkedGoal.Title)
		builder.WriteString(" — ")
		builder.WriteString(linkedGoal.Outcome)
		builder.WriteString(" (status: ")
		builder.WriteString(string(linkedGoal.Status))
		builder.WriteString(")")
	}
	return builder.String()
}
