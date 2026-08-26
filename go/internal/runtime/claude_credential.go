package runtime

import (
	"context"
	"fmt"
	"strings"
)

// ClaudeCredentialSource is the closed Runtime-edge choice for resolving the
// Anthropic credential. Provider adapters receive only the resolved value and
// never observe this source.
type ClaudeCredentialSource string

const (
	ClaudeCredentialAutomatic     ClaudeCredentialSource = "automatic"
	ClaudeCredentialEnvironment   ClaudeCredentialSource = "environment"
	ClaudeCredentialKeychain      ClaudeCredentialSource = "keychain"
	ClaudeCredentialHeadlessLocal ClaudeCredentialSource = "headless-local"
)

type CredentialResolutionClassification string

const (
	CredentialSourceInvalid     CredentialResolutionClassification = "credential_source_invalid"
	CredentialSourceMissing     CredentialResolutionClassification = "credential_source_missing"
	CredentialSourceUnavailable CredentialResolutionClassification = "credential_source_unavailable"
	CredentialSourceReadOnly    CredentialResolutionClassification = "credential_source_read_only"
)

// CredentialResolutionError is deliberately secret-free. It retains only the
// selected source and a closed classification; the underlying loader error is
// never exposed through logs, HTTP, Ledger, or command output.
type CredentialResolutionError struct {
	Source         ClaudeCredentialSource
	Classification CredentialResolutionClassification
}

func (credentialErr *CredentialResolutionError) Error() string {
	return fmt.Sprintf("Claude credential resolution failed for %s: %s", credentialErr.Source, credentialErr.Classification)
}

type CredentialReaders struct {
	Environment   func() string
	Keychain      func(context.Context) (string, error)
	HeadlessLocal func(context.Context) (string, error)
}

// ParseClaudeCredentialSource treats an omitted value as the documented
// backward-compatible precedence. Explicit sources never fall through to a
// different source.
func ParseClaudeCredentialSource(value string) (ClaudeCredentialSource, error) {
	source := ClaudeCredentialSource(strings.TrimSpace(value))
	if source == "" {
		source = ClaudeCredentialAutomatic
	}
	switch source {
	case ClaudeCredentialAutomatic, ClaudeCredentialEnvironment, ClaudeCredentialKeychain, ClaudeCredentialHeadlessLocal:
		return source, nil
	default:
		return "", &CredentialResolutionError{Source: source, Classification: CredentialSourceInvalid}
	}
}

// ResolveClaudeCredential performs exactly one read from the selected source.
// Automatic is the documented compatibility mode: environment first, then
// Keychain. It never includes headless-local. Explicit modes do not inspect any
// other reader, so an unavailable configured source fails closed.
func ResolveClaudeCredential(ctx context.Context, source ClaudeCredentialSource, readers CredentialReaders) (string, error) {
	if ctx == nil {
		return "", &CredentialResolutionError{Source: source, Classification: CredentialSourceUnavailable}
	}
	if err := ctx.Err(); err != nil {
		return "", &CredentialResolutionError{Source: source, Classification: CredentialSourceUnavailable}
	}
	switch source {
	case ClaudeCredentialAutomatic:
		if readers.Environment != nil {
			if credential := strings.TrimSpace(readers.Environment()); credential != "" {
				return credential, nil
			}
		}
		return readCredential(ctx, ClaudeCredentialKeychain, readers.Keychain)
	case ClaudeCredentialEnvironment:
		if readers.Environment == nil {
			return "", &CredentialResolutionError{Source: source, Classification: CredentialSourceUnavailable}
		}
		credential := strings.TrimSpace(readers.Environment())
		if credential == "" {
			return "", &CredentialResolutionError{Source: source, Classification: CredentialSourceMissing}
		}
		return credential, nil
	case ClaudeCredentialKeychain:
		return readCredential(ctx, source, readers.Keychain)
	case ClaudeCredentialHeadlessLocal:
		return readCredential(ctx, source, readers.HeadlessLocal)
	default:
		return "", &CredentialResolutionError{Source: source, Classification: CredentialSourceInvalid}
	}
}

func readCredential(ctx context.Context, source ClaudeCredentialSource, reader func(context.Context) (string, error)) (string, error) {
	if reader == nil {
		return "", &CredentialResolutionError{Source: source, Classification: CredentialSourceUnavailable}
	}
	credential, err := reader(ctx)
	if err != nil {
		return "", &CredentialResolutionError{Source: source, Classification: CredentialSourceUnavailable}
	}
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return "", &CredentialResolutionError{Source: source, Classification: CredentialSourceMissing}
	}
	return credential, nil
}
