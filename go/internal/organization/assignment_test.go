package organization

import (
	"errors"
	"testing"
)

func TestResolveTaskAssignmentUsesOnlyUniqueExactRoleMatch(t *testing.T) {
	employees := []Identity{
		{ID: "PLAN-001", Role: "Product Manager"},
		{ID: "DEV-001", Role: "Backend Engineer"},
	}
	result, err := ResolveTaskAssignment(AssignmentRequest{RequiredRole: "product manager"}, employees)
	if err != nil || result.Status != AssignmentResolved || result.EmployeeID == nil || *result.EmployeeID != "PLAN-001" || result.RequiredRole != "Product Manager" {
		t.Fatalf("result=%#v err=%v", result, err)
	}

	noMatch, err := ResolveTaskAssignment(AssignmentRequest{RequiredRole: "Writer"}, employees)
	if err != nil || noMatch.Status != AssignmentNoMatch || noMatch.EmployeeID != nil {
		t.Fatalf("noMatch=%#v err=%v", noMatch, err)
	}

	ambiguousEmployees := append(employees, Identity{ID: "PLAN-002", Role: "Product Manager"})
	ambiguous, err := ResolveTaskAssignment(AssignmentRequest{RequiredRole: "Product Manager"}, ambiguousEmployees)
	if err != nil || ambiguous.Status != AssignmentAmbiguous || ambiguous.EmployeeID != nil || ambiguous.MatchCount != 2 {
		t.Fatalf("ambiguous=%#v err=%v", ambiguous, err)
	}
}

func TestResolveTaskAssignmentValidatesProviderProposal(t *testing.T) {
	employees := []Identity{{ID: "PLAN-001", Role: "Product Manager"}}
	id := "PLAN-001"
	result, err := ResolveTaskAssignment(AssignmentRequest{RequiredRole: "Product Manager", ProposedEmployeeID: &id}, employees)
	if err != nil || result.EmployeeID == nil || *result.EmployeeID != id {
		t.Fatalf("result=%#v err=%v", result, err)
	}

	if _, err := ResolveTaskAssignment(AssignmentRequest{RequiredRole: "Writer", ProposedEmployeeID: &id}, employees); !errors.Is(err, ErrProposedRoleMismatch) {
		t.Fatalf("role mismatch err=%v", err)
	}
	unknown := "UNKNOWN-001"
	if _, err := ResolveTaskAssignment(AssignmentRequest{RequiredRole: "Writer", ProposedEmployeeID: &unknown}, employees); !errors.Is(err, ErrUnknownProposedEmployee) {
		t.Fatalf("unknown employee err=%v", err)
	}
	ambiguous, err := ResolveTaskAssignment(AssignmentRequest{RequiredRole: "Product Manager", ProposedEmployeeID: &id}, append(employees, Identity{ID: "PLAN-002", Role: "Product Manager"}))
	if err != nil || ambiguous.Status != AssignmentAmbiguous || ambiguous.EmployeeID != nil {
		t.Fatalf("Provider proposal bypassed ambiguous role: %#v err=%v", ambiguous, err)
	}
}

func TestResolveTaskAssignmentDoesNotGuessWithoutRole(t *testing.T) {
	result, err := ResolveTaskAssignment(AssignmentRequest{}, []Identity{{ID: "PLAN-001", Role: "Product Manager"}})
	if err != nil || result.Status != AssignmentRequirementMissing || result.EmployeeID != nil {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestResolveReviewerAssignmentExcludesMakerAndUsesAllowList(t *testing.T) {
	employees := []Identity{
		{ID: "CONTENT-001", Role: "Content Writer"},
		{ID: "QA-001", Role: "QA Engineer"},
	}
	result, err := ResolveReviewerAssignment(ReviewerAssignmentRequest{
		RequiredRole: "QA Engineer", MakerEmployeeIDs: []string{"CONTENT-001"},
		AllowedEmployeeIDs: []string{"CONTENT-001", "QA-001"},
	}, employees)
	if err != nil || result.Status != AssignmentResolved || result.EmployeeID == nil || *result.EmployeeID != "QA-001" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	explicit := "QA-001"
	explicitResult, err := ResolveReviewerAssignment(ReviewerAssignmentRequest{
		RequiredRole: "QA Engineer", MakerEmployeeIDs: []string{"CONTENT-001"},
		AllowedEmployeeIDs: []string{"CONTENT-001", "QA-001"}, ProposedEmployeeID: &explicit,
	}, append(employees, Identity{ID: "QA-002", Role: "QA Engineer"}))
	if err != nil || explicitResult.Status != AssignmentResolved || explicitResult.EmployeeID == nil || *explicitResult.EmployeeID != explicit {
		t.Fatalf("explicit result=%#v err=%v", explicitResult, err)
	}

	selfOnly, err := ResolveReviewerAssignment(ReviewerAssignmentRequest{
		RequiredRole: "QA Engineer", MakerEmployeeIDs: []string{"QA-001"},
	}, employees)
	if err != nil || selfOnly.Status != AssignmentNoMatch || selfOnly.EmployeeID != nil {
		t.Fatalf("self-review candidate=%#v err=%v", selfOnly, err)
	}
	if _, err := ResolveReviewerAssignment(ReviewerAssignmentRequest{
		RequiredRole: "QA Engineer", MakerEmployeeIDs: []string{"QA-001"}, ProposedEmployeeID: &explicit,
	}, employees); !errors.Is(err, ErrProposedReviewerDenied) {
		t.Fatalf("explicit self-review err=%v", err)
	}
	invalid := "CONTENT-001"
	if _, err := ResolveReviewerAssignment(ReviewerAssignmentRequest{
		RequiredRole: "QA Engineer", MakerEmployeeIDs: []string{"PLAN-001"}, ProposedEmployeeID: &invalid,
	}, employees); !errors.Is(err, ErrProposedRoleMismatch) {
		t.Fatalf("invalid explicit reviewer err=%v", err)
	}

	excluded, err := ResolveReviewerAssignment(ReviewerAssignmentRequest{
		RequiredRole: "QA Engineer", MakerEmployeeIDs: []string{"CONTENT-001"}, AllowedEmployeeIDs: []string{"CONTENT-001"},
	}, employees)
	if err != nil || excluded.Status != AssignmentNoMatch || excluded.EmployeeID != nil {
		t.Fatalf("Autonomy-excluded reviewer=%#v err=%v", excluded, err)
	}
}

func TestResolveReviewerAssignmentDeniesZeroAndMultipleCandidates(t *testing.T) {
	zero, err := ResolveReviewerAssignment(ReviewerAssignmentRequest{
		RequiredRole: "QA Engineer", MakerEmployeeIDs: []string{"CONTENT-001"},
	}, []Identity{{ID: "CONTENT-001", Role: "Content Writer"}})
	if err != nil || zero.Status != AssignmentNoMatch {
		t.Fatalf("zero=%#v err=%v", zero, err)
	}
	multiple, err := ResolveReviewerAssignment(ReviewerAssignmentRequest{
		RequiredRole: "QA Engineer", MakerEmployeeIDs: []string{"CONTENT-001"},
	}, []Identity{{ID: "CONTENT-001", Role: "Content Writer"}, {ID: "QA-001", Role: "QA Engineer"}, {ID: "QA-002", Role: "QA Engineer"}})
	if err != nil || multiple.Status != AssignmentAmbiguous || multiple.MatchCount != 2 || multiple.EmployeeID != nil {
		t.Fatalf("multiple=%#v err=%v", multiple, err)
	}
}
