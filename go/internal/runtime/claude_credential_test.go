package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestExplicitClaudeCredentialSourcesNeverInspectAnotherSource(t *testing.T) {
	tests := []struct {
		name   string
		source ClaudeCredentialSource
		want   string
	}{
		{name: "environment", source: ClaudeCredentialEnvironment, want: "environment-secret"},
		{name: "keychain", source: ClaudeCredentialKeychain, want: "keychain-secret"},
		{name: "headless local", source: ClaudeCredentialHeadlessLocal, want: "headless-secret"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := map[ClaudeCredentialSource]int{}
			readers := CredentialReaders{
				Environment: func() string { calls[ClaudeCredentialEnvironment]++; return "environment-secret" },
				Keychain: func(context.Context) (string, error) {
					calls[ClaudeCredentialKeychain]++
					return "keychain-secret", nil
				},
				HeadlessLocal: func(context.Context) (string, error) {
					calls[ClaudeCredentialHeadlessLocal]++
					return "headless-secret", nil
				},
			}
			credential, err := ResolveClaudeCredential(context.Background(), test.source, readers)
			if err != nil || credential != test.want {
				t.Fatalf("ResolveClaudeCredential = %q, %v", credential, err)
			}
			for _, source := range []ClaudeCredentialSource{ClaudeCredentialEnvironment, ClaudeCredentialKeychain, ClaudeCredentialHeadlessLocal} {
				count := calls[source]
				want := 0
				if source == test.source {
					want = 1
				}
				if count != want {
					t.Fatalf("%s calls = %d, want %d", source, count, want)
				}
			}
		})
	}
}

func TestAutomaticClaudeCredentialSourceHasDocumentedPrecedenceWithoutHeadlessFallback(t *testing.T) {
	keychainCalls, headlessCalls := 0, 0
	credential, err := ResolveClaudeCredential(context.Background(), ClaudeCredentialAutomatic, CredentialReaders{
		Environment:   func() string { return "environment-secret" },
		Keychain:      func(context.Context) (string, error) { keychainCalls++; return "keychain-secret", nil },
		HeadlessLocal: func(context.Context) (string, error) { headlessCalls++; return "headless-secret", nil },
	})
	if err != nil || credential != "environment-secret" || keychainCalls != 0 || headlessCalls != 0 {
		t.Fatalf("environment precedence = %q, %v keychain=%d headless=%d", credential, err, keychainCalls, headlessCalls)
	}
	credential, err = ResolveClaudeCredential(context.Background(), ClaudeCredentialAutomatic, CredentialReaders{
		Environment:   func() string { return "" },
		Keychain:      func(context.Context) (string, error) { keychainCalls++; return "keychain-secret", nil },
		HeadlessLocal: func(context.Context) (string, error) { headlessCalls++; return "headless-secret", nil },
	})
	if err != nil || credential != "keychain-secret" || keychainCalls != 1 || headlessCalls != 0 {
		t.Fatalf("Keychain fallback = %q, %v keychain=%d headless=%d", credential, err, keychainCalls, headlessCalls)
	}
}

func TestConfiguredClaudeCredentialSourceFailsClosedWithoutRetryOrSecretExposure(t *testing.T) {
	const secret = "secret-must-not-escape"
	for _, source := range []ClaudeCredentialSource{ClaudeCredentialEnvironment, ClaudeCredentialKeychain, ClaudeCredentialHeadlessLocal} {
		t.Run(string(source), func(t *testing.T) {
			calls := 0
			readers := CredentialReaders{
				Environment:   func() string { calls++; return "" },
				Keychain:      func(context.Context) (string, error) { calls++; return "", errors.New(secret) },
				HeadlessLocal: func(context.Context) (string, error) { calls++; return "", errors.New(secret) },
			}
			credential, err := ResolveClaudeCredential(context.Background(), source, readers)
			var resolutionErr *CredentialResolutionError
			if credential != "" || !errors.As(err, &resolutionErr) || calls != 1 {
				t.Fatalf("resolution = %q, %#v calls=%d", credential, err, calls)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("credential loader error escaped through resolution error")
			}
		})
	}
}

