//go:build darwin

package localos

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type recordingRunner struct {
	outputs        []string
	errors         []error
	secretErrors   []error
	calls          []runnerCall
	expectedSecret string
	secretMatched  bool
}

type runnerCall struct {
	name         string
	args         []string
	stdin        string
	secretPrompt bool
}

func (runner *recordingRunner) Run(_ context.Context, name string, args []string, stdin string) (string, error) {
	runner.calls = append(runner.calls, runnerCall{name: name, args: append([]string(nil), args...), stdin: stdin})
	if len(runner.outputs) == 0 {
		return "", errors.New("unexpected command")
	}
	output := runner.outputs[0]
	runner.outputs = runner.outputs[1:]
	var err error
	if len(runner.errors) != 0 {
		err = runner.errors[0]
		runner.errors = runner.errors[1:]
	}
	return output, err
}

func (runner *recordingRunner) RunSecretPrompt(_ context.Context, name string, args []string, secret string) error {
	runner.calls = append(runner.calls, runnerCall{name: name, args: append([]string(nil), args...), secretPrompt: true})
	runner.secretMatched = secret == runner.expectedSecret
	if len(runner.secretErrors) == 0 {
		return nil
	}
	err := runner.secretErrors[0]
	runner.secretErrors = runner.secretErrors[1:]
	return err
}

func TestDarwinCredentialStoreKeepsSecretOutOfArguments(t *testing.T) {
	runner := &recordingRunner{outputs: []string{"fake-secret\n", "stored-secret\n"}, expectedSecret: "fake-secret"}
	store := &DarwinClaudeCredentialStore{runner: runner}
	credential, err := store.RequestAndStore(context.Background())
	if err != nil || credential != "stored-secret" {
		t.Fatalf("RequestAndStore = %q, %v", credential, err)
	}
	if len(runner.calls) != 3 || runner.calls[0].name != "/usr/bin/osascript" || runner.calls[1].name != "/usr/bin/security" || runner.calls[2].name != "/usr/bin/security" {
		t.Fatalf("calls = %#v", runner.calls)
	}
	if strings.Contains(strings.Join(runner.calls[1].args, " "), "fake-secret") || runner.calls[1].stdin != "" || !runner.calls[1].secretPrompt || !runner.secretMatched {
		t.Fatalf("credential entered argv/stdin instead of the secret prompt: %#v", runner.calls[1])
	}
	for _, call := range runner.calls[1:] {
		joined := strings.Join(call.args, " ")
		if !strings.Contains(joined, "-a "+claudeKeychainAccount) || !strings.Contains(joined, "-s "+claudeKeychainService) {
			t.Fatalf("service/account mismatch: %#v", call)
		}
	}
}

func TestDarwinCredentialStoreUpdatesExistingItemAndReadsBackEachWrite(t *testing.T) {
	runner := &recordingRunner{outputs: []string{"first\n", "stored-first\n", "second\n", "stored-second\n"}}
	store := &DarwinClaudeCredentialStore{runner: runner}
	if got, err := store.RequestAndStore(context.Background()); err != nil || got != "stored-first" {
		t.Fatalf("first write = %q, %v", got, err)
	}
	if got, err := store.RequestAndStore(context.Background()); err != nil || got != "stored-second" {
		t.Fatalf("update = %q, %v", got, err)
	}
	secretCalls := 0
	readCalls := 0
	for _, call := range runner.calls {
		if call.secretPrompt {
			secretCalls++
			if !containsSequence(call.args, "add-generic-password", "-U", "-a", claudeKeychainAccount, "-s", claudeKeychainService, "-w") {
				t.Fatalf("unsafe update arguments = %#v", call.args)
			}
		} else if call.name == "/usr/bin/security" {
			readCalls++
		}
	}
	if secretCalls != 2 || readCalls != 2 {
		t.Fatalf("writes=%d reads=%d calls=%#v", secretCalls, readCalls, runner.calls)
	}
}

func TestDarwinCredentialStoreClassifiesMissingPermissionAndInvalidOutput(t *testing.T) {
	tests := []struct {
		name           string
		store          *DarwinClaudeCredentialStore
		operation      func(*DarwinClaudeCredentialStore) error
		substage       string
		classification CredentialFailure
	}{
		{
			name:      "missing item",
			store:     &DarwinClaudeCredentialStore{runner: &recordingRunner{outputs: []string{""}, errors: []error{&commandExecutionError{exitCode: 44, diagnostic: "The specified item could not be found in the keychain."}}}},
			operation: func(store *DarwinClaudeCredentialStore) error { _, err := store.Load(context.Background()); return err },
			substage:  CredentialRead, classification: CredentialNotFound,
		},
		{
			name:  "permission denied write",
			store: &DarwinClaudeCredentialStore{runner: &recordingRunner{outputs: []string{"secret\n"}, secretErrors: []error{&commandExecutionError{exitCode: 1, diagnostic: "User interaction is not allowed."}}}},
			operation: func(store *DarwinClaudeCredentialStore) error {
				_, err := store.RequestAndStore(context.Background())
				return err
			},
			substage: CredentialWrite, classification: CredentialPermissionDenied,
		},
		{
			name:  "empty read after write",
			store: &DarwinClaudeCredentialStore{runner: &recordingRunner{outputs: []string{"secret\n", "\n"}}},
			operation: func(store *DarwinClaudeCredentialStore) error {
				_, err := store.RequestAndStore(context.Background())
				return err
			},
			substage: CredentialReadAfterWrite, classification: CredentialOutputInvalid,
		},
		{
			name:  "unclassified command failure",
			store: &DarwinClaudeCredentialStore{runner: &recordingRunner{outputs: []string{"secret\n"}, secretErrors: []error{&commandExecutionError{exitCode: 2, diagnostic: "usage"}}}},
			operation: func(store *DarwinClaudeCredentialStore) error {
				_, err := store.RequestAndStore(context.Background())
				return err
			},
			substage: CredentialWrite, classification: CredentialCommandFailed,
		},
		{
			name:  "keychain setup timeout",
			store: &DarwinClaudeCredentialStore{runner: &recordingRunner{outputs: []string{"secret\n"}, secretErrors: []error{&commandExecutionError{timedOut: true}}}},
			operation: func(store *DarwinClaudeCredentialStore) error {
				_, err := store.RequestAndStore(context.Background())
				return err
			},
			substage: CredentialWrite, classification: CredentialSetupTimeout,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.operation(test.store)
			var credentialErr *CredentialError
			if !errors.As(err, &credentialErr) || credentialErr.Substage != test.substage || credentialErr.Classification != test.classification {
				t.Fatalf("error = %#v, want %s/%s", err, test.substage, test.classification)
			}
		})
	}
}

