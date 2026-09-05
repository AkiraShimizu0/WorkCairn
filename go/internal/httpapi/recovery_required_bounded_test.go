package httpapi

import (
	"testing"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/failure"
	workspaceprocess "github.com/AkiraShimizu0/WorkCairn/go/internal/process"
)

// TestMapCommandErrorPrefersEnvelopeRecoveryRequiredOverPartial is the
// ADR-0072 focused correction (PB-3an.2a P1): the top-level
// CommandError.RecoveryRequired projection must come from a present
// failure.Envelope's own RecoveryRequired, never unconditionally from
// Partial -- a bounded_acceptance stop is Partial (Review/Task evidence
// committed) but must never be reported as RecoveryRequired.
func TestMapCommandErrorPrefersEnvelopeRecoveryRequiredOverPartial(t *testing.T) {
	recorded := &workspaceprocess.RecordedCommandError{
		Code: "REVIEWED_WORKFLOW_BOUNDED_STOP", Stage: "revision_forbidden", Partial: true,
		Envelope: &failure.Envelope{
			SchemaVersion: failure.SchemaVersion, Code: "REVIEWED_WORKFLOW_BOUNDED_STOP", Stage: "revision_forbidden",
			Partial: true, RecoveryRequired: false,
		},
	}
	status, mapped := mapCommandError(recorded)
	if status != 422 || mapped == nil || mapped.RecoveryRequired {
		t.Fatalf("mapCommandError() = %d, %#v, want RecoveryRequired=false", status, mapped)
	}
	if mapped.Details == nil || mapped.Details.RecoveryRequired {
		t.Fatalf("mapped.Details = %#v, want RecoveryRequired=false", mapped.Details)
	}
}

// TestMapCommandErrorLegacyRecordWithoutEnvelopeFallsBackToPartial pins the
// one remaining case that still uses Partial: a pre-ADR-0041 record that
// never carries an Envelope at all.
func TestMapCommandErrorLegacyRecordWithoutEnvelopeFallsBackToPartial(t *testing.T) {
	recorded := &workspaceprocess.RecordedCommandError{Code: "REVISION_LIMIT_REACHED", Stage: "revision_limit", Partial: true}
	_, mapped := mapCommandError(recorded)
	if mapped == nil || !mapped.RecoveryRequired {
		t.Fatalf("legacy no-Envelope mapped = %#v, want RecoveryRequired=true (from Partial)", mapped)
	}
}

// TestMapCommandErrorStandardRecoverableFailureStillTrue guards against
// over-correcting: a standard, genuinely recoverable failure whose Envelope
// already says RecoveryRequired=true (e.g. REVISION_LIMIT_REACHED once
// migrated) must still surface as true, not be flipped to false by this
// fix.
func TestMapCommandErrorStandardRecoverableFailureStillTrue(t *testing.T) {
	recorded := &workspaceprocess.RecordedCommandError{
		Code: "REVISION_LIMIT_REACHED", Stage: "revision_limit", Partial: true,
		Envelope: &failure.Envelope{
			SchemaVersion: failure.SchemaVersion, Code: "REVISION_LIMIT_REACHED", Stage: "revision_limit",
			Partial: true, RecoveryRequired: true,
		},
	}
	_, mapped := mapCommandError(recorded)
	if mapped == nil || !mapped.RecoveryRequired {
		t.Fatalf("standard recoverable mapped = %#v, want RecoveryRequired=true", mapped)
	}
}
