//go:build darwin

package localos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	urlpkg "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/creack/pty"
)

const (
	claudeKeychainService = "com.workcairn.provider.anthropic"
	claudeKeychainAccount = "api-key"
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
		return "", newCommandExecutionError(err, stderr.String(), "")
	}
	return string(output), nil
}

// RunSecretPrompt uses a pseudo-terminal because security(1)'s password
// prompt is interactive and does not accept a daemon pipe as a non-interactive
// password source. The secret is written only to the PTY and is never argv,
// stdout, stderr, a returned error, or persistent evidence.
func (execRunner) RunSecretPrompt(ctx context.Context, name string, args []string, secret string) error {
	command := exec.CommandContext(ctx, name, args...)
	terminal, err := pty.Start(command)
	if err != nil {
		return newCommandExecutionError(err, "", secret)
	}
	defer terminal.Close()

	output := make(chan []byte, 1)
	go func() {
		content, _ := io.ReadAll(io.LimitReader(terminal, 16<<10))
		output <- content
	}()
	if _, err := io.WriteString(terminal, secret+"\n"); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return newCommandExecutionError(err, "", secret)
	}
	waitErr := command.Wait()
	transcript := <-output
	if waitErr != nil {
		return newCommandExecutionError(waitErr, string(transcript), secret)
	}
	return nil
}

type commandExecutionError struct {
	exitCode   int
	diagnostic string
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
	return &commandExecutionError{exitCode: exitCode, diagnostic: diagnostic}
}

type DarwinWorkspaceSelector struct {
	runner CommandRunner
}

func NewWorkspaceSelector() WorkspaceSelector {
	return &DarwinWorkspaceSelector{runner: execRunner{}}
}

func (selector *DarwinWorkspaceSelector) Select(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	start := filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs")
	if info, statErr := os.Stat(start); statErr != nil || !info.IsDir() {
		start = home
	}
	script := `set startFolder to POSIX file ` + appleScriptString(start) + ` as alias
try
  set chosenFolder to choose folder with prompt "WorkCairn専用の新しいフォルダを作成または選択してください。iCloud Driveを推奨します。" default location startFolder
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
	runner CommandRunner
}

func NewClaudeCredentialStore() ClaudeCredentialStore {
	return &DarwinClaudeCredentialStore{runner: execRunner{}}
}

func (store *DarwinClaudeCredentialStore) Load(ctx context.Context) (string, error) {
	return store.load(ctx, CredentialRead)
}

func (store *DarwinClaudeCredentialStore) load(ctx context.Context, substage string) (string, error) {
	output, err := store.runner.Run(ctx, "/usr/bin/security", []string{"find-generic-password", "-a", claudeKeychainAccount, "-s", claudeKeychainService, "-w"}, "")
	if err != nil {
		return "", classifyCredentialError(substage, err)
	}
	credential := strings.TrimSpace(output)
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
	output, err := store.runner.Run(ctx, "/usr/bin/osascript", []string{"-e", script}, "")
	if err != nil {
		return "", fmt.Errorf("request Claude credential: %w", err)
	}
	credential := strings.TrimSpace(output)
	if credential == "" {
		return "", ErrCanceled
	}
	if err := store.runner.RunSecretPrompt(ctx, "/usr/bin/security", []string{"add-generic-password", "-U", "-a", claudeKeychainAccount, "-s", claudeKeychainService, "-w"}, credential); err != nil {
		return "", classifyCredentialError(CredentialWrite, err)
	}
	// A zero exit status alone is not a commit point. Read the exact same
	// service/account immediately and return only the non-empty stored value.
	// No value comparison, logging, fingerprinting, or persistence occurs.
	return store.load(ctx, CredentialReadAfterWrite)
}

func classifyCredentialError(substage string, err error) error {
	classification := CredentialCommandFailed
	var commandErr *commandExecutionError
	if errors.As(err, &commandErr) {
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

func appleScriptString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}
