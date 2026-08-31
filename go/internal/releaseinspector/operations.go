package releaseinspector

import (
	"encoding/json"
	"errors"
	"time"
)

// Closed operation set for Slice 1. Every operation here is
// characterization-independent: it makes no Developer ID Application
// type determination and is not wired to any notary, hdiutil,
// find-identity, or codesign schema.
const (
	OperationSelectCertificateBySHA1 = "select_certificate_by_sha1"
	OperationDetectJSONDuplicateKeys = "detect_json_duplicate_keys"
)

type selectCertificatePayload struct {
	ExpectedSHA1 string `json:"expected_sha1"`
	PEMBundle    string `json:"pem_bundle"`
}

type selectCertificateResult struct {
	NotBefore string `json:"not_before"`
	NotAfter  string `json:"not_after"`
	Valid     bool   `json:"valid"`
}

type detectDuplicateKeysPayload struct {
	JSONDocument string `json:"json_document"`
}

// detectDuplicateKeysResult is intentionally limited to a single closed
// boolean. It must never carry any caller-controlled key name, object
// path, or JSON fragment -- those come from the input document itself
// and are not safe to echo back into an external Response.
type detectDuplicateKeysResult struct {
	HasDuplicate bool `json:"has_duplicate"`
}

// Dispatch handles one Request against the closed Slice 1 operation
// set and returns a Response. It never starts an external process,
// touches the filesystem, or makes a network call.
func Dispatch(req Request) Response {
	if req.Version != ContractVersion {
		return failure(ErrorCodeUnsupportedVersion, "contract version is not supported")
	}
	switch req.Operation {
	case OperationSelectCertificateBySHA1:
		return dispatchSelectCertificate(req.Payload)
	case OperationDetectJSONDuplicateKeys:
		return dispatchDetectDuplicateKeys(req.Payload)
	case "":
		return failure(ErrorCodeInvalidRequest, "operation is required")
	default:
		return failure(ErrorCodeUnknownOperation, "operation is not supported")
	}
}

func dispatchSelectCertificate(rawPayload json.RawMessage) Response {
	var payload selectCertificatePayload
	if err := decodeStrict(rawPayload, &payload); err != nil {
		return failure(ErrorCodeInvalidRequest, "payload is invalid")
	}
	match, err := SelectCertificateBySHA1(payload.ExpectedSHA1, []byte(payload.PEMBundle))
	if err != nil {
		code, message := certificateFailure(err)
		return failure(code, message)
	}
	return success(selectCertificateResult{
		NotBefore: match.NotBefore.UTC().Format(time.RFC3339),
		NotAfter:  match.NotAfter.UTC().Format(time.RFC3339),
		Valid:     match.Valid,
	})
}

func certificateFailure(err error) (code, message string) {
	switch {
	case errors.Is(err, ErrCertZeroMatches):
		return ErrorCodeCertZeroMatches, "no certificate matches the expected fingerprint"
	case errors.Is(err, ErrCertMultipleMatches):
		return ErrorCodeCertMultipleMatches, "more than one certificate matches the expected fingerprint"
	case errors.Is(err, ErrCertBundleMalformed):
		return ErrorCodeCertBundleMalformed, "certificate bundle could not be parsed"
	default:
		return ErrorCodeCertSHA1Invalid, "expected fingerprint is not a valid SHA-1 value"
	}
}

func dispatchDetectDuplicateKeys(rawPayload json.RawMessage) Response {
	var payload detectDuplicateKeysPayload
	if err := decodeStrict(rawPayload, &payload); err != nil {
		return failure(ErrorCodeInvalidRequest, "payload is invalid")
	}
	dupErr := DetectDuplicateJSONKeys([]byte(payload.JSONDocument))
	if dupErr == nil {
		return success(detectDuplicateKeysResult{HasDuplicate: false})
	}
	var dup *DuplicateJSONKeyError
	if errors.As(dupErr, &dup) {
		return success(detectDuplicateKeysResult{HasDuplicate: true})
	}
	return failure(ErrorCodeJSONMalformed, "json document could not be parsed")
}
