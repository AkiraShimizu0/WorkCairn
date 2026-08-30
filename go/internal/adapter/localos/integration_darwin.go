//go:build darwin

package localos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	urlpkg "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	claudeKeychainService  = "com.workcairn.provider.anthropic"
	claudeKeychainAccount  = "api-key"
	nativeInputTimeout     = 2 * time.Minute
	keychainCommandTimeout = 15 * time.Second
)

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args []string, stdin string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return "", newCommandExecutionError(err, stderr.String(), "")
	}
	return string(output), nil
}

type commandExecutionError struct {
	exitCode   int
	diagnostic string
	timedOut   bool
}

func (commandErr *commandExecutionError) Error() string { return "local OS command failed" }

func newCommandExecutionError(err error, diagnostic, secret string) error {
	if secret != "" {
		diagnostic = strings.ReplaceAll(diagnostic, secret, "<redacted>")
	}
	// A bounded diagnostic is retained only in-process for closed
	// classification below. Error() never returns it.
	if len(diagnostic) > 4096 {
		diagnostic = diagnostic[:4096]
	}
	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	} else if status, ok := err.(interface{ ExitStatus() int }); ok {
		exitCode = status.ExitStatus()
	}
	return &commandExecutionError{exitCode: exitCode, diagnostic: diagnostic, timedOut: errors.Is(err, context.DeadlineExceeded)}
}

type DarwinWorkspaceSelector struct {
	runner CommandRunner
	// homeDir overrides os.UserHomeDir for tests, so the start-folder
	// resolution below can be exercised against a temporary directory
	// instead of the real home directory. nil means production default.
	homeDir func() (string, error)
	// documentsUsable overrides the check deciding whether the Documents
	// start-folder fallback in Select() is safe to use. nil means the
	// production check (dirIsUsableStartFolder). Tests can inject a fixed
	// predicate to deterministically exercise the symlink / missing /
	// access-failure branches without real filesystem permission changes.
	documentsUsable func(path string) bool
}

func NewWorkspaceSelector() WorkspaceSelector {
	return &DarwinWorkspaceSelector{runner: execRunner{}}
}

func (selector *DarwinWorkspaceSelector) Select(ctx context.Context) (string, error) {
	homeDir := selector.homeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	// A normal local location is the default start position. ~/Documents is
	// used when it is a real, accessible directory; a symlinked, missing,
	// or inaccessible Documents falls back to home rather than resolving
	// through the symlink. iCloud Drive is never the start position and is
	// not recommended -- it remains a fully optional choice the picker
	// still allows.
	documentsUsable := selector.documentsUsable
	if documentsUsable == nil {
		documentsUsable = dirIsUsableStartFolder
	}
	start := home
	if documents := filepath.Join(home, "Documents"); documentsUsable(documents) {
		start = documents
	}
	script := `set startFolder to POSIX file ` + appleScriptString(start) + ` as alias
try
  set chosenFolder to choose folder with prompt "WorkCairn専用の新しい空のデータフォルダを、Mac上の通常の保存場所に作成または選択してください。iCloud Driveは任意です。" default location startFolder
  return POSIX path of chosenFolder
on error number -128
  return ""
end try`
	output, err := selector.runner.Run(ctx, "/usr/bin/osascript", []string{"-e", script}, "")
	if err != nil {
		return "", fmt.Errorf("select workspace directory: %w", err)
	}
	selected := strings.TrimSpace(output)
	if selected == "" {
		return "", ErrCanceled
	}
	return ValidateWorkspaceRoot(selected)
}

type DarwinClaudeCredentialStore struct {
	runner          CommandRunner
	keychain        credentialKeychain
	inputTimeout    time.Duration
	keychainTimeout time.Duration
}

func NewClaudeCredentialStore() ClaudeCredentialStore {
	return &DarwinClaudeCredentialStore{runner: execRunner{}, keychain: newProcessCredentialKeychain()}
}

func (store *DarwinClaudeCredentialStore) Load(ctx context.Context) (string, error) {
	return store.load(ctx, CredentialRead)
}

func (store *DarwinClaudeCredentialStore) load(ctx context.Context, substage string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, store.keychainCommandDuration())
	defer cancel()
	output, err := store.keychain.Read(commandCtx)
	if err != nil {
		return "", classifyCredentialError(substage, err)
	}
	defer zeroBytes(output)
	credential := strings.TrimSpace(string(output))
	if credential == "" {
		return "", &CredentialError{Substage: substage, Classification: CredentialOutputInvalid}
	}
	return credential, nil
}

