package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/goal"
)

func vaultGoal(t *testing.T, scope goal.Scope, projectName string) goal.Record {
	t.Helper()
	record, err := goal.NewActive("GOAL-onboarding-activation", scope, projectName, "Improve onboarding activation", "80% of new users complete onboarding within 7 days", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestWorkspaceGoalStoreAtomicCreateListAndCAS(t *testing.T) {
	root := t.TempDir()
	store, err := NewWorkspaceGoalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record := vaultGoal(t, goal.ScopeCompany, "")
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), record); !errors.Is(err, goal.ErrAlreadyExists) {
		t.Fatalf("duplicate Create() error = %v", err)
	}
	stored, err := store.Get(context.Background(), record.GoalID)
	if err != nil || stored.Status != goal.StatusActive || stored.Version != 1 {
		t.Fatalf("Get() = %#v, %v", stored, err)
	}
	records, err := store.List(context.Background())
	if err != nil || len(records) != 1 || records[0].GoalID != record.GoalID {
		t.Fatalf("List() = %#v, %v", records, err)
	}
	achieved, err := stored.Achieve()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), achieved, stored.Version); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), achieved, stored.Version); !errors.Is(err, goal.ErrVersionConflict) {
		t.Fatalf("stale Update() error = %v", err)
	}
	terminal, err := store.Get(context.Background(), record.GoalID)
	if err != nil || terminal.Status != goal.StatusAchieved || terminal.Version != 2 {
		t.Fatalf("terminal Goal = %#v, %v", terminal, err)
	}
}

func TestWorkspaceGoalStoreDirectoryLayout(t *testing.T) {
	root := t.TempDir()
	store, err := NewWorkspaceGoalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record := vaultGoal(t, goal.ScopeCompany, "")
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "会社", "Goals")
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var sawJSON, sawMarkdown bool
	for _, entry := range entries {
		switch {
		case strings.HasSuffix(entry.Name(), ".json"):
			sawJSON = true
		case strings.HasSuffix(entry.Name(), ".md"):
			sawMarkdown = true
		}
	}
	if !sawJSON || !sawMarkdown {
		t.Fatalf("会社/Goals/ entries = %v, want both a .json and a .md file", entries)
	}
}

func TestProjectGoalStoreRequiresExistingProjectDirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := NewGoalStore(root, "Onboarding"); !errors.Is(err, ErrDocumentNotFound) {
		t.Fatalf("NewGoalStore() with no Project directory, error = %v, want ErrDocumentNotFound", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト", "Onboarding"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := NewGoalStore(root, "Onboarding")
	if err != nil {
		t.Fatal(err)
	}
	record := vaultGoal(t, goal.ScopeProject, "Onboarding")
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "プロジェクト", "Onboarding", "Goals")
	if _, err := os.Stat(directory); err != nil {
		t.Fatalf("プロジェクト/Onboarding/Goals/ does not exist: %v", err)
	}
}

func TestGoalStoreMarkdownProjectionContent(t *testing.T) {
	root := t.TempDir()
	store, err := NewWorkspaceGoalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record := vaultGoal(t, goal.ScopeCompany, "")
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(store.markdownPath(record.GoalID))
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(content)
	for _, want := range []string{record.Title, record.GoalID, string(record.Scope), string(record.Status), record.Outcome} {
		if !strings.Contains(rendered, want) {
			t.Errorf("Markdown projection = %q, want it to contain %q", rendered, want)
		}
	}
	for _, forbidden := range []string{"Prompt", "Model", "Persona", "Skill", "Agent"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("Markdown projection unexpectedly contains %q: %q", forbidden, rendered)
		}
	}
}

func TestGoalStoreListSortsByCreatedThenID(t *testing.T) {
	root := t.TempDir()
	store, err := NewWorkspaceGoalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	second, err := goal.NewActive("GOAL-b", goal.ScopeCompany, "", "Second", "Outcome", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	first, err := goal.NewActive("GOAL-a", goal.ScopeCompany, "", "First", "Outcome", now)
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
	if records[0].GoalID != first.GoalID || records[1].GoalID != second.GoalID {
		t.Fatalf("List() order = [%s, %s], want [%s, %s]", records[0].GoalID, records[1].GoalID, first.GoalID, second.GoalID)
	}
}
