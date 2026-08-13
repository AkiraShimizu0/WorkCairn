//go:build darwin

package localos

import (
	"context"
	"errors"
	"fmt"
	"net"
	urlpkg "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	claudeKeychainService = "com.workcairn.provider.anthropic"
	claudeKeychainAccount = "api-key"
)

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args []string, stdin string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	output, err := command.Output()
	return string(output), err
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
	output, err := store.runner.Run(ctx, "/usr/bin/security", []string{"find-generic-password", "-a", claudeKeychainAccount, "-s", claudeKeychainService, "-w"}, "")
	if err != nil {
		return "", ErrNotConfigured
	}
	credential := strings.TrimSpace(output)
	if credential == "" {
		return "", ErrNotConfigured
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
	_, err = store.runner.Run(ctx, "/usr/bin/security", []string{"add-generic-password", "-U", "-a", claudeKeychainAccount, "-s", claudeKeychainService, "-w"}, credential+"\n")
	if err != nil {
		return "", fmt.Errorf("store Claude credential: %w", err)
	}
	return credential, nil
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
