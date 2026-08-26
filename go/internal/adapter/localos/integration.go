package localos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrNotConfigured = errors.New("local OS setting is not configured")
	ErrCanceled      = errors.New("local OS interaction was canceled")
	ErrUnsupported   = errors.New("local OS integration is unsupported")
)

const workspaceConfigVersion = "workcairn-local-workspace.v1"

type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, stdin string) (string, error)
}

type CredentialFailure string

const (
	CredentialNotFound          CredentialFailure = "keychain_not_found"
	CredentialPermissionDenied  CredentialFailure = "keychain_permission_denied"
	CredentialCommandFailed     CredentialFailure = "keychain_command_failed"
	CredentialOutputInvalid     CredentialFailure = "keychain_output_invalid"
	CredentialSetupTimeout      CredentialFailure = "keychain_setup_timeout"
	CredentialUnavailable       CredentialFailure = "keychain_unavailable"
	CredentialFileNotFound      CredentialFailure = "credential_file_not_found"
	CredentialFilePermission    CredentialFailure = "credential_file_permission_denied"
	CredentialFileUnsafe        CredentialFailure = "credential_file_unsafe"
	CredentialFileOutputInvalid CredentialFailure = "credential_file_output_invalid"
)

const (
	CredentialWrite          = "keychain_write"
	CredentialRead           = "keychain_read"
	CredentialReadAfterWrite = "keychain_read_after_write"
	CredentialInput          = "credential_input"
	CredentialHeadlessRead   = "headless_local_read"
)

// CredentialError is a secret-free diagnostic crossing the Local OS Adapter
// boundary. It intentionally carries no command output or credential data.
type CredentialError struct {
	Substage       string
	Classification CredentialFailure
}

func (credentialErr *CredentialError) Error() string {
	return fmt.Sprintf("Claude credential %s failed: %s", credentialErr.Substage, credentialErr.Classification)
}

func (credentialErr *CredentialError) CredentialSubstage() string { return credentialErr.Substage }

func (credentialErr *CredentialError) CredentialClassification() string {
	return string(credentialErr.Classification)
}

type WorkspaceLocationStore struct {
	path string
}

type workspaceLocation struct {
	Version string `json:"version"`
	Root    string `json:"root"`
}

func NewWorkspaceLocationStore(configRoot string) (*WorkspaceLocationStore, error) {
	if strings.TrimSpace(configRoot) == "" {
		return nil, errors.New("config root is required")
	}
	return &WorkspaceLocationStore{path: filepath.Join(configRoot, "WorkCairn", "workspace.json")}, nil
}

func (store *WorkspaceLocationStore) Load() (string, error) {
	content, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotConfigured
	}
	if err != nil {
		return "", fmt.Errorf("read workspace location: %w", err)
	}
	var location workspaceLocation
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&location); err != nil || location.Version != workspaceConfigVersion {
		return "", errors.New("stored workspace location is invalid")
	}
	root, err := ValidateWorkspaceRoot(location.Root)
	if err != nil {
		return "", fmt.Errorf("stored workspace location: %w", err)
	}
	return root, nil
}

