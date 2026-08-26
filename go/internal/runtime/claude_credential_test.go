package runtime

import (
	"context"
	"errors"
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

func TestParseClaudeCredentialSourceRejectsUnknownValues(t *testing.T) {
	if source, err := ParseClaudeCredentialSource(""); err != nil || source != ClaudeCredentialAutomatic {
		t.Fatalf("empty source = %q, %v", source, err)
	}
	if _, err := ParseClaudeCredentialSource("local-maybe"); err == nil {
		t.Fatal("unknown credential source was accepted")
	}
}