// fakeAdapterCredentialError is a minimal double for the structural shape a
// Local OS Adapter credential error already implements (see
// localos.CredentialError's CredentialSubstage()/CredentialClassification()
// methods). This package never imports internal/adapter/localos -- Go's
// implicit interface satisfaction is exactly what lets readCredential
// recognize this fake via errors.As, the same way it would recognize the
// real Adapter type, without any import.
type fakeAdapterCredentialError struct {
	substage, classification, message string
}

func (fakeErr *fakeAdapterCredentialError) Error() string { return fakeErr.message }

func (fakeErr *fakeAdapterCredentialError) CredentialSubstage() string { return fakeErr.substage }

func (fakeErr *fakeAdapterCredentialError) CredentialClassification() string {
	return fakeErr.classification
}

// TestReadCredentialClassifiesOnlyExactValidStartupTuples is PB-3m.2's
// correction of PB-3m's P1-1: substage and category are validated together,
// bound to the caller's own source, not as two independent allow-lists.
// Every valid (source, substage, category) startup tuple is recognized with
// an exact-match rendered Error() text; every other combination -- an
// unrecognized substage or category, a source/substage mismatch, a known
// substage paired with the other source's category vocabulary (including
// headless-local reporting keychain_unavailable, which this Checkpoint
// deliberately leaves unrecognized rather than special-casing), a setup-only
// substage (keychain_write, keychain_read_after_write, credential_input --
// these belong to the First-run/Settings connect flow, never startup
// resolution), a plain non-diagnostic error, or a partial/empty diagnostic --
// degrades to the exact-match generic Error() text with both accessors
// returning "". In every case exactly one reader call happens (the explicit
// source, never a sibling, and never Environment), and the fake Adapter
// error's own message text never appears in the resulting error.
func TestReadCredentialClassifiesOnlyExactValidStartupTuples(t *testing.T) {
	const secretMarker = "FAKE-ADAPTER-RAW-MESSAGE-MUST-NOT-ESCAPE-9f2c"

	run := func(t *testing.T, source ClaudeCredentialSource, adapterErr error, wantErrorText string) *CredentialResolutionError {
		t.Helper()
		keychainCalls, headlessCalls, environmentCalls := 0, 0, 0
		readers := CredentialReaders{
			Environment: func() string { environmentCalls++; return "" },
			Keychain: func(context.Context) (string, error) {
				keychainCalls++
				if source == ClaudeCredentialKeychain {
					return "", adapterErr
				}
				return "must-not-be-called", nil
			},
			HeadlessLocal: func(context.Context) (string, error) {
				headlessCalls++
				if source == ClaudeCredentialHeadlessLocal {
					return "", adapterErr
				}
				return "must-not-be-called", nil
			},
		}
		credential, err := ResolveClaudeCredential(context.Background(), source, readers)
		var resolutionErr *CredentialResolutionError
		if credential != "" || !errors.As(err, &resolutionErr) {
			t.Fatalf("resolution = %q, %v", credential, err)
		}
		if keychainCalls+headlessCalls != 1 || environmentCalls != 0 {
			t.Fatalf("call counts wrong (no single-call guarantee or unwanted fallback): keychain=%d headless=%d environment=%d", keychainCalls, headlessCalls, environmentCalls)
		}
		if resolutionErr.Classification != CredentialSourceUnavailable {
			t.Fatalf("Classification = %q, want %q", resolutionErr.Classification, CredentialSourceUnavailable)
		}
		if err.Error() != wantErrorText {
			t.Fatalf("Error() = %q, want exact %q", err.Error(), wantErrorText)
		}
		if strings.Contains(err.Error(), secretMarker) {
			t.Fatal("raw underlying Adapter error message escaped into CredentialResolutionError")
		}
		return resolutionErr
	}

	t.Run("valid", func(t *testing.T) {
		valid := []struct {
			name               string
			source             ClaudeCredentialSource
			substage, category string
		}{
			{name: "keychain not found", source: ClaudeCredentialKeychain, substage: "keychain_read", category: "keychain_not_found"},
			{name: "keychain permission denied", source: ClaudeCredentialKeychain, substage: "keychain_read", category: "keychain_permission_denied"},
			{name: "keychain command failed", source: ClaudeCredentialKeychain, substage: "keychain_read", category: "keychain_command_failed"},
			{name: "keychain output invalid", source: ClaudeCredentialKeychain, substage: "keychain_read", category: "keychain_output_invalid"},
			{name: "keychain setup timeout", source: ClaudeCredentialKeychain, substage: "keychain_read", category: "keychain_setup_timeout"},
			{name: "keychain unavailable", source: ClaudeCredentialKeychain, substage: "keychain_read", category: "keychain_unavailable"},
			{name: "headless-local file not found", source: ClaudeCredentialHeadlessLocal, substage: "headless_local_read", category: "credential_file_not_found"},
			{name: "headless-local file permission denied", source: ClaudeCredentialHeadlessLocal, substage: "headless_local_read", category: "credential_file_permission_denied"},
			{name: "headless-local file unsafe", source: ClaudeCredentialHeadlessLocal, substage: "headless_local_read", category: "credential_file_unsafe"},
			{name: "headless-local file output invalid", source: ClaudeCredentialHeadlessLocal, substage: "headless_local_read", category: "credential_file_output_invalid"},
		}
		for _, testCase := range valid {
			t.Run(testCase.name, func(t *testing.T) {
				adapterErr := &fakeAdapterCredentialError{substage: testCase.substage, classification: testCase.category, message: secretMarker}
				wantErrorText := fmt.Sprintf("Claude credential resolution failed for %s: credential_source_unavailable (%s: %s)", testCase.source, testCase.substage, testCase.category)
				resolutionErr := run(t, testCase.source, adapterErr, wantErrorText)
				if resolutionErr.SafeCredentialSubstage() != testCase.substage || resolutionErr.SafeCredentialCategory() != testCase.category {
					t.Fatalf("accessors = %q/%q, want %q/%q", resolutionErr.SafeCredentialSubstage(), resolutionErr.SafeCredentialCategory(), testCase.substage, testCase.category)
				}
			})
		}
	})

	t.Run("invalid", func(t *testing.T) {
		invalid := []struct {
			name       string
			source     ClaudeCredentialSource
			adapterErr error
		}{
			{name: "unknown substage", source: ClaudeCredentialKeychain,
				adapterErr: &fakeAdapterCredentialError{substage: "unknown_substage_xyz", classification: "keychain_permission_denied", message: secretMarker}},
			{name: "unknown category", source: ClaudeCredentialKeychain,
				adapterErr: &fakeAdapterCredentialError{substage: "keychain_read", classification: "unknown_category_xyz", message: secretMarker}},
			{name: "keychain substage with file category", source: ClaudeCredentialKeychain,
				adapterErr: &fakeAdapterCredentialError{substage: "keychain_read", classification: "credential_file_permission_denied", message: secretMarker}},
			{name: "headless substage with keychain category", source: ClaudeCredentialHeadlessLocal,
				adapterErr: &fakeAdapterCredentialError{substage: "headless_local_read", classification: "keychain_permission_denied", message: secretMarker}},
			{name: "source keychain reports headless substage", source: ClaudeCredentialKeychain,
				adapterErr: &fakeAdapterCredentialError{substage: "headless_local_read", classification: "credential_file_not_found", message: secretMarker}},
			{name: "source headless-local reports keychain substage", source: ClaudeCredentialHeadlessLocal,
				adapterErr: &fakeAdapterCredentialError{substage: "keychain_read", classification: "keychain_not_found", message: secretMarker}},
			{name: "setup substage keychain_write", source: ClaudeCredentialKeychain,
				adapterErr: &fakeAdapterCredentialError{substage: "keychain_write", classification: "credential_file_unsafe", message: secretMarker}},
			{name: "setup substage keychain_read_after_write", source: ClaudeCredentialKeychain,
				adapterErr: &fakeAdapterCredentialError{substage: "keychain_read_after_write", classification: "keychain_permission_denied", message: secretMarker}},
			{name: "setup substage credential_input", source: ClaudeCredentialKeychain,
				adapterErr: &fakeAdapterCredentialError{substage: "credential_input", classification: "keychain_not_found", message: secretMarker}},
			{name: "headless-local reports keychain_unavailable", source: ClaudeCredentialHeadlessLocal,
				adapterErr: &fakeAdapterCredentialError{substage: "headless_local_read", classification: "keychain_unavailable", message: secretMarker}},
			{name: "plain non-diagnostic error", source: ClaudeCredentialKeychain,
				adapterErr: errors.New(secretMarker)},
			{name: "empty substage and category", source: ClaudeCredentialKeychain,
				adapterErr: &fakeAdapterCredentialError{substage: "", classification: "", message: secretMarker}},
			{name: "empty category only", source: ClaudeCredentialKeychain,
				adapterErr: &fakeAdapterCredentialError{substage: "keychain_read", classification: "", message: secretMarker}},
		}
		for _, testCase := range invalid {
			t.Run(testCase.name, func(t *testing.T) {
				wantErrorText := fmt.Sprintf("Claude credential resolution failed for %s: credential_source_unavailable", testCase.source)
				resolutionErr := run(t, testCase.source, testCase.adapterErr, wantErrorText)
				if resolutionErr.SafeCredentialSubstage() != "" || resolutionErr.SafeCredentialCategory() != "" {
					t.Fatalf("accessors = %q/%q, want both empty", resolutionErr.SafeCredentialSubstage(), resolutionErr.SafeCredentialCategory())
				}
			})
		}
	})
}

