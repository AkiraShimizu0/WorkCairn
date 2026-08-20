package ceoplan

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
)

var ErrAssignmentUnresolved = errors.New("CEO plan intent assignment could not be resolved")

// IntentContext carries the minimal, already-persisted deterministic
// Interaction Session identity NormalizeIntent needs to derive safe
// fallbacks for optional Intent fields (ADR-0046). It never carries the
// full interaction.Session/Record, Provider content, or credentials —
// only the three stable, already-durable values a fallback may read from.
type IntentContext struct {
	// Request is the CEO's own already-persisted natural-language request
	// (interaction.Record.PlanningRequest(), i.e. the original request plus
	// any confirmed clarification answers). Used verbatim, trimmed, as the
	// Objective fallback — never a guess, never an LLM re-summary.
	Request string
	// SessionID and RequestDigest are the Interaction Session's own stable,
	// already-persisted identifiers. Used only to derive a deterministic,
	// replay-safe ProjectName fallback. Never combined with wall-clock
	// time, randomness, or a Provider call.
	SessionID     string
	RequestDigest string
}

// NormalizationFailureReason is a sanitized, non-identifying classification
// of why Go could not deterministically turn an Intent into a Canonical
// Plan. It never carries raw Provider text or employee-identifying detail
// beyond what NormalizationError.err already restricts to role/count.
type NormalizationFailureReason string

const (
	// NormalizationAssignmentNoMatch means zero employees currently hold
	// the step's required_role. This is never guessed around — WorkCairn
	// safely rejects rather than leaving a task unassigned.
	NormalizationAssignmentNoMatch NormalizationFailureReason = "assignment_no_match"
	// NormalizationAssignmentAmbiguous means more than one employee holds
	// the step's required_role, so Go cannot pick one deterministically.
	NormalizationAssignmentAmbiguous NormalizationFailureReason = "assignment_ambiguous"
	// NormalizationAssignmentRequirementMissing is defensive: ParseIntent
	// already rejects an empty required_role on a non-review step, so this
	// should be unreachable in practice.
	NormalizationAssignmentRequirementMissing NormalizationFailureReason = "assignment_requirement_missing"
	// NormalizationExcessiveTaskCount means the Intent's non-review steps
	// would generate more Tasks than MaxGeneratedTasks allows (ADR-0051
	// LoopGuard). Rejected before any Task identity or dependency is ever
	// constructed — one CEO request can propose a plan, but cannot make the
	// Provider single-handedly decide how large a decomposition Go commits
	// to executing.
	NormalizationExcessiveTaskCount NormalizationFailureReason = "excessive_task_count"
)

// MaxGeneratedTasks bounds how many Tasks a single CEO Plan decomposition
// may propose (ADR-0051 LoopGuard). It is enforced here, in ceoplan, rather
// than as a field on autonomy.Contract: Contract's lifecycle is Workflow
// *execution* scope (ADR-0035) and is not constructed until after a Plan
// already exists and Tasks are being readied for a Reviewed Workflow run,
// whereas this limit must reject during Plan *generation* itself, before
// any Task, Employee assignment, or dependency graph exists. Forcing a
// premature or empty Workflow-scoped Contract into existence just to carry
// this one Plan-generation-time constant would couple two lifecycles that
// do not otherwise overlap. Like every other Go-decided limit in this
// codebase (see process.defaultWorkflowMaxTasks), it is not caller input:
// the LLM never sees or influences this number, and Go rejects before any
// canonical Task is created.
const MaxGeneratedTasks = 5

// NormalizationError pairs ErrAssignmentUnresolved with a sanitized
// NormalizationFailureReason, following the same pattern as
// ceoplan.ParseError.
type NormalizationError struct {
	Reason NormalizationFailureReason
	err    error
}

func (normErr *NormalizationError) Error() string { return normErr.err.Error() }
func (normErr *NormalizationError) Unwrap() error { return normErr.err }

func newNormalizationError(reason NormalizationFailureReason, err error) *NormalizationError {
	return &NormalizationError{Reason: reason, err: err}
}

