package organization

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/text/cases"
)

var (
	ErrInvalidAssignment       = errors.New("invalid task assignment")
	ErrUnknownProposedEmployee = errors.New("unknown proposed employee")
	ErrProposedRoleMismatch    = errors.New("proposed employee role mismatch")
	ErrProposedReviewerDenied  = errors.New("proposed reviewer is not eligible")
)

type AssignmentStatus string

const (
	AssignmentResolved           AssignmentStatus = "resolved"
	AssignmentRequirementMissing AssignmentStatus = "requirement_missing"
	AssignmentNoMatch            AssignmentStatus = "no_match"
	AssignmentAmbiguous          AssignmentStatus = "ambiguous"
)

type AssignmentRequest struct {
	RequiredRole       string
	ProposedEmployeeID *string
}

type AssignmentResult struct {
	Status       AssignmentStatus
	RequiredRole string
	EmployeeID   *string
	MatchCount   int
}

type ReviewerAssignmentRequest struct {
	RequiredRole       string
	MakerEmployeeIDs   []string
	AllowedEmployeeIDs []string
	ProposedEmployeeID *string
}

var assignmentRoleCaseFolder = cases.Fold()

// ResolveTaskAssignment validates a Provider-proposed employee or resolves an
// unassigned task only when its required role has exactly one employee. It
// intentionally does not rank, guess, or select among multiple candidates.
func ResolveTaskAssignment(request AssignmentRequest, employees []Identity) (AssignmentResult, error) {
	requiredRole := strings.TrimSpace(request.RequiredRole)
	byID := make(map[string]Identity, len(employees))
	for _, employee := range employees {
		id := strings.TrimSpace(employee.ID)
		role := strings.TrimSpace(employee.Role)
		if id == "" || role == "" {
			return AssignmentResult{}, fmt.Errorf("%w: employee identity", ErrInvalidAssignment)
		}
		if _, exists := byID[id]; exists {
			return AssignmentResult{}, fmt.Errorf("%w: duplicate employee ID %s", ErrInvalidAssignment, id)
		}
		byID[id] = employee
	}

	var proposedID string
	if request.ProposedEmployeeID != nil {
		proposedID = strings.TrimSpace(*request.ProposedEmployeeID)
		employee, exists := byID[proposedID]
		if proposedID == "" || !exists {
			return AssignmentResult{}, fmt.Errorf("%w: %s", ErrUnknownProposedEmployee, proposedID)
		}
		actualRole := strings.TrimSpace(employee.Role)
		if requiredRole != "" && assignmentRoleCaseFolder.String(actualRole) != assignmentRoleCaseFolder.String(requiredRole) {
			return AssignmentResult{}, fmt.Errorf("%w: employee %s", ErrProposedRoleMismatch, proposedID)
		}
		requiredRole = actualRole
	}

	if requiredRole == "" {
		return AssignmentResult{Status: AssignmentRequirementMissing}, nil
	}
	matches := make([]string, 0, 1)
	canonicalRole := assignmentRoleCaseFolder.String(requiredRole)
	for _, employee := range employees {
		if assignmentRoleCaseFolder.String(strings.TrimSpace(employee.Role)) == canonicalRole {
			matches = append(matches, strings.TrimSpace(employee.ID))
		}
	}
	if len(matches) == 0 {
		return AssignmentResult{Status: AssignmentNoMatch, RequiredRole: requiredRole}, nil
	}
	if len(matches) > 1 {
		return AssignmentResult{Status: AssignmentAmbiguous, RequiredRole: requiredRole, MatchCount: len(matches)}, nil
	}
	if proposedID != "" && proposedID != matches[0] {
		return AssignmentResult{}, fmt.Errorf("%w: employee %s", ErrProposedRoleMismatch, proposedID)
	}
	return AssignmentResult{
		Status: AssignmentResolved, RequiredRole: strings.TrimSpace(byID[matches[0]].Role),
		EmployeeID: stringPointer(matches[0]), MatchCount: 1,
	}, nil
}

// ResolveReviewerAssignment applies the same exact-role, unique-candidate
// policy as Task assignment after excluding the Maker and intersecting an
// optional Autonomy allow-list. It never accepts a UI-selected reviewer as a
// source of truth.
func ResolveReviewerAssignment(request ReviewerAssignmentRequest, employees []Identity) (AssignmentResult, error) {
	makers := make(map[string]struct{}, len(request.MakerEmployeeIDs))
	for _, makerID := range request.MakerEmployeeIDs {
		makerID = strings.TrimSpace(makerID)
		if makerID == "" {
			return AssignmentResult{}, fmt.Errorf("%w: Maker employee ID", ErrInvalidAssignment)
		}
		makers[makerID] = struct{}{}
	}
	if len(makers) == 0 {
		return AssignmentResult{}, fmt.Errorf("%w: Maker employee IDs", ErrInvalidAssignment)
	}
	allowed := map[string]struct{}{}
	if request.AllowedEmployeeIDs != nil {
		for _, employeeID := range request.AllowedEmployeeIDs {
			employeeID = strings.TrimSpace(employeeID)
			if employeeID == "" {
				return AssignmentResult{}, fmt.Errorf("%w: allowed employee ID", ErrInvalidAssignment)
			}
			allowed[employeeID] = struct{}{}
		}
	}
	candidates := make([]Identity, 0, len(employees))
	for _, employee := range employees {
		if _, maker := makers[strings.TrimSpace(employee.ID)]; maker {
			continue
		}
		if request.AllowedEmployeeIDs != nil {
			if _, exists := allowed[strings.TrimSpace(employee.ID)]; !exists {
				continue
			}
		}
		candidates = append(candidates, employee)
	}
	if request.ProposedEmployeeID != nil {
		proposedID := strings.TrimSpace(*request.ProposedEmployeeID)
		for _, candidate := range candidates {
			if strings.TrimSpace(candidate.ID) != proposedID {
				continue
			}
			return ResolveTaskAssignment(AssignmentRequest{
				RequiredRole: request.RequiredRole, ProposedEmployeeID: &proposedID,
			}, []Identity{candidate})
		}
		return AssignmentResult{}, fmt.Errorf("%w: %s", ErrProposedReviewerDenied, proposedID)
	}
	return ResolveTaskAssignment(AssignmentRequest{RequiredRole: request.RequiredRole}, candidates)
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}
