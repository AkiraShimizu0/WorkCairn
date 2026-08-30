package runtime

import (
	"context"
	"errors"
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
//
// diagnostic is additive, unexported detail: a closed, private enum value
// (never a raw string) set only when a Local OS Adapter reader error reports
// an exact valid (source, substage, category) startup tuple recognized by
// classifyStartupCredentialDiagnostic. It stays credentialDiagnosticNone for
// any unrecognized, mismatched, setup-only, or absent diagnostic --
// Classification alone (the existing generic CredentialSourceUnavailable) is
// always still correct on its own. Because it is unexported and can only be
// reached through the read-only SafeCredentialSubstage()/
// SafeCredentialCategory() accessors (which render fixed literals from the
// enum, never Adapter-supplied text), no other package can set an arbitrary
// diagnostic string here. This type intentionally has no Unwrap() -- there
// is no path from a CredentialResolutionError back to the Adapter's raw
// underlying error, OSStatus, command output, path, or credential content.
type CredentialResolutionError struct {
	Source         ClaudeCredentialSource
	Classification CredentialResolutionClassification
	diagnostic     credentialDiagnosticOutcome
}

func (credentialErr *CredentialResolutionError) Error() string {
	base := fmt.Sprintf("Claude credential resolution failed for %s: %s", credentialErr.Source, credentialErr.Classification)
	substage, category := credentialErr.diagnostic.substage(), credentialErr.diagnostic.category()
	if substage == "" || category == "" {
		return base
	}
	return fmt.Sprintf("%s (%s: %s)", base, substage, category)
}

// SafeCredentialSubstage and SafeCredentialCategory are read-only accessors
// for the fixed, closed diagnostic literal pair recognized for this error,
// or "" when none was. Deliberately named apart from the Local OS Adapter's
// own CredentialSubstage()/CredentialClassification() (an unbounded-string
// interface): these two always return one of a small, fixed set of literals
// this package itself owns, never Adapter-supplied text.
func (credentialErr *CredentialResolutionError) SafeCredentialSubstage() string {
	return credentialErr.diagnostic.substage()
}

func (credentialErr *CredentialResolutionError) SafeCredentialCategory() string {
	return credentialErr.diagnostic.category()
}

// credentialDiagnostic is the structural shape a Local OS Adapter credential
// error already implements (e.g. localos.CredentialError's
// CredentialSubstage()/CredentialClassification() methods). Runtime never
// imports internal/adapter/localos -- Go's implicit interface satisfaction
// lets errors.As recognize any error with this method shape, preserving the
// Adapter Pattern (Runtime depends on no concrete Adapter type, only this
// method-shaped contract it owns).
type credentialDiagnostic interface {
	CredentialSubstage() string
	CredentialClassification() string
}

// credentialDiagnosticOutcome is a private, closed enum: each value is one
// exact valid (source, substage, category) startup diagnostic tuple. This is
// the only thing ever stored beyond the Adapter-supplied strings -- never
// those strings themselves, and never the underlying error.
type credentialDiagnosticOutcome int

const (
	credentialDiagnosticNone credentialDiagnosticOutcome = iota
	credentialDiagnosticKeychainNotFound
	credentialDiagnosticKeychainPermissionDenied
	credentialDiagnosticKeychainCommandFailed
	credentialDiagnosticKeychainOutputInvalid
	credentialDiagnosticKeychainSetupTimeout
	credentialDiagnosticKeychainUnavailable
	credentialDiagnosticHeadlessLocalFileNotFound
	credentialDiagnosticHeadlessLocalFilePermissionDenied
	credentialDiagnosticHeadlessLocalFileUnsafe
	credentialDiagnosticHeadlessLocalFileOutputInvalid
)

// substage and category render the fixed, closed literal pair for a known
// outcome -- never anything derived from Adapter-supplied text.
// credentialDiagnosticNone (and any value this switch doesn't recognize)
// renders "", matching "no diagnostic".
func (outcome credentialDiagnosticOutcome) substage() string {
	switch outcome {
	case credentialDiagnosticKeychainNotFound, credentialDiagnosticKeychainPermissionDenied,
		credentialDiagnosticKeychainCommandFailed, credentialDiagnosticKeychainOutputInvalid,
		credentialDiagnosticKeychainSetupTimeout, credentialDiagnosticKeychainUnavailable:
		return "keychain_read"
	case credentialDiagnosticHeadlessLocalFileNotFound, credentialDiagnosticHeadlessLocalFilePermissionDenied,
		credentialDiagnosticHeadlessLocalFileUnsafe, credentialDiagnosticHeadlessLocalFileOutputInvalid:
		return "headless_local_read"
	default:
		return ""
	}
}

func (outcome credentialDiagnosticOutcome) category() string {
	switch outcome {
	case credentialDiagnosticKeychainNotFound:
		return "keychain_not_found"
	case credentialDiagnosticKeychainPermissionDenied:
		return "keychain_permission_denied"
	case credentialDiagnosticKeychainCommandFailed:
		return "keychain_command_failed"
	case credentialDiagnosticKeychainOutputInvalid:
		return "keychain_output_invalid"
	case credentialDiagnosticKeychainSetupTimeout:
		return "keychain_setup_timeout"
	case credentialDiagnosticKeychainUnavailable:
		return "keychain_unavailable"
	case credentialDiagnosticHeadlessLocalFileNotFound:
		return "credential_file_not_found"
	case credentialDiagnosticHeadlessLocalFilePermissionDenied:
		return "credential_file_permission_denied"
	case credentialDiagnosticHeadlessLocalFileUnsafe:
		return "credential_file_unsafe"
	case credentialDiagnosticHeadlessLocalFileOutputInvalid:
		return "credential_file_output_invalid"
	default:
		return ""
	}
}

// classifyStartupCredentialDiagnostic validates the exact (source, substage,
// category) tuple a Local OS Adapter reader error reports during Runtime
// startup resolution (readCredential) and returns the one matching closed
// outcome, or credentialDiagnosticNone for anything that is not a valid
// startup tuple:
//
//   - an unrecognized substage or category;
//   - a source/substage mismatch (e.g. source keychain with substage
//     headless_local_read, or vice versa);
//   - a setup-only substage (keychain_write, keychain_read_after_write,
//     credential_input) -- these belong to the First-run/Settings connect
//     flow (DarwinClaudeCredentialStore.RequestAndStore), never to
//     readCredential's startup resolution;
//   - a known substage paired with a category from the other source's
//     vocabulary (e.g. headless_local_read reporting a keychain_* category,
//     including keychain_unavailable -- a real Adapter classification whose
//     name is source-name-inconsistent for headless-local; this function
//     deliberately does not special-case it, and it degrades to
//     credentialDiagnosticNone like any other mismatched pairing, per this
//     Checkpoint's explicit scope: no Adapter rename or new category here).
func classifyStartupCredentialDiagnostic(source ClaudeCredentialSource, substage, category string) credentialDiagnosticOutcome {
	switch {
	case source == ClaudeCredentialKeychain && substage == "keychain_read":
		switch category {
		case "keychain_not_found":
			return credentialDiagnosticKeychainNotFound
		case "keychain_permission_denied":
			return credentialDiagnosticKeychainPermissionDenied
		case "keychain_command_failed":
			return credentialDiagnosticKeychainCommandFailed
		case "keychain_output_invalid":
			return credentialDiagnosticKeychainOutputInvalid
		case "keychain_setup_timeout":
			return credentialDiagnosticKeychainSetupTimeout
		case "keychain_unavailable":
			return credentialDiagnosticKeychainUnavailable
		}
	case source == ClaudeCredentialHeadlessLocal && substage == "headless_local_read":
		switch category {
		case "credential_file_not_found":
			return credentialDiagnosticHeadlessLocalFileNotFound
		case "credential_file_permission_denied":
			return credentialDiagnosticHeadlessLocalFilePermissionDenied
		case "credential_file_unsafe":
			return credentialDiagnosticHeadlessLocalFileUnsafe
		case "credential_file_output_invalid":
			return credentialDiagnosticHeadlessLocalFileOutputInvalid
		}
	}
	return credentialDiagnosticNone
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
		resolutionErr := &CredentialResolutionError{Source: source, Classification: CredentialSourceUnavailable}
		var diagnostic credentialDiagnostic
		if errors.As(err, &diagnostic) {
			resolutionErr.diagnostic = classifyStartupCredentialDiagnostic(source, diagnostic.CredentialSubstage(), diagnostic.CredentialClassification())
		}
		return "", resolutionErr
	}
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return "", &CredentialResolutionError{Source: source, Classification: CredentialSourceMissing}
	}
	return credential, nil
}