// TestReadCredentialRecognizesWrappedAdapterDiagnostic locks that errors.As
// still finds the Adapter diagnostic shape through an fmt.Errorf %w wrapper,
// not only when the reader returns the Adapter error directly.
func TestReadCredentialRecognizesWrappedAdapterDiagnostic(t *testing.T) {
	adapterErr := &fakeAdapterCredentialError{substage: "keychain_read", classification: "keychain_permission_denied", message: "fake-wrapped-detail"}
	wrapped := fmt.Errorf("keychain read failed: %w", adapterErr)
	credential, err := ResolveClaudeCredential(context.Background(), ClaudeCredentialKeychain, CredentialReaders{
		Keychain: func(context.Context) (string, error) { return "", wrapped },
	})
	var resolutionErr *CredentialResolutionError
	if credential != "" || !errors.As(err, &resolutionErr) {
		t.Fatalf("resolution = %q, %v", credential, err)
	}
	if resolutionErr.SafeCredentialSubstage() != "keychain_read" || resolutionErr.SafeCredentialCategory() != "keychain_permission_denied" {
		t.Fatalf("accessors = %q/%q, want keychain_read/keychain_permission_denied", resolutionErr.SafeCredentialSubstage(), resolutionErr.SafeCredentialCategory())
	}
}

