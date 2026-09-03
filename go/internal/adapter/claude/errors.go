package claude

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
)

var (
	ErrInvalidConfig   = errors.New("invalid Claude Runner configuration")
	ErrInvalidRequest  = errors.New("invalid Claude Runner request")
	ErrTransport       = errors.New("Claude transport failed")
	ErrProvider        = errors.New("Claude provider rejected the request")
	ErrInvalidResponse = errors.New("invalid Claude provider response")
)

type FailureCategory string

// TransportFailureCategory is a closed, sanitized classification derived
// only from Go's typed network error chain. It never contains an endpoint,
// raw error text, request data, or credentials.
type TransportFailureCategory string

const (
	FailureAuthentication FailureCategory = "authentication_required"
	FailureBilling        FailureCategory = "billing_required"
	FailurePermission     FailureCategory = "permission_denied"
	FailureInvalidRequest FailureCategory = "invalid_provider_request"
	FailureRateLimited    FailureCategory = "rate_limited"
	FailureUnavailable    FailureCategory = "provider_unavailable"
	FailureTransport      FailureCategory = "provider_transport"
	FailureResponse       FailureCategory = "invalid_provider_response"
	FailureUnknown        FailureCategory = "provider_failure"
	// FailureRefusal classifies a safety-classifier decline (HTTP 200,
	// stop_reason "refusal"). It is distinct from FailureResponse because it
	// reflects a Provider policy decision, not a malformed response.
	FailureRefusal FailureCategory = "provider_refusal"
	// FailureStructuredOutputInvalid classifies a response that did not
	// honor the Structured Output contract this Runner requested (e.g. the
	// declared ContentField was missing from the returned JSON envelope).
	FailureStructuredOutputInvalid FailureCategory = "structured_output_invalid"
)

// StructuredOutputInvalidReason is a closed, content-blind classification of
// why a Structured Output response failed extraction. It describes only the
// shape of the problem -- never a block's text, a byte count, or any
// fragment of Provider content -- so it is always safe to record as
// diagnostic evidence (Ledger, FailureEnvelope, logs). It is set only when
// Category is FailureStructuredOutputInvalid and the failure came from
// messageResponse.structuredJSON() (never from the separate ContentField
// unwrap step, which has its own established, unclassified error text).
type StructuredOutputInvalidReason string

const (
	// StructuredOutputUnexpectedBlock means the response contained a
	// content block type other than "text", "thinking", or
	// "redacted_thinking" -- a shape this Adapter's Structured Output
	// contract does not expect.
	StructuredOutputUnexpectedBlock StructuredOutputInvalidReason = "unexpected_content_block"
	// StructuredOutputBlockCountInvalid means the response contained zero,
	// or more than one, "text" content block instead of exactly one.
	StructuredOutputBlockCountInvalid StructuredOutputInvalidReason = "text_block_count_invalid"
	// StructuredOutputEmptyText means the single "text" content block was
	// present but empty (or whitespace-only) after trimming.
	StructuredOutputEmptyText StructuredOutputInvalidReason = "empty_text"
	// StructuredOutputInvalidJSON means the text block's content could not
	// be decoded as a JSON value at all.
	StructuredOutputInvalidJSON StructuredOutputInvalidReason = "invalid_json"
	// StructuredOutputMultipleJSON means the text block decoded as one
	// complete JSON value, but a second complete JSON value followed it.
	StructuredOutputMultipleJSON StructuredOutputInvalidReason = "multiple_json_documents"
	// StructuredOutputTrailingJSON means the text block decoded as one
	// complete JSON value, but non-JSON content followed it.
	StructuredOutputTrailingJSON StructuredOutputInvalidReason = "trailing_json_content"
)

const (
	TransportDNSFailed       TransportFailureCategory = "provider_dns_failed"
	TransportConnectFailed   TransportFailureCategory = "provider_connect_failed"
	TransportTLSFailed       TransportFailureCategory = "provider_tls_failed"
	TransportTimeout         TransportFailureCategory = "provider_timeout"
	TransportConnectionReset TransportFailureCategory = "provider_connection_reset"
)

// Error retains machine-readable Adapter failure details without exposing the
// Provider response body or credential-bearing request data in its text.
type Error struct {
	Kind         error
	StatusCode   int
	RequestID    string
	ProviderType string
	Category     FailureCategory
	Transport    TransportFailureCategory
	// structuredOutputReason is set only when Category is
	// FailureStructuredOutputInvalid and the failure came from
	// messageResponse.structuredJSON(). Zero value otherwise. Private so
	// every reader (in-package or not) goes through the StructuredOutputReason()
	// accessor's own closed-allow-list validation rather than trusting a
	// raw field value that could otherwise be set to an arbitrary string.
	structuredOutputReason StructuredOutputInvalidReason
	Err                    error
}

// StructuredOutputReason returns the closed StructuredOutputInvalidReason
// this Error carries, degrading to the empty string unless BOTH: Category
// is FailureStructuredOutputInvalid, and the private reason is one of the
// six-value allow-list -- including the zero value, a category mismatch,
// and defensively, any future value this package's own construction does
// not yet produce. A StructuredOutputInvalidReason is only ever meaningful
// for that one Category, so this accessor's own contract stays closed
// without depending on any caller to separately check Category first.
// Callers (in this package and elsewhere) must use this accessor instead of
// reading a raw field, so a forged or unknown reason -- or a valid reason
// left over on an Error whose Category was later reassigned -- can never
// reach a caller as if it were a validated classification.
func (failure *Error) StructuredOutputReason() StructuredOutputInvalidReason {
	if failure == nil || failure.Category != FailureStructuredOutputInvalid {
		return ""
	}
	switch failure.structuredOutputReason {
	case StructuredOutputUnexpectedBlock, StructuredOutputBlockCountInvalid, StructuredOutputEmptyText,
		StructuredOutputInvalidJSON, StructuredOutputMultipleJSON, StructuredOutputTrailingJSON:
		return failure.structuredOutputReason
	default:
		return ""
	}
}

func (failure *Error) Error() string {
	if failure == nil {
		return "Claude Runner failed"
	}
	if failure.StatusCode != 0 {
		return fmt.Sprintf("Claude Runner failed: %v (status %d)", failure.Kind, failure.StatusCode)
	}
	return fmt.Sprintf("Claude Runner failed: %v", failure.Kind)
}

func (failure *Error) Unwrap() []error {
	if failure == nil {
		return nil
	}
	if failure.Err == nil {
		return []error{failure.Kind}
	}
	return []error{failure.Kind, failure.Err}
}

// classifyTransportFailure deliberately leaves unrecognized network errors
// unclassified. Callers retain the broad provider_transport category rather
// than guessing from a raw error string.
func classifyTransportFailure(err error) TransportFailureCategory {
	if err == nil {
		return ""
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return TransportDNSFailed
	}
	var certificateError *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameError x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	if errors.As(err, &certificateError) || errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostnameError) || errors.As(err, &invalidCertificate) {
		return TransportTLSFailed
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return TransportConnectionReset
	}
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return TransportTimeout
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return TransportTimeout
	}
	var operationError *net.OpError
	if errors.As(err, &operationError) && strings.EqualFold(operationError.Op, "dial") {
		return TransportConnectFailed
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return TransportConnectFailed
	}
	return ""
}
