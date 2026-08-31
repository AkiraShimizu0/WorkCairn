// Package releaseinspector is a release-engineering-only, non-shipped
// internal contract for macOS Developer ID signing/notarization tooling
// (ADR-0071). It never starts an external process, touches the
// filesystem, or makes a network call -- every operation is pure
// parsing and validation over data supplied by its caller. It is not
// part of WorkCairn's public product surface or JSON Contract v1.
package releaseinspector

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ContractVersion identifies this package's own internal request/result
// envelope. It is deliberately distinct from the product's JSON
// Contract v1 (workcairn-core), since this is release tooling, not a
// public product contract.
const ContractVersion = "release-inspector.v1"

// Request is the closed envelope this package accepts. Payload is
// operation-specific and decoded strictly (unknown fields rejected) by
// each operation's own handler.
type Request struct {
	Version   string          `json:"version"`
	Operation string          `json:"operation"`
	Payload   json.RawMessage `json:"payload"`
}

// Response is the closed envelope this package returns. Result is
// operation-specific and always a sanitized, typed value -- never raw
// certificate content, raw tool output, a credential, a real name, or
// an absolute filesystem path.
type Response struct {
	Version string         `json:"version"`
	OK      bool           `json:"ok"`
	Result  any            `json:"result,omitempty"`
	Error   *ResponseError `json:"error,omitempty"`
}

// ResponseError is a closed, machine-readable failure. Message is
// always one of a fixed set of sanitized strings -- never the
// underlying error text, which could otherwise echo caller-supplied
// content.
type ResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Closed error vocabulary for Slice 1's operation set.
const (
	ErrorCodeInvalidRequest      = "INVALID_REQUEST"
	ErrorCodeUnsupportedVersion  = "UNSUPPORTED_VERSION"
	ErrorCodeUnknownOperation    = "UNKNOWN_OPERATION"
	ErrorCodeInternalError       = "INTERNAL_ERROR"
	ErrorCodeCertSHA1Invalid     = "CERT_SHA1_INVALID"
	ErrorCodeCertBundleMalformed = "CERT_BUNDLE_MALFORMED"
	ErrorCodeCertZeroMatches     = "CERT_ZERO_MATCHES"
	ErrorCodeCertMultipleMatches = "CERT_MULTIPLE_MATCHES"
	ErrorCodeJSONMalformed       = "JSON_MALFORMED"
)

func success(result any) Response {
	return Response{Version: ContractVersion, OK: true, Result: result}
}

func failure(code, message string) Response {
	return Response{Version: ContractVersion, OK: false, Error: &ResponseError{Code: code, Message: message}}
}

// decodeStrict decodes data into target, rejecting unknown fields and
// any trailing JSON data -- the same discipline workcairn-core's own
// JSON Contract v1 entrypoint already uses.
func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}
