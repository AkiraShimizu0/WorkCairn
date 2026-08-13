package process

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/organization"
)

func TestWorkspaceSetupBuildsStarterOrganizationThroughExistingWriterAndReplays(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 8, 13, 15, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	candidates := []organization.EmployeeCandidate{
		{ID: "PLAN-001", Name: "田中 美咲", Department: "企画部", Role: "Product Manager", Model: "workcairn-auto"},
		{ID: "CONTENT-001", Name: "佐藤 葵", Department: "コンテンツ部", Role: "Content Writer", Model: "workcairn-auto"},
		{ID: "QA-001", Name: "伊藤 健太", Department: "品質保証部", Role: "QA Engineer", Model: "workcairn-auto"},
	}
	input := WorkspaceSetupInput{VaultRoot: root, Candidates: candidates, CurrentTime: at, CommandID: "CMD-WORKSPACE-SETUP-001"}
	result, err := ExecuteWorkspaceSetup(context.Background(), input, true)
	if err != nil || !result.Complete || len(result.Employees) != 3 || !result.Layout.StateCreated {
		t.Fatalf("setup=%#v err=%v", result, err)
	}
	inspection, err := InspectOrganization(context.Background(), root)
	if err != nil || len(inspection.Inventory.Employees) != 3 || len(inspection.ValidationIssues) != 0 {
		t.Fatalf("inventory=%#v err=%v", inspection, err)
	}
	replayed, err := ExecuteWorkspaceSetup(context.Background(), input, true)
	if err != nil || !replayed.Complete || len(replayed.Employees) != 3 {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
}

func TestWorkspaceSetupRequiresApprovalBeforeCreatingLayout(t *testing.T) {
	root := t.TempDir()
	input := WorkspaceSetupInput{
		VaultRoot: root,
		Candidates: []organization.EmployeeCandidate{{
			ID: "PLAN-001", Name: "田中 美咲", Department: "企画部", Role: "Product Manager", Model: "workcairn-auto",
		}},
		CurrentTime: time.Date(2026, 8, 13, 15, 0, 0, 0, time.FixedZone("JST", 9*60*60)),
		CommandID:   "CMD-WORKSPACE-SETUP-DENIED-001",
	}
	if _, err := ExecuteWorkspaceSetup(context.Background(), input, false); err == nil {
		t.Fatal("unapproved Workspace setup must fail")
	}
	if _, err := os.Stat(filepath.Join(root, "会社")); !os.IsNotExist(err) {
		t.Fatalf("unapproved setup created layout: %v", err)
	}
}
