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
	Err          error
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