func TestCommandFailureSanitizesSecretAndNeverReturnsDiagnostic(t *testing.T) {
	secret := "secret-must-not-escape"
	err := newCommandExecutionError(errors.New("failed"), "prefix "+secret+" permission denied", secret)
	var commandErr *commandExecutionError
	if !errors.As(err, &commandErr) || strings.Contains(commandErr.diagnostic, secret) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("secret or raw diagnostic escaped: %q %#v", err.Error(), commandErr)
	}
}

func TestExecRunnerUsesPseudoTerminalWithoutSecretArgument(t *testing.T) {
	t.Setenv("WORKCAIRN_KEYCHAIN_PTY_HELPER", "success")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	secret := "pty-secret-never-in-argv"
	args := []string{"-test.run=^TestKeychainPTYHelperProcess$"}
	if strings.Contains(strings.Join(args, " "), secret) {
		t.Fatal("secret entered helper arguments")
	}
	if err := (execRunner{}).RunSecretPrompt(context.Background(), executable, args, secret); err != nil {
		t.Fatalf("pseudo-terminal prompt failed: %v", err)
	}
}

func TestExecRunnerWaitsForPromptAndTerminatesOnTimeout(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("prompt synchronization", func(t *testing.T) {
		t.Setenv("WORKCAIRN_KEYCHAIN_PTY_HELPER", "delayed-prompt")
		if err := (execRunner{}).RunSecretPrompt(context.Background(), executable, []string{"-test.run=^TestKeychainPTYHelperProcess$"}, "full-fake-credential"); err != nil {
			t.Fatalf("delayed prompt = %v", err)
		}
	})
	t.Run("bounded timeout", func(t *testing.T) {
		t.Setenv("WORKCAIRN_KEYCHAIN_PTY_HELPER", "no-prompt")
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := (execRunner{}).RunSecretPrompt(ctx, executable, []string{"-test.run=^TestKeychainPTYHelperProcess$"}, "full-fake-credential")
		var commandErr *commandExecutionError
		if !errors.As(err, &commandErr) || !commandErr.timedOut {
			t.Fatalf("timeout = %#v", err)
		}
	})
}

func TestExecRunnerSanitizesEchoedSecretOnFailure(t *testing.T) {
	t.Setenv("WORKCAIRN_KEYCHAIN_PTY_HELPER", "fail")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	secret := "echoed-secret-must-be-redacted"
	err = (execRunner{}).RunSecretPrompt(context.Background(), executable, []string{"-test.run=^TestKeychainPTYHelperProcess$"}, secret)
	var commandErr *commandExecutionError
	if !errors.As(err, &commandErr) || strings.Contains(commandErr.diagnostic, secret) || strings.Contains(err.Error(), secret) {
		t.Fatalf("PTY diagnostic exposed secret: %v %#v", err, commandErr)
	}
}

func TestKeychainPTYHelperProcess(t *testing.T) {
	mode := os.Getenv("WORKCAIRN_KEYCHAIN_PTY_HELPER")
	if mode == "" {
		return
	}
	if mode == "no-prompt" {
		time.Sleep(10 * time.Second)
		os.Exit(3)
	}
	if mode == "delayed-prompt" {
		if err := syscall.SetNonblock(int(os.Stdin.Fd()), true); err != nil {
			os.Exit(4)
		}
		time.Sleep(75 * time.Millisecond)
		early := make([]byte, 4096)
		if count, _ := os.Stdin.Read(early); count != 0 {
			os.Exit(5)
		}
		if err := syscall.SetNonblock(int(os.Stdin.Fd()), false); err != nil {
			os.Exit(6)
		}
	}
	fmt.Fprint(os.Stdout, "Password: ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) == "" {
		os.Exit(2)
	}
	if mode == "fail" {
		fmt.Fprintln(os.Stdout, scanner.Text())
		os.Exit(2)
	}
	os.Exit(0)
}

func containsSequence(values []string, expected ...string) bool {
	if len(values) != len(expected) {
		return false
	}
	for index := range expected {
		if values[index] != expected[index] {
			return false
		}
	}
	return true
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
	if strings.Count(source, ".runner.Run(") != 5 || strings.Count(source, ".runner.RunSecretPrompt(") != 1 {
		t.Fatalf("review OS command call allow-list: Run=%d secret=%d", strings.Count(source, ".runner.Run("), strings.Count(source, ".runner.RunSecretPrompt("))
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
