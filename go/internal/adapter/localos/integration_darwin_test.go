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
	errors  []error
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
	var err error
	if len(runner.errors) != 0 {
		err = runner.errors[0]
		runner.errors = runner.errors[1:]
	}
	return output, err
}

type recordingKeychain struct {
	stored      []byte
	readValues  [][]byte
	readErrors  []error
	writeErrors []error
	writes      [][]byte
}

func (keychain *recordingKeychain) Read(_ context.Context) ([]byte, error) {
	if len(keychain.readErrors) != 0 {
		err := keychain.readErrors[0]
		keychain.readErrors = keychain.readErrors[1:]
		if err != nil {
			return nil, err
		}
	}
	if len(keychain.readValues) != 0 {
		value := append([]byte(nil), keychain.readValues[0]...)
		keychain.readValues = keychain.readValues[1:]
		return value, nil
	}
	return append([]byte(nil), keychain.stored...), nil
}

func (keychain *recordingKeychain) Upsert(_ context.Context, credential []byte) error {
	keychain.writes = append(keychain.writes, append([]byte(nil), credential...))
	if len(keychain.writeErrors) != 0 {
		err := keychain.writeErrors[0]
		keychain.writeErrors = keychain.writeErrors[1:]
		if err != nil {
			return err
		}
	}
	keychain.stored = append(keychain.stored[:0], credential...)
	return nil
}

func TestDarwinCredentialStoreKeepsSecretOutOfCommandArguments(t *testing.T) {
	const secret = "fake-secret-never-in-argv"
	runner := &recordingRunner{outputs: []string{secret + "\n"}}
	keychain := &recordingKeychain{}
	store := &DarwinClaudeCredentialStore{runner: runner, keychain: keychain}
	credential, err := store.RequestAndStore(context.Background())
	if err != nil || credential != secret {
		t.Fatalf("RequestAndStore = %q, %v", credential, err)
	}
	if len(runner.calls) != 1 || runner.calls[0].name != "/usr/bin/osascript" {
		t.Fatalf("OS command calls = %#v", runner.calls)
	}
	if strings.Contains(strings.Join(runner.calls[0].args, " "), secret) || runner.calls[0].stdin != "" {
		t.Fatalf("credential entered argv/stdin: %#v", runner.calls[0])
	}
	if len(keychain.writes) != 1 || string(keychain.writes[0]) != secret {
		t.Fatalf("native Keychain did not receive the complete credential")
	}
}

func TestDarwinCredentialStoreInitialWriteUpdateAndReadBack(t *testing.T) {
	runner := &recordingRunner{outputs: []string{"first\n", "second\n"}}
	keychain := &recordingKeychain{}
	store := &DarwinClaudeCredentialStore{runner: runner, keychain: keychain}
	if got, err := store.RequestAndStore(context.Background()); err != nil || got != "first" {
		t.Fatalf("initial write = %q, %v", got, err)
	}
	if got, err := store.RequestAndStore(context.Background()); err != nil || got != "second" {
		t.Fatalf("update = %q, %v", got, err)
	}
	if len(keychain.writes) != 2 || string(keychain.writes[0]) != "first" || string(keychain.writes[1]) != "second" {
		t.Fatalf("writes = %q", keychain.writes)
	}
}

func TestDarwinCredentialStoreClassifiesKeychainFailures(t *testing.T) {
	tests := []struct {
		name           string
		store          *DarwinClaudeCredentialStore
		operation      func(*DarwinClaudeCredentialStore) error
		substage       string
		classification CredentialFailure
	}{
		{
			name: "missing item",
			store: &DarwinClaudeCredentialStore{keychain: &recordingKeychain{
				readErrors: []error{classifyKeychainStatus(errSecItemNotFound)},
			}},
			operation: func(store *DarwinClaudeCredentialStore) error { _, err := store.Load(context.Background()); return err },
			substage:  CredentialRead, classification: CredentialNotFound,
		},
		{
			name: "permission denied write",
			store: &DarwinClaudeCredentialStore{
				runner:   &recordingRunner{outputs: []string{"secret\n"}},
				keychain: &recordingKeychain{writeErrors: []error{classifyKeychainStatus(errSecAuthFailed)}},
			},
			operation: func(store *DarwinClaudeCredentialStore) error {
				_, err := store.RequestAndStore(context.Background())
				return err
			},
			substage: CredentialWrite, classification: CredentialPermissionDenied,
		},
		{
			name: "unavailable Keychain",
			store: &DarwinClaudeCredentialStore{keychain: &recordingKeychain{
				readErrors: []error{classifyKeychainStatus(errSecNotAvailable)},
			}},
			operation: func(store *DarwinClaudeCredentialStore) error { _, err := store.Load(context.Background()); return err },
			substage:  CredentialRead, classification: CredentialUnavailable,
		},
		{
			name: "empty read after write",
			store: &DarwinClaudeCredentialStore{
				runner:   &recordingRunner{outputs: []string{"secret\n"}},
				keychain: &recordingKeychain{readValues: [][]byte{[]byte("\n")}},
			},
			operation: func(store *DarwinClaudeCredentialStore) error {
				_, err := store.RequestAndStore(context.Background())
				return err
			},
			substage: CredentialReadAfterWrite, classification: CredentialOutputInvalid,
		},
		{
			name: "keychain timeout",
			store: &DarwinClaudeCredentialStore{
				runner:   &recordingRunner{outputs: []string{"secret\n"}},
				keychain: &recordingKeychain{writeErrors: []error{context.DeadlineExceeded}},
			},
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

func TestDarwinCredentialLoadReturnsOnlyStoredValue(t *testing.T) {
	keychain := &recordingKeychain{stored: []byte("stored-secret\n")}
	store := &DarwinClaudeCredentialStore{keychain: keychain}
	credential, err := store.Load(context.Background())
	if err != nil || credential != "stored-secret" {
		t.Fatalf("Load = %q, %v", credential, err)
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
	if strings.Count(source, ".runner.Run(") != 4 {
		t.Fatalf("review OS command call allow-list: Run=%d", strings.Count(source, ".runner.Run("))
	}
	for _, tool := range []string{"/usr/bin/osascript", "/usr/bin/open"} {
		if !strings.Contains(source, tool) {
			t.Fatalf("expected fixed OS tool is missing: %s", tool)
		}
	}
	for _, forbidden := range []string{"/usr/bin/security", "/bin/sh", "/bin/bash", "/usr/bin/env", "python", "node"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("forbidden process entered local OS Adapter: %s", forbidden)
		}
	}
}