// NormalizeIntent is Go's deterministic construction of a Canonical CEO
// Plan from a small LLM Intent. It owns everything the LLM no longer
// decides: Task identity (via the reused NormalizeCandidate/proposal
// numbering), Employee assignment (via the existing
// organization.ResolveTaskAssignment policy), dependency ordering, the
// required_departments/assigned_existing_employees defaults, and — since
// ADR-0046 — canonical fallbacks for Objective/ProjectName when the LLM
// left them blank, sourced only from context (never a guess, never a new
// Provider call) — then hands the assembled candidate to the existing,
// unmodified NormalizeCandidate for final canonical validation. No
// business rule is duplicated here.
func NormalizeIntent(intent Intent, employees []organization.Identity, context IntentContext) (Plan, error) {
	// Validate the Organization roster once, up front, via the same check
	// NormalizeCandidate performs — fail fast on malformed employee data
	// (duplicate/empty ID or role) instead of misclassifying it as a
	// per-step assignment failure below.
	if _, _, err := employeeIndex(employees); err != nil {
		return Plan{}, err
	}

	generatedTaskCount := 0
	for _, step := range intent.Steps {
		if step.Kind != IntentStepReview {
			generatedTaskCount++
		}
	}
	if generatedTaskCount > MaxGeneratedTasks {
		return Plan{}, newNormalizationError(NormalizationExcessiveTaskCount,
			fmt.Errorf("%w: intent proposes %d Tasks, exceeding MaxGeneratedTasks (%d)", ErrAssignmentUnresolved, generatedTaskCount, MaxGeneratedTasks))
	}

	tasks := make([]candidateTask, 0, len(intent.Steps))
	assignedEmployees := make([]string, 0, len(intent.Steps))
	departments := make([]string, 0, len(intent.Steps))
	// groupMembers holds the ProposalIDs of every Task in the currently
	// "open" fan-out group; groupUpstream holds the dependency set every
	// member of that group shares. A step with ParallelWithPrevious==true
	// joins the current group (taking on the same groupUpstream every other
	// member has, so parallel siblings never depend on each other) without
	// closing it. Any other step (ParallelWithPrevious==false, or the very
	// first step, or a group of exactly one) depends on every current
	// member of the open group — for a plain sequential chain that is
	// always exactly one Task, reproducing the original linear-chain
	// behavior byte-for-byte; for a step arriving after two or more
	// parallel siblings, that is a genuine fan-in (Synthesis) depending on
	// all of them — and then opens a brand-new one-member group seeded by
	// this step, so a later step can fan out from *it* in turn. This stays
	// Go-decided and structural throughout: the LLM only ever supplies one
	// boolean per step (ParallelWithPrevious), never a dependency ID or
	// graph shape (ADR-0039's existing principle, extended to fan-out/
	// fan-in per ADR-0051).
	var groupMembers []string
	var groupUpstream []string

	for _, step := range intent.Steps {
		if step.Kind == IntentStepReview {
			// Review is already automatic per Task via the existing
			// Reviewed Workflow (ADR-0024); an explicit CEO Plan review
			// task would duplicate that with no independent meaning. It
			// never joins or closes a fan-out group either — the next real
			// step's grouping is unaffected by a review step in between.
			continue
		}

		assignment, err := resolveStepAssignment(step, employees)
		if err != nil {
			// employees was already validated above, so this indicates a
			// defect in ResolveTaskAssignment's own re-check rather than a
			// resolvable assignment outcome — surface it plainly.
			return Plan{}, fmt.Errorf("%w: %v", ErrAssignmentUnresolved, err)
		}
		switch assignment.Status {
		case organization.AssignmentResolved:
			// fall through to task construction below
		case organization.AssignmentNoMatch:
			return Plan{}, newNormalizationError(NormalizationAssignmentNoMatch,
				fmt.Errorf("%w: no employee currently holds role %q", ErrAssignmentUnresolved, assignment.RequiredRole))
		case organization.AssignmentAmbiguous:
			return Plan{}, newNormalizationError(NormalizationAssignmentAmbiguous,
				fmt.Errorf("%w: %d employees hold role %q", ErrAssignmentUnresolved, assignment.MatchCount, assignment.RequiredRole))
		default:
			return Plan{}, newNormalizationError(NormalizationAssignmentRequirementMissing,
				fmt.Errorf("%w: required_role is empty", ErrAssignmentUnresolved))
		}

		employeeID := *assignment.EmployeeID
		if !containsString(assignedEmployees, employeeID) {
			assignedEmployees = append(assignedEmployees, employeeID)
		}
		if department := employeeDepartment(employees, employeeID); department != "" && !containsString(departments, department) {
			departments = append(departments, department)
		}

		proposalID := fmt.Sprintf("PROPOSED-%03d", len(tasks)+1)
		joinsOpenGroup := step.ParallelWithPrevious && len(groupMembers) > 0
		var dependencyIDs []string
		if joinsOpenGroup {
			dependencyIDs = append([]string{}, groupUpstream...)
			groupMembers = append(groupMembers, proposalID)
		} else {
			dependencyIDs = append([]string{}, groupMembers...)
			groupUpstream = dependencyIDs
			groupMembers = []string{proposalID}
		}
		if dependencyIDs == nil {
			dependencyIDs = []string{}
		}

		assigneeID := employeeID
		tasks = append(tasks, candidateTask{
			Title: step.Description, RequiredRole: assignment.RequiredRole,
			AssigneeID: &assigneeID, DependencyIDs: dependencyIDs, Rationale: step.Description,
		})
	}

	if len(tasks) == 0 {
		return Plan{}, newNormalizationError(NormalizationAssignmentRequirementMissing,
			fmt.Errorf("%w: intent has no non-review steps", ErrAssignmentUnresolved))
	}

	candidate := candidatePlan{
		ProjectName:               canonicalProjectName(intent.ProjectName, context),
		Objective:                 canonicalObjective(intent.Objective, context),
		Summary:                   strings.TrimSpace(intent.Summary),
		RequiredDepartments:       departments,
		RequiredRoles:             []string{}, // NormalizeCandidate auto-collects this from tasks
		AssignedExistingEmployees: assignedEmployees,
		ProposedTasks:             tasks,
		Risks:                     []string{},
		CEOQuestions:              intent.CEOQuestions,
	}
	return NormalizeCandidate(candidate, employees)
}

