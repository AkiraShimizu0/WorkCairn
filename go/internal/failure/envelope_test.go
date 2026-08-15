package failure

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestEnvelopeValidateRequiresCodeAndStage(t *testing.T) {
	valid := New("REVIEW_RESULT_INVALID", "review_result_parser")
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid Envelope rejected: %v", err)
	}
	for _, mutate := range []func(*Envelope){
		func(envelope *Envelope) { envelope.SchemaVersion = 0 },
		func(envelope *Envelope) { envelope.Code = "" },
		func(envelope *Envelope) { envelope.Stage = "" },
		func(envelope *Envelope) { envelope.Code = " REVIEW_RESULT_INVALID" },
		func(envelope *Envelope) { envelope.Substage = "\n" },
		func(envelope *Envelope) { envelope.Category = "\r" },
		func(envelope *Envelope) { envelope.ChildCommandID = " " },
		func(envelope *Envelope) {
			envelope.Provider = &ProviderDiagnostic{Category: "provider_transport", Subcategory: "bad\nvalue"}
		},
	} {
		invalid := valid
		mutate(&invalid)
		if invalid.Validate() == nil {
			t.Fatalf("invalid Envelope accepted: %#v", invalid)
		}
	}
}

func TestEnvelopeValidateRejectsUnsafeSubStructures(t *testing.T) {
	base := New("PROVIDER_RATE_LIMITED", "review_provider")
	withBadProvider := base
	withBadProvider.Provider = &ProviderDiagnostic{Category: ""}
	if withBadProvider.Validate() == nil {
		t.Fatal("Provider with empty Category accepted")
	}
	withBadParse := base
	withBadParse.Parse = &ParseDiagnostic{Domain: "review", Reason: ""}
	if withBadParse.Validate() == nil {
		t.Fatal("Parse with empty Reason accepted")
	}
	withBadParseField := base
	withBadParseField.Parse = &ParseDiagnostic{Domain: "ceo_plan_intent", Reason: "missing_required_field", Field: "steps\nrequired_role"}
	if withBadParseField.Validate() == nil {
		t.Fatal("Parse with unsafe Field accepted")
	}
	withBadPresenceKey := base
	withBadPresenceKey.Parse = &ParseDiagnostic{
		Domain: "review", Reason: "missing_required_field", Field: "summary",
		StructuredOutputPresence: map[string]bool{"verdict": true, "summary\n": false},
	}
	if withBadPresenceKey.Validate() == nil {
		t.Fatal("Parse with an unsafe StructuredOutputPresence key accepted")
	}
}

func TestEnvelopeValidateRejectsUnsafeStructuredOutputFieldShape(t *testing.T) {
	envelope := New("REVIEW_RESULT_INVALID", "review_result_parser")
	nonBlank := true
	envelope.Parse = &ParseDiagnostic{
		Domain: "review", Reason: "missing_required_field", Field: "summary",
		StructuredOutputFieldShape: map[string]StructuredOutputFieldShape{
			"summary\n": {Present: true, JSONType: "string", NonBlank: &nonBlank},
		},
	}
	if err := envelope.Validate(); err == nil {
		t.Fatal("Parse with an unsafe StructuredOutputFieldShape key accepted")
	}
}

func TestEnvelopeValidateAcceptsStructuredOutputFieldShape(t *testing.T) {
	envelope := New("REVIEW_RESULT_INVALID", "review_result_parser")
	nonBlank := false
	envelope.Parse = &ParseDiagnostic{
		Domain: "review", Reason: "missing_required_field", Field: "summary",
		StructuredOutputFieldShape: map[string]StructuredOutputFieldShape{
			"summary": {Present: true, JSONType: "string", NonBlank: &nonBlank},
		},
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("Envelope with well-formed StructuredOutputFieldShape rejected: %v", err)
	}
}

