package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/responsibility"
)

func vaultResponsibility(t *testing.T, scope responsibility.Scope, projectName string) responsibility.Record {
	t.Helper()
	record, err := responsibility.New("RESP-onboarding-quality", scope, projectName, "Improve onboarding quality", []string{"GOAL-1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestWorkspaceResponsibilityStoreAtomicCreateListAndCAS(t *testing.T) {
	root := t.TempDir()
	store, err := NewWorkspaceResponsibilityStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record := vaultResponsibility(t, responsibility.ScopeCompany, "")
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), record); !errors.Is(err, responsibility.ErrAlreadyExists) {
		t.Fatalf("duplicate Create() error = %v", err)
	}
	stored, err := store.Get(context.Background(), record.ResponsibilityID)
	if err != nil || stored.Status != responsibility.StatusActive || stored.Version != 1 {
		t.Fatalf("Get() = %#v, %v", stored, err)
	}
	records, err := store.List(context.Background())
	if err != nil || len(records) != 1 || records[0].ResponsibilityID != record.ResponsibilityID {
		t.Fatalf("List() = %#v, %v", records, err)
	}
	inactive, err := stored.Deactivate()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), inactive, stored.Version); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), inactive, stored.Version); !errors.Is(err, responsibility.ErrVersionConflict) {
		t.Fatalf("stale Update() error = %v", err)
	}
	current, err := store.Get(context.Background(), record.ResponsibilityID)
	if err != nil || current.Status != responsibility.StatusInactive || current.Version != 2 {
		t.Fatalf("current Responsibility = %#v, %v", current, err)
	}
}

func TestProjectResponsibilityStoreRequiresExistingProjectDirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := NewResponsibilityStore(root, "Onboarding"); !errors.Is(err, ErrDocumentNotFound) {
		t.Fatalf("NewResponsibilityStore() with no Project directory, error = %v, want ErrDocumentNotFound", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト", "Onboarding"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := NewResponsibilityStore(root, "Onboarding")
	if err != nil {
		t.Fatal(err)
	}
	record := vaultResponsibility(t, responsibility.ScopeProject, "Onboarding")
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "プロジェクト", "Onboarding", "Responsibilities")
	if _, err := os.Stat(directory); err != nil {
		t.Fatalf("プロジェクト/Onboarding/Responsibilities/ does not exist: %v", err)
	}
}

func TestResponsibilityStoreMarkdownProjectionContent(t *testing.T) {
	root := t.TempDir()
	store, err := NewWorkspaceResponsibilityStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record := vaultResponsibility(t, responsibility.ScopeCompany, "")
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(store.markdownPath(record.ResponsibilityID))
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(content)
	for _, want := range []string{record.Title, record.ResponsibilityID, string(record.Scope), string(record.Status), "GOAL-1"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("Markdown projection = %q, want it to contain %q", rendered, want)
		}
	}
	for _, forbidden := range []string{"Prompt", "Model", "Persona", "Skill", "Agent", "employee_id", "Bound"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("Markdown projection unexpectedly contains %q: %q", forbidden, rendered)
		}
	}
}

// TestResponsibilityBindingIsASeparateCanonicalFile confirms Binding's CAS
// lineage is fully independent of Record's -- reassigning/unassigning
// never touches the Responsibility's own JSON or Markdown file.
func TestResponsibilityBindingIsASeparateCanonicalFile(t *testing.T) {
	root := t.TempDir()
	store, err := NewWorkspaceResponsibilityStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record := vaultResponsibility(t, responsibility.ScopeCompany, "")
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetBinding(context.Background(), record.ResponsibilityID); !errors.Is(err, responsibility.ErrNotFound) {
		t.Fatalf("GetBinding() before any assignment, error = %v, want ErrNotFound", err)
	}

	recordBefore, err := os.ReadFile(store.jsonPath(record.ResponsibilityID))
	if err != nil {
		t.Fatal(err)
	}

	binding, err := responsibility.NewBinding(record.ResponsibilityID, "PM-101")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBinding(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBinding(context.Background(), binding); !errors.Is(err, responsibility.ErrAlreadyExists) {
		t.Fatalf("duplicate CreateBinding() error = %v", err)
	}

	reassigned, err := binding.WithEmployee("PM-102")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateBinding(context.Background(), reassigned, binding.Version); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetBinding(context.Background(), record.ResponsibilityID)
	if err != nil || current.EmployeeID != "PM-102" || current.Version != 2 {
		t.Fatalf("GetBinding() after reassign = %#v, %v", current, err)
	}

	recordAfter, err := os.ReadFile(store.jsonPath(record.ResponsibilityID))
	if err != nil {
		t.Fatal(err)
	}
	if string(recordBefore) != string(recordAfter) {
		t.Fatalf("Responsibility record JSON changed after a Binding update -- before=%q after=%q", recordBefore, recordAfter)
	}
}

func TestResponsibilityStoreListSortsByCreatedThenID(t *testing.T) {
	root := t.TempDir()
	store, err := NewWorkspaceResponsibilityStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	second, err := responsibility.New("RESP-b", responsibility.ScopeCompany, "", "Second", nil, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	first, err := responsibility.New("RESP-a", responsibility.ScopeCompany, "", "First", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	records, err := store.List(context.Background())
	if err != nil || len(records) != 2 {
		t.Fatalf("List() = %#v, %v", records, err)
	}
	if records[0].ResponsibilityID != first.ResponsibilityID || records[1].ResponsibilityID != second.ResponsibilityID {
		t.Fatalf("List() order = [%s, %s], want [%s, %s]", records[0].ResponsibilityID, records[1].ResponsibilityID, first.ResponsibilityID, second.ResponsibilityID)
	}
}
