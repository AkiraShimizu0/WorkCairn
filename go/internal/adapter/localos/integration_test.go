package localos

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceLocationStoreRoundTripUsesAtomicPrivateConfig(t *testing.T) {
	configRoot := t.TempDir()
	workspace := t.TempDir()
	store, err := NewWorkspaceLocationStore(configRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Load before Save error = %v", err)
	}
	if err := store.Save(workspace); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || loaded != workspace {
		t.Fatalf("Load = %q, %v", loaded, err)
	}
	info, err := os.Stat(filepath.Join(configRoot, "WorkCairn", "workspace.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("workspace config mode = %v, %v", info.Mode(), err)
	}
	entries, _ := os.ReadDir(filepath.Join(configRoot, "WorkCairn"))
	if len(entries) != 1 || entries[0].Name() != "workspace.json" {
		t.Fatalf("config entries = %#v", entries)
	}
}

func TestValidateWorkspaceRootDefaultsDenyBroadAndUnrelatedDirectories(t *testing.T) {
	empty := t.TempDir()
	if root, err := ValidateWorkspaceRoot(empty); err != nil || root != empty {
		t.Fatalf("empty dedicated root = %q, %v", root, err)
	}
	if err := os.WriteFile(filepath.Join(empty, ".DS_Store"), []byte("metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateWorkspaceRoot(empty); err != nil {
		t.Fatalf("OS metadata-only root rejected: %v", err)
	}
	unrelated := t.TempDir()
	if err := os.WriteFile(filepath.Join(unrelated, "notes.md"), []byte("not WorkCairn"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateWorkspaceRoot(unrelated); err == nil {
		t.Fatal("unrelated non-empty directory was accepted")
	}
	existing := t.TempDir()
	if err := os.Mkdir(filepath.Join(existing, "会社"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "会社", "Workspace State.md"), []byte("# WorkCairn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateWorkspaceRoot(existing); err != nil {
		t.Fatalf("existing WorkCairn root rejected: %v", err)
	}
}
