//go:build darwin

package localos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingRunner struct {
	outputs []string
	calls   []runnerCall
}

type runnerCall struct {
	name  string
	args  []string
	stdin string
}

func (runner *recordingRunner) Run(_ context.Context, name string, args []string, stdin string) (string, error) {
	runner.calls = append(runner.calls, runnerCall{name: name, args: append([]string(nil), args...), stdin: stdin})
	if len(runner.outputs) == 0 {
		return "", errors.New("unexpected command")
	}
	output := runner.outputs[0]
	runner.outputs = runner.outputs[1:]
	return output, nil
}

func TestDarwinCredentialStoreKeepsSecretOutOfArguments(t *testing.T) {
	runner := &recordingRunner{outputs: []string{"fake-secret\n", ""}}
	store := &DarwinClaudeCredentialStore{runner: runner}
	credential, err := store.RequestAndStore(context.Background())
	if err != nil || credential != "fake-secret" {
		t.Fatalf("RequestAndStore = %q, %v", credential, err)
	}
	if len(runner.calls) != 2 || runner.calls[0].name != "/usr/bin/osascript" || runner.calls[1].name != "/usr/bin/security" {
		t.Fatalf("calls = %#v", runner.calls)
	}
	if strings.Contains(strings.Join(runner.calls[1].args, " "), credential) || runner.calls[1].stdin != credential+"\n" {
		t.Fatalf("credential was not confined to security stdin: %#v", runner.calls[1])
	}
}

func TestDarwinCredentialLoadReturnsOnlyStoredValue(t *testing.T) {
	runner := &recordingRunner{outputs: []string{"stored-secret\n"}}
	store := &DarwinClaudeCredentialStore{runner: runner}
	credential, err := store.Load(context.Background())
	if err != nil || credential != "stored-secret" || len(runner.calls) != 1 {
		t.Fatalf("Load = %q, %v calls=%#v", credential, err, runner.calls)
	}
}

func TestDarwinBrowserOpenerAcceptsOnlyLocalHTTPURL(t *testing.T) {
	runner := &recordingRunner{outputs: []string{""}}
	opener := &DarwinWorkspaceViewer{runner: runner}
	if err := opener.OpenURL(context.Background(), "https://example.com/"); err == nil || len(runner.calls) != 0 {
		t.Fatalf("external URL was opened: err=%v calls=%#v", err, runner.calls)
	}
	if err := opener.OpenURL(context.Background(), "http://127.0.0.1:8787/"); err != nil || len(runner.calls) != 1 || runner.calls[0].name != "/usr/bin/open" {
		t.Fatalf("local URL = %v calls=%#v", err, runner.calls)
	}
}

func TestDarwinIntegrationUsesOnlyFixedAbsoluteSystemTools(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("integration_darwin.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(content)
	if strings.Count(source, ".Run(ctx,") != 6 {
		t.Fatalf("review OS command call allow-list: found %d", strings.Count(source, ".Run(ctx,"))
	}
	for _, tool := range []string{"/usr/bin/osascript", "/usr/bin/security", "/usr/bin/open"} {
		if !strings.Contains(source, tool) {
			t.Fatalf("expected fixed OS tool is missing: %s", tool)
		}
	}
	for _, forbidden := range []string{"/bin/sh", "/bin/bash", "/usr/bin/env", "python", "node"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("interpreter entered local OS Adapter: %s", forbidden)
		}
	}
}
