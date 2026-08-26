package localos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestHeadlessClaudeCredentialStoreReadsOnlyPrivateOwnedRegularFile(t *testing.T) {
	configRoot := t.TempDir()
	store, err := NewHeadlessClaudeCredentialStore(configRoot)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(configRoot, "WorkCairn", "credentials")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "fake-headless-secret"
	if err := os.WriteFile(filepath.Join(directory, "anthropic-api-key"), []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	credential, err := store.Load(context.Background())
	if err != nil || credential != secret {
		t.Fatalf("Load = %q, %v", credential, err)
	}
	if _, err := store.RequestAndStore(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("RequestAndStore error = %v", err)
	}
}

func TestHeadlessClaudeCredentialStoreRejectsMissingUnsafeAndInvalidFilesWithoutExposure(t *testing.T) {
	const secret = "secret-must-not-escape"
	tests := []struct {
		name           string
		prepare        func(t *testing.T, path string)
		classification CredentialFailure
	}{
		{name: "missing", classification: CredentialFileNotFound},
		{name: "unsafe permissions", classification: CredentialFileUnsafe, prepare: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(secret), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", classification: CredentialFileUnsafe, prepare: func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "target")
			if err := os.WriteFile(target, []byte(secret), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "empty", classification: CredentialFileOutputInvalid, prepare: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configRoot := t.TempDir()
			directory := filepath.Join(configRoot, "WorkCairn", "credentials")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "anthropic-api-key")
			if test.prepare != nil {
				test.prepare(t, path)
			}
			store, err := NewHeadlessClaudeCredentialStore(configRoot)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.Load(context.Background())
			var credentialErr *CredentialError
			if !errors.As(err, &credentialErr) || credentialErr.Classification != test.classification {
				t.Fatalf("Load error = %#v, want %s", err, test.classification)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), path) {
				t.Fatal("credential error exposed secret or local path")
			}
		})
	}
}