func (store *WorkspaceLocationStore) Save(root string) error {
	root, err := ValidateWorkspaceRoot(root)
	if err != nil {
		return err
	}
	content, err := json.MarshalIndent(workspaceLocation{Version: workspaceConfigVersion, Root: root}, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return fmt.Errorf("create workspace config directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(store.path), ".workspace-*.tmp")
	if err != nil {
		return fmt.Errorf("create workspace config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return fmt.Errorf("commit workspace config: %w", err)
	}
	return nil
}

// ValidateWorkspaceRoot accepts only an empty dedicated directory or an
// existing WorkCairn workspace. It never searches or mutates another Vault.
func ValidateWorkspaceRoot(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("workspace directory is required")
	}
	root, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil {
		return "", errors.New("workspace directory is invalid")
	}
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", errors.New("workspace directory must already exist")
	}
	home, _ := os.UserHomeDir()
	for _, unsafeRoot := range []string{string(filepath.Separator), filepath.Clean(home), filepath.Join(filepath.Clean(home), "Library", "Mobile Documents", "com~apple~CloudDocs")} {
		if unsafeRoot != "." && root == unsafeRoot {
			return "", errors.New("select a dedicated WorkCairn directory, not a broad root")
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", errors.New("workspace directory cannot be inspected")
	}
	managedEntries := 0
	for _, entry := range entries {
		if entry.Name() != ".DS_Store" && entry.Name() != ".localized" {
			managedEntries++
		}
	}
	if managedEntries == 0 {
		return root, nil
	}
	state, err := os.Stat(filepath.Join(root, "会社", "Workspace State.md"))
	if err != nil || !state.Mode().IsRegular() {
		return "", errors.New("non-empty directory is not an existing WorkCairn workspace")
	}
	return root, nil
}

type WorkspaceSelector interface {
	Select(ctx context.Context) (string, error)
}

type ClaudeCredentialStore interface {
	Load(ctx context.Context) (string, error)
	RequestAndStore(ctx context.Context) (string, error)
}

const maxHeadlessCredentialBytes = 64 << 10

// HeadlessClaudeCredentialStore is a read-only unattended source. Its path is
// fixed below the OS user config root, outside both the repository and Vault;
// WorkCairn never writes or migrates a real credential into it.
type HeadlessClaudeCredentialStore struct {
	path string
}

func NewHeadlessClaudeCredentialStore(configRoot string) (*HeadlessClaudeCredentialStore, error) {
	if strings.TrimSpace(configRoot) == "" {
		return nil, errors.New("config root is required")
	}
	root, err := filepath.Abs(strings.TrimSpace(configRoot))
	if err != nil {
		return nil, errors.New("config root is invalid")
	}
	return &HeadlessClaudeCredentialStore{path: filepath.Join(filepath.Clean(root), "WorkCairn", "credentials", "anthropic-api-key")}, nil
}

func (store *HeadlessClaudeCredentialStore) Load(ctx context.Context) (string, error) {
	if ctx == nil || ctx.Err() != nil {
		return "", &CredentialError{Substage: CredentialHeadlessRead, Classification: CredentialUnavailable}
	}
	before, err := os.Lstat(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return "", &CredentialError{Substage: CredentialHeadlessRead, Classification: CredentialFileNotFound}
	}
	if err != nil {
		return "", &CredentialError{Substage: CredentialHeadlessRead, Classification: CredentialUnavailable}
	}
	if !safeHeadlessCredentialFile(before) {
		return "", &CredentialError{Substage: CredentialHeadlessRead, Classification: CredentialFileUnsafe}
	}
	file, err := os.Open(store.path)
	if err != nil {
		return "", &CredentialError{Substage: CredentialHeadlessRead, Classification: CredentialFilePermission}
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !safeHeadlessCredentialFile(after) || !os.SameFile(before, after) {
		return "", &CredentialError{Substage: CredentialHeadlessRead, Classification: CredentialFileUnsafe}
	}
	content, err := io.ReadAll(io.LimitReader(file, maxHeadlessCredentialBytes+1))
	if err != nil {
		return "", &CredentialError{Substage: CredentialHeadlessRead, Classification: CredentialUnavailable}
	}
	defer zeroCredentialBytes(content)
	if len(content) > maxHeadlessCredentialBytes {
		return "", &CredentialError{Substage: CredentialHeadlessRead, Classification: CredentialFileOutputInvalid}
	}
	credential := strings.TrimSpace(string(content))
	if credential == "" {
		return "", &CredentialError{Substage: CredentialHeadlessRead, Classification: CredentialFileOutputInvalid}
	}
	return credential, nil
}

func (*HeadlessClaudeCredentialStore) RequestAndStore(context.Context) (string, error) {
	return "", ErrUnsupported
}

func safeHeadlessCredentialFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm() == 0o600 && credentialFileOwnedByCurrentUser(info)
}

func zeroCredentialBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

type WorkspaceViewer interface {
	Reveal(ctx context.Context, root string) error
}

type BrowserOpener interface {
	OpenURL(ctx context.Context, url string) error
}