// canonicalObjective returns the LLM-supplied objective, trimmed, or —
// when the LLM left it blank — context.Request, the caller-supplied
// canonical planning input (ADR-0046; in production this is
// interaction.Record.PlanningRequest(), not a raw Session.Request). This
// is not a guess: it reuses information Go already holds as canonical
// fact, never a re-summary or a fixed placeholder.
func canonicalObjective(objective string, context IntentContext) string {
	if trimmed := strings.TrimSpace(objective); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(context.Request)
}

// canonicalProjectName returns the LLM-supplied project name, trimmed, or
// — when the LLM left it blank — a deterministic fallback derived only
// from the Interaction Session's own stable, already-persisted identity
// (ADR-0046). No wall-clock time, no randomness, no Provider call: the
// same SessionID/RequestDigest always produces the same fallback, so
// Command Ledger replay stays consistent. The result is handed to the
// existing project name collision/auto-disambiguation policy unchanged —
// this is not a separate collision policy.
func canonicalProjectName(projectName string, context IntentContext) string {
	if trimmed := strings.TrimSpace(projectName); trimmed != "" {
		return trimmed
	}
	seed := strings.TrimSpace(context.SessionID) + ":" + strings.TrimSpace(context.RequestDigest)
	sum := sha256.Sum256([]byte(seed))
	return "依頼-" + hex.EncodeToString(sum[:])[:8]
}

// kindPreferredRoles lists Organization Role titles Go may try after an exact
// LLM required_role lookup fails with NoMatch. Only write steps receive a
// deterministic fallback today; this is step-kind policy, not fuzzy matching.
func kindPreferredRoles(kind IntentStepKind) []string {
	if kind == IntentStepWrite {
		return []string{"Content Writer"}
	}
	return nil
}

func roleCandidatesForStep(step IntentStep) []string {
	candidates := make([]string, 0, 2)
	if role := strings.TrimSpace(step.RequiredRole); role != "" {
		candidates = append(candidates, role)
	}
	for _, preferred := range kindPreferredRoles(step.Kind) {
		if !containsString(candidates, preferred) {
			candidates = append(candidates, preferred)
		}
	}
	return candidates
}

func resolveStepAssignment(step IntentStep, employees []organization.Identity) (organization.AssignmentResult, error) {
	candidates := roleCandidatesForStep(step)
	if len(candidates) == 0 {
		return organization.AssignmentResult{Status: organization.AssignmentRequirementMissing}, nil
	}
	var last organization.AssignmentResult
	for _, role := range candidates {
		assignment, err := organization.ResolveTaskAssignment(organization.AssignmentRequest{
			RequiredRole: role,
		}, employees)
		if err != nil {
			return organization.AssignmentResult{}, err
		}
		switch assignment.Status {
		case organization.AssignmentResolved:
			return assignment, nil
		case organization.AssignmentAmbiguous:
			return assignment, nil
		case organization.AssignmentNoMatch, organization.AssignmentRequirementMissing:
			last = assignment
		default:
			last = assignment
		}
	}
	if last.Status == "" {
		return organization.AssignmentResult{Status: organization.AssignmentRequirementMissing}, nil
	}
	return last, nil
}

func employeeDepartment(employees []organization.Identity, employeeID string) string {
	for _, employee := range employees {
		if employee.ID == employeeID {
			return employee.Department
		}
	}
	return ""
}