func (store *DarwinClaudeCredentialStore) RequestAndStore(ctx context.Context) (string, error) {
	script := `try
  set response to display dialog "ClaudeのAPI keyを入力してください。値はmacOS Keychainへ保存され、browserやVaultには保存されません。" default answer "" with hidden answer buttons {"キャンセル", "接続"} default button "接続" with title "WorkCairn — AI Connection"
  return text returned of response
on error number -128
  return ""
end try`
	inputCtx, cancelInput := context.WithTimeout(ctx, store.inputDuration())
	output, err := store.runner.Run(inputCtx, "/usr/bin/osascript", []string{"-e", script}, "")
	cancelInput()
	if err != nil {
		return "", classifyCredentialError(CredentialInput, err)
	}
	credential := strings.TrimSpace(output)
	if credential == "" {
		return "", ErrCanceled
	}
	writeCtx, cancelWrite := context.WithTimeout(ctx, store.keychainCommandDuration())
	err = store.keychain.Upsert(writeCtx, []byte(credential))
	cancelWrite()
	if err != nil {
		return "", classifyCredentialError(CredentialWrite, err)
	}
	// A zero exit status alone is not a commit point. Read the exact same
	// service/account immediately and return only the non-empty stored value.
	// No value comparison, logging, fingerprinting, or persistence occurs.
	return store.load(ctx, CredentialReadAfterWrite)
}

func (store *DarwinClaudeCredentialStore) inputDuration() time.Duration {
	if store.inputTimeout > 0 {
		return store.inputTimeout
	}
	return nativeInputTimeout
}

func (store *DarwinClaudeCredentialStore) keychainCommandDuration() time.Duration {
	if store.keychainTimeout > 0 {
		return store.keychainTimeout
	}
	return keychainCommandTimeout
}

func classifyCredentialError(substage string, err error) error {
	classification := CredentialCommandFailed
	var commandErr *commandExecutionError
	var keychainErr *keychainOperationError
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &commandErr) && commandErr.timedOut {
		classification = CredentialSetupTimeout
	} else if errors.As(err, &keychainErr) {
		classification = keychainErr.classification
	} else if errors.As(err, &commandErr) {
		diagnostic := strings.ToLower(commandErr.diagnostic)
		switch {
		case strings.Contains(diagnostic, "could not be found"), strings.Contains(diagnostic, "item not found"), strings.Contains(diagnostic, "errsecitemnotfound"):
			classification = CredentialNotFound
		case strings.Contains(diagnostic, "user interaction is not allowed"), strings.Contains(diagnostic, "interaction not allowed"),
			strings.Contains(diagnostic, "permission denied"), strings.Contains(diagnostic, "not permitted"),
			strings.Contains(diagnostic, "authorization was denied"), strings.Contains(diagnostic, "errsecauthfailed"):
			classification = CredentialPermissionDenied
		}
	}
	return &CredentialError{Substage: substage, Classification: classification}
}

type DarwinWorkspaceViewer struct {
	runner CommandRunner
}

func NewWorkspaceViewer() WorkspaceViewer {
	return &DarwinWorkspaceViewer{runner: execRunner{}}
}

func NewBrowserOpener() BrowserOpener {
	return &DarwinWorkspaceViewer{runner: execRunner{}}
}

func (viewer *DarwinWorkspaceViewer) Reveal(ctx context.Context, root string) error {
	root, err := ValidateWorkspaceRoot(root)
	if err != nil {
		return err
	}
	_, err = viewer.runner.Run(ctx, "/usr/bin/open", []string{"-R", root}, "")
	if err != nil {
		return fmt.Errorf("reveal workspace directory: %w", err)
	}
	return nil
}

func (viewer *DarwinWorkspaceViewer) OpenURL(ctx context.Context, url string) error {
	parsed, err := urlpkgParse(url)
	if err != nil || parsed.Scheme != "http" || parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("only a local WorkCairn URL can be opened")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())) {
		return errors.New("only a local WorkCairn URL can be opened")
	}
	_, err = viewer.runner.Run(ctx, "/usr/bin/open", []string{url}, "")
	if err != nil {
		return fmt.Errorf("open WorkCairn UI: %w", err)
	}
	return nil
}

var urlpkgParse = urlpkg.Parse

// dirIsUsableStartFolder reports whether path is a real (non-symlink)
// directory that can actually be opened as a picker start location. Lstat
// classifies the entry without dereferencing a symlink, so a symlinked
// Documents folder is rejected rather than resolved through to whatever it
// points at; Open/Close confirms the directory itself is accessible without
// listing or modifying its contents.
func dirIsUsableStartFolder(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	handle, err := os.Open(path)
	if err != nil {
		return false
	}
	_ = handle.Close()
	return true
}

func appleScriptString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}