// TestEnvelopeValidateAcceptsProposedTaskIndex locks the CMD-B0BFC132
// diagnostic addition: a non-negative ProposedTaskIndex alongside a Field
// scoped to one task (e.g. "proposed_tasks.title") is a well-formed,
// content-blind diagnostic.
func TestEnvelopeValidateAcceptsProposedTaskIndex(t *testing.T) {
	envelope := New("INTERACTION_PLAN_FAILED", "interaction_plan_validation")
	taskIndex := 0
	envelope.Parse = &ParseDiagnostic{
		Domain: "interaction_plan_validation", Reason: "missing_required_field", Field: "proposed_tasks.title",
		ProposedTaskIndex: &taskIndex,
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("Envelope with well-formed ProposedTaskIndex rejected: %v", err)
	}
}

// TestEnvelopeValidateRejectsNegativeProposedTaskIndex confirms a
// negative index -- which could never be a genuine ProposedTasks
// position -- is rejected as structurally unsafe, the same way an unsafe
// StructuredOutputFieldShape key is.
func TestEnvelopeValidateRejectsNegativeProposedTaskIndex(t *testing.T) {
	envelope := New("INTERACTION_PLAN_FAILED", "interaction_plan_validation")
	taskIndex := -1
	envelope.Parse = &ParseDiagnostic{
		Domain: "interaction_plan_validation", Reason: "missing_required_field", Field: "proposed_tasks.title",
		ProposedTaskIndex: &taskIndex,
	}
	if err := envelope.Validate(); err == nil {
		t.Fatal("Parse with a negative ProposedTaskIndex accepted")
	}
}

// TestEnvelopeValidateAcceptsStructuredOutputPresenceValuesRegardlessOfBool
// confirms Validate() never rejects an Envelope on the basis of what a
// presence value actually is -- true and false are both legitimate,
// well-formed diagnostics; only the map's keys are checked for safety.
func TestEnvelopeValidateAcceptsStructuredOutputPresenceValuesRegardlessOfBool(t *testing.T) {
	envelope := New("REVIEW_RESULT_INVALID", "review_result_parser")
	envelope.Parse = &ParseDiagnostic{
		Domain: "review", Reason: "missing_required_field", Field: "summary",
		StructuredOutputPresence: map[string]bool{"verdict": true, "issues": true, "summary": false},
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("Envelope with a well-formed StructuredOutputPresence rejected: %v", err)
	}
}

func TestErrorUnwrapPreservesCause(t *testing.T) {
	cause := errors.New("underlying typed cause")
	wrapped := &Error{Envelope: New("REVIEW_EXECUTION_FAILED", "process"), Cause: cause}
	if !errors.Is(wrapped, cause) {
		t.Fatal("errors.Is did not see through Unwrap() to Cause")
	}
	var target *Error
	if !errors.As(wrapped, &target) || target != wrapped {
		t.Fatal("errors.As did not recover the *Error")
	}
}

// TestErrorNeverLeaksCauseText proves Error() is built only from Envelope
// fields, never from Cause -- planting a secret marker in Cause's text must
// never appear in Error()'s output.
func TestErrorNeverLeaksCauseText(t *testing.T) {
	secret := "PROVIDER_SECRET_MARKER_MUST_NOT_APPEAR_IN_ERROR_TEXT"
	cause := errors.New("raw provider response: " + secret)
	wrapped := &Error{Envelope: New("PROVIDER_RESPONSE_INVALID", "review_provider_response"), Cause: cause}
	if strings.Contains(wrapped.Error(), secret) {
		t.Fatalf("Error() leaked secret from Cause: %q", wrapped.Error())
	}
	withSubstage := &Error{Envelope: New("REVIEW_RESULT_INVALID", "review_result_parser"), Cause: cause}
	withSubstage.Envelope.Substage = "unknown_field"
	if strings.Contains(withSubstage.Error(), secret) {
		t.Fatalf("Error() with Substage leaked secret from Cause: %q", withSubstage.Error())
	}
	if !strings.Contains(withSubstage.Error(), "unknown_field") {
		t.Fatalf("Error() did not include Substage: %q", withSubstage.Error())
	}
}

