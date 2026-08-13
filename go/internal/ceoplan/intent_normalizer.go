package ceoplan

import (
	"errors"
	"fmt"

	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
)

var ErrAssignmentUnresolved = errors.New("CEO plan intent assignment could not be resolved")

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
)

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
// organization.ResolveTaskAssignment policy), dependency ordering, and the
// required_departments/assigned_existing_employees defaults — then hands
// the assembled candidate to the existing, unmodified NormalizeCandidate
// for final canonical validation. No business rule is duplicated here.
func NormalizeIntent(intent Intent, employees []organization.Identity) (Plan, error) {
	// Validate the Organization roster once, up front, via the same check
	// NormalizeCandidate performs — fail fast on malformed employee data
	// (duplicate/empty ID or role) instead of misclassifying it as a
	// per-step assignment failure below.
	if _, _, err := employeeIndex(employees); err != nil {
		return Plan{}, err
	}

	tasks := make([]candidateTask, 0, len(intent.Steps))
	assignedEmployees := make([]string, 0, len(intent.Steps))
	departments := make([]string, 0, len(intent.Steps))
	previousProposalID := ""

	for _, step := range intent.Steps {
		if step.Kind == IntentStepReview {
			// Review is already automatic per Task via the existing
			// Reviewed Workflow (ADR-0024); an explicit CEO Plan review
			// task would duplicate that with no independent meaning. It
			// contributes nothing to the dependency chain either — the
			// next real step still depends on the last real step.
			continue
		}

		assignment, err := organization.ResolveTaskAssignment(organization.AssignmentRequest{
			RequiredRole: step.RequiredRole,
		}, employees)
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
				fmt.Errorf("%w: no employee currently holds role %q", ErrAssignmentUnresolved, step.RequiredRole))
		case organization.AssignmentAmbiguous:
			return Plan{}, newNormalizationError(NormalizationAssignmentAmbiguous,
				fmt.Errorf("%w: %d employees hold role %q", ErrAssignmentUnresolved, assignment.MatchCount, step.RequiredRole))
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

		dependencyIDs := []string{}
		if previousProposalID != "" {
			dependencyIDs = []string{previousProposalID}
		}
		proposalID := fmt.Sprintf("PROPOSED-%03d", len(tasks)+1)
		previousProposalID = proposalID

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
		ProjectName: intent.ProjectName, Objective: intent.Objective, Summary: intent.Summary,
		RequiredDepartments:       departments,
		RequiredRoles:             []string{}, // NormalizeCandidate auto-collects this from tasks
		AssignedExistingEmployees: assignedEmployees,
		ProposedTasks:             tasks,
		Risks:                     []string{},
		CEOQuestions:              intent.CEOQuestions,
	}
	return NormalizeCandidate(candidate, employees)
}

func employeeDepartment(employees []organization.Identity, employeeID string) string {
	for _, employee := range employees {
		if employee.ID == employeeID {
			return employee.Department
		}
	}
	return ""
}
