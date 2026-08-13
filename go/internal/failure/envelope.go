// Package failure defines the Provider- and Domain-neutral typed failure
// contract every durable Process boundary uses to describe a terminal
// Command failure exactly once. An Envelope is constructed only at the
// first boundary that determines a failure (e.g. Review execution, Task
// execution) and is forwarded unchanged through every later boundary
// (outer Command, Command Ledger, HTTP, UI) — later layers never
// reclassify or reconstruct it from a child's Result shape.
package failure

import (
	"errors"
	"fmt"
	"strings"
)

const SchemaVersion = 1

var ErrInvalidEnvelope = errors.New("invalid failure Envelope")

// Envelope is the shared representation of a terminal Command failure. It
// never carries raw Provider response text, raw error messages,
// credentials, secrets, or user-authored content — every field is either a
// closed/sanitized classification or a plain boolean/identifier.
type Envelope struct {
	SchemaVersion int    `json:"schema_version"`
	Code          string `json:"code"`
	Stage         string `json:"stage"`
	// Substage narrows Stage further (e.g. Stage "review_provider",
	// Substage "structured_output"). Optional.
	Substage string `json:"substage,omitempty"`
	// Category is the sanitized failure category (e.g. a Provider
	// FailureCategory string, or a Domain-defined class of structural
	// failure). Optional — not every failure has a meaningful category
	// beyond its Code/Stage.
	Category string `json:"category,omitempty"`
	// Partial is true when some canonical evidence committed before the
	// failure (see Evidence) — mirrors commandledger.StatePartialFailure.
	Partial bool `json:"partial"`
	// RecoveryRequired is relayed verbatim from whatever boolean the
	// owning Command's own finish logic already computed. This package
	// and every downstream layer only ever copy it; nothing infers it.
	RecoveryRequired bool `json:"recovery_required"`
	// ChildCommandID identifies the durable child Command this Envelope
	// originated from, when the caller is an outer orchestration Command
	// forwarding a child's Envelope. Empty when the Envelope's owner is
	// itself the origin.
	ChildCommandID string `json:"child_command_id,omitempty"`
	// Provider is set only when the failure is a redacted Provider
	// diagnostic (no raw response body, no credential, no message text).
	Provider *ProviderDiagnostic `json:"provider,omitempty"`
	// Parse is set only when the failure is a sanitized structural parse
	// classification from a Domain package's own ParseFailureReason-style
	// enum.
	Parse *ParseDiagnostic `json:"parse,omitempty"`
	// Evidence records only what the owning Command can prove committed
	// from a signal it already has. Never a guess.
	Evidence *CommittedEvidence `json:"evidence,omitempty"`
}

// ProviderDiagnostic is the redacted Provider failure detail already
// established by adapter/claude's error classification: no raw response
// body, no credential, no message text.
type ProviderDiagnostic struct {
	Category     string `json:"category"`
	HTTPStatus   int    `json:"http_status,omitempty"`
	ProviderType string `json:"provider_type,omitempty"`
	RequestID    string `json:"request_id,omitempty"`
}

// ParseDiagnostic is a sanitized structural-parse failure classification —
// a Domain-defined reason enum value as plain text, never raw Provider
// output.
type ParseDiagnostic struct {
	// Domain names the owning contract (e.g. "review", "ceo_plan_intent")
	// so the same Reason string from different Domains is never confused.
	Domain string `json:"domain"`
	Reason string `json:"reason"`
}

// CommittedEvidence records only what is certainly committed. Every field
// defaults false; a caller that never learned an answer must leave the
// corresponding field false rather than guess "not committed" from
// silence, and must never guess "committed" either — every field here is
// set only from a signal the caller already has (e.g. Deliverable != nil,
// Artifact.CanonicalCommitted).
type CommittedEvidence struct {
	Deliverable     bool `json:"deliverable,omitempty"`
	TaskState       bool `json:"task_state,omitempty"`
	ReviewCanonical bool `json:"review_canonical,omitempty"`
	Revision        bool `json:"revision,omitempty"`
	InteractionTurn bool `json:"interaction_turn,omitempty"`
}

func canonicalText(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n")
}

// Validate checks structural shape only — it cannot detect whether a
// caller mistakenly put raw content into a field; that is enforced by
// review at every construction site plus targeted tests.
func (envelope Envelope) Validate() error {
	if envelope.SchemaVersion != SchemaVersion || !canonicalText(envelope.Code) || !canonicalText(envelope.Stage) {
		return ErrInvalidEnvelope
	}
	if envelope.Substage != "" && !canonicalText(envelope.Substage) {
		return ErrInvalidEnvelope
	}
	if envelope.Category != "" && !canonicalText(envelope.Category) {
		return ErrInvalidEnvelope
	}
	if envelope.ChildCommandID != "" && !canonicalText(envelope.ChildCommandID) {
		return ErrInvalidEnvelope
	}
	if envelope.Provider != nil && !canonicalText(envelope.Provider.Category) {
		return ErrInvalidEnvelope
	}
	if envelope.Parse != nil && (!canonicalText(envelope.Parse.Domain) || !canonicalText(envelope.Parse.Reason)) {
		return ErrInvalidEnvelope
	}
	return nil
}

// New builds a validated Envelope from its required fields, defaulting
// SchemaVersion. Callers set the optional fields (Substage, Category,
// Partial, RecoveryRequired, ChildCommandID, Provider, Parse, Evidence)
// directly on the returned value before use.
func New(code, stage string) Envelope {
	return Envelope{SchemaVersion: SchemaVersion, Code: strings.TrimSpace(code), Stage: strings.TrimSpace(stage)}
}

// Error carries an Envelope alongside the original internal cause so
// errors.Is/errors.As keep working for callers checking specific
// sentinel/typed errors. Error() is built only from Envelope fields —
// never from Cause — so it can never leak secret/raw content regardless of
// what Cause turns out to be.
type Error struct {
	Envelope Envelope
	Cause    error
}

func (failureErr *Error) Error() string {
	if failureErr.Envelope.Substage != "" {
		return fmt.Sprintf("%s failed at %s/%s", failureErr.Envelope.Code, failureErr.Envelope.Stage, failureErr.Envelope.Substage)
	}
	return fmt.Sprintf("%s failed at %s", failureErr.Envelope.Code, failureErr.Envelope.Stage)
}

func (failureErr *Error) Unwrap() error { return failureErr.Cause }

// ClassifyProviderCategory maps a Provider FailureCategory value (passed as
// a plain string so this package stays Provider-neutral) to the shared
// PROVIDER_* code taxonomy every migrated Command uses. Unlike the
// per-Command classifiers it replaces, its default is Command-neutral —
// it never assumes the caller is Interaction Plan or any other specific
// Command.
func ClassifyProviderCategory(category string) string {
	switch category {
	case "authentication_required":
		return "PROVIDER_AUTHENTICATION_REQUIRED"
	case "billing_required":
		return "PROVIDER_BILLING_REQUIRED"
	case "permission_denied":
		return "PROVIDER_PERMISSION_DENIED"
	case "invalid_provider_request":
		return "PROVIDER_REQUEST_INVALID"
	case "rate_limited":
		return "PROVIDER_RATE_LIMITED"
	case "provider_unavailable", "provider_transport":
		return "PROVIDER_UNAVAILABLE"
	case "invalid_provider_response", "structured_output_invalid":
		return "PROVIDER_RESPONSE_INVALID"
	case "provider_refusal":
		return "PROVIDER_REFUSED"
	default:
		return "PROVIDER_FAILURE"
	}
}