// TestAutomaticSourceKeychainFallbackDiagnosticClassifiesAsKeychainSource
// locks this Checkpoint's point 5: when automatic finds no environment
// credential and falls through to Keychain, the resulting diagnostic must
// classify under source keychain (matching the actual reader consulted),
// not automatic -- unchanged existing behavior, verified directly.
func TestAutomaticSourceKeychainFallbackDiagnosticClassifiesAsKeychainSource(t *testing.T) {
	adapterErr := &fakeAdapterCredentialError{substage: "keychain_read", classification: "keychain_permission_denied", message: "fake"}
	_, err := ResolveClaudeCredential(context.Background(), ClaudeCredentialAutomatic, CredentialReaders{
		Environment: func() string { return "" },
		Keychain:    func(context.Context) (string, error) { return "", adapterErr },
	})
	var resolutionErr *CredentialResolutionError
	if !errors.As(err, &resolutionErr) || resolutionErr.Source != ClaudeCredentialKeychain ||
		resolutionErr.SafeCredentialSubstage() != "keychain_read" || resolutionErr.SafeCredentialCategory() != "keychain_permission_denied" {
		t.Fatalf("resolutionErr = %#v, want Source=keychain with keychain_read/keychain_permission_denied", resolutionErr)
	}
}

func TestParseClaudeCredentialSourceRejectsUnknownValues(t *testing.T) {
	if source, err := ParseClaudeCredentialSource(""); err != nil || source != ClaudeCredentialAutomatic {
		t.Fatalf("empty source = %q, %v", source, err)
	}
	if _, err := ParseClaudeCredentialSource("local-maybe"); err == nil {
		t.Fatal("unknown credential source was accepted")
	}
}
