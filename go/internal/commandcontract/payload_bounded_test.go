package commandcontract

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestInteractionStartAcceptsOmittedAndBoundedExecutionProfile(t *testing.T) {
	requestDigest := "sha256:3c8f6dc8dde25e7cad6814e9ee01b8efabe7451719fafa18c84792eb35aa8bbe"
	base := `{"session_id":"SESSION-001","request":"Webアプリを作りたい","request_digest":"` + requestDigest + `","model":"Claude Sonnet 5","current_time":"2026-08-09T12:00:00Z"`
	standardOmitted := json.RawMessage(base + `}`)
	standardExplicitEmpty := json.RawMessage(base + `,"execution_profile":""}`)
	bounded := json.RawMessage(base + `,"execution_profile":"bounded_acceptance"}`)
	for name, payload := range map[string]json.RawMessage{
		"omitted":        standardOmitted,
		"explicit empty": standardExplicitEmpty,
		"bounded":        bounded,
	} {
		if err := ValidatePayload("interaction.start", payload); err != nil {
			t.Fatalf("%s: ValidatePayload() = %v, want accept", name, err)
		}
	}
}

func TestInteractionStartRejectsUnknownExecutionProfile(t *testing.T) {
	requestDigest := "sha256:3c8f6dc8dde25e7cad6814e9ee01b8efabe7451719fafa18c84792eb35aa8bbe"
	base := `{"session_id":"SESSION-001","request":"Webアプリを作りたい","request_digest":"` + requestDigest + `","model":"Claude Sonnet 5","current_time":"2026-08-09T12:00:00Z"`
	for _, unknown := range []string{"standard", "Bounded_Acceptance", "bounded", " bounded_acceptance"} {
		payload := json.RawMessage(base + `,"execution_profile":"` + unknown + `"}`)
		if err := ValidatePayload("interaction.start", payload); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("execution_profile=%q error = %v, want ErrInvalidPayload", unknown, err)
		}
	}
}
