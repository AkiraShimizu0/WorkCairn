package vault

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapWorkspaceLayoutUsesOnlySelectedRootAndIsRepeatable(t *testing.T) {
	root := t.TempDir()
	result, err := BootstrapWorkspaceLayout(context.Background(), root)
	if err != nil || !result.DirectoriesReady || !result.StateReady || !result.StateCreated || !result.EffectCommitted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	for _, relative := range []string{"社員", "会社", "プロジェクト"} {
		if info, statErr := os.Stat(filepath.Join(root, relative)); statErr != nil || !info.IsDir() {
			t.Fatalf("directory %s info=%#v err=%v", relative, info, statErr)
		}
	}
	before, _ := os.ReadFile(filepath.Join(root, "会社", "Workspace State.md"))
	repeated, err := BootstrapWorkspaceLayout(context.Background(), root)
	after, _ := os.ReadFile(filepath.Join(root, "会社", "Workspace State.md"))
	if err != nil || repeated.StateCreated || repeated.EffectCommitted || string(before) != string(after) {
		t.Fatalf("repeat=%#v err=%v changed=%t", repeated, err, string(before) != string(after))
	}
}

func TestBootstrapWorkspaceLayoutRejectsInvalidExistingStateWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "会社", "Workspace State.md")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	const invalid = "# Personal note\n"
	if err := os.WriteFile(statePath, []byte(invalid), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := BootstrapWorkspaceLayout(context.Background(), root); err == nil {
		t.Fatal("invalid existing Workspace State must be rejected")
	}
	after, err := os.ReadFile(statePath)
	if err != nil || string(after) != invalid {
		t.Fatalf("existing state changed: %q err=%v", after, err)
	}
	if _, err := BootstrapWorkspaceLayout(nil, root); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil context error = %v", err)
	}
}