func TestClassifyProviderCategoryCoversAllCategoriesWithNeutralDefault(t *testing.T) {
	tests := map[string]string{
		"authentication_required":   "PROVIDER_AUTHENTICATION_REQUIRED",
		"billing_required":          "PROVIDER_BILLING_REQUIRED",
		"permission_denied":         "PROVIDER_PERMISSION_DENIED",
		"invalid_provider_request":  "PROVIDER_REQUEST_INVALID",
		"rate_limited":              "PROVIDER_RATE_LIMITED",
		"provider_unavailable":      "PROVIDER_UNAVAILABLE",
		"provider_transport":        "PROVIDER_UNAVAILABLE",
		"invalid_provider_response": "PROVIDER_RESPONSE_INVALID",
		"structured_output_invalid": "PROVIDER_RESPONSE_INVALID",
		"provider_refusal":          "PROVIDER_REFUSED",
		"totally_unknown_category":  "PROVIDER_FAILURE",
	}
	for category, want := range tests {
		if got := ClassifyProviderCategory(category); got != want {
			t.Errorf("ClassifyProviderCategory(%q) = %q, want %q", category, got, want)
		}
	}
}

// TestEnvelopeJSONRoundTripsWithoutOptionalFields confirms an old, minimal
// {code, stage} shape (matching what a pre-migration Ledger Failure would
// look like if it ever carried an Envelope at all) still decodes cleanly,
// and a fully-populated Envelope round-trips byte for byte.
func TestEnvelopeJSONRoundTripsWithoutOptionalFields(t *testing.T) {
	minimal := []byte(`{"schema_version":1,"code":"REVIEW_SAVE_FAILED","stage":"review_artifact_save","partial":false,"recovery_required":false}`)
	var decoded Envelope
	if err := json.Unmarshal(minimal, &decoded); err != nil {
		t.Fatalf("decode minimal Envelope: %v", err)
	}
	if decoded.Provider != nil || decoded.Parse != nil || decoded.Evidence != nil || decoded.Substage != "" || decoded.Category != "" {
		t.Fatalf("minimal Envelope decoded with unexpected optional fields: %#v", decoded)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("minimal Envelope failed Validate(): %v", err)
	}

	full := Envelope{
		SchemaVersion: SchemaVersion, Code: "PROVIDER_RATE_LIMITED", Stage: "review_provider", Substage: "retry_after",
		Category: "rate_limited", Partial: true, RecoveryRequired: true, ChildCommandID: "CHILD-abc123",
		Provider: &ProviderDiagnostic{Category: "rate_limited", HTTPStatus: 429, ProviderType: "rate_limit_error", RequestID: "req_1"},
		Parse: &ParseDiagnostic{
			Domain: "ceo_plan_intent", Reason: "missing_required_field", Field: "steps.required_role",
			StructuredOutputPresence: map[string]bool{"project_name": true, "objective": true, "summary": false},
		},
		Evidence: &CommittedEvidence{ReviewCanonical: true},
	}
	encoded, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("encode full Envelope: %v", err)
	}
	var roundTripped Envelope
	if err := json.Unmarshal(encoded, &roundTripped); err != nil {
		t.Fatalf("decode full Envelope: %v", err)
	}
	if !reflect.DeepEqual(roundTripped, full) {
		t.Fatalf("round trip mismatch: got %#v, want %#v", roundTripped, full)
	}
}

// TestEnvelopeParseDiagnosticDecodesWithoutStructuredOutputPresence proves
// backward compatibility with the immediately-preceding round: a Ledger
// record whose Parse diagnostic carries Domain/Reason/Field but predates
// StructuredOutputPresence (no such key in the stored JSON at all) still
// decodes cleanly with StructuredOutputPresence left nil, and still
// validates.
func TestEnvelopeParseDiagnosticDecodesWithoutStructuredOutputPresence(t *testing.T) {
	legacy := []byte(`{"schema_version":1,"code":"REVIEW_RESULT_INVALID","stage":"review_result_parser","partial":false,"recovery_required":false,` +
		`"parse":{"domain":"review","reason":"missing_required_field","field":"summary"}}`)
	var decoded Envelope
	if err := json.Unmarshal(legacy, &decoded); err != nil {
		t.Fatalf("decode legacy Envelope: %v", err)
	}
	if decoded.Parse == nil || decoded.Parse.Field != "summary" || decoded.Parse.StructuredOutputPresence != nil {
		t.Fatalf("legacy Envelope decoded = %#v", decoded.Parse)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("legacy Envelope failed Validate(): %v", err)
	}
}
