package commandledger

import (
	"errors"
	"testing"
)

func validReservation() ApprovalReservationID {
	return ApprovalReservationID{
		SessionID: "SESSION-001", ExpectedVersion: 3,
		PlanDigest: "sha256:" + repeatHex(64), Profile: "bounded_acceptance",
	}
}

func repeatHex(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = 'a'
	}
	return string(out)
}

func TestDeriveApprovalReservationIDIsDeterministicAndBounded(t *testing.T) {
	input := validReservation()
	first, err := DeriveApprovalReservationID(input)
	second, secondErr := DeriveApprovalReservationID(input)
	if err != nil || secondErr != nil || first != second || ValidateCommandID(first) != nil {
		t.Fatalf("reservation IDs = %q %q; errors = %v %v", first, second, err, secondErr)
	}
}

func TestDeriveApprovalReservationIDNeverDependsOnAnOuterCommandID(t *testing.T) {
	// Two different outer Commands approving the same target must collide
	// on the same reservation ID -- there is no outer Command ID field on
	// ApprovalReservationID at all, so this is true by construction, but
	// this test pins that the same logical target always yields one ID
	// regardless of anything else the caller might otherwise pass.
	id, err := DeriveApprovalReservationID(validReservation())
	if err != nil {
		t.Fatal(err)
	}
	again, err := DeriveApprovalReservationID(validReservation())
	if err != nil || id != again {
		t.Fatalf("second derivation = %q, %v, want identical to %q", again, err, id)
	}
}

func TestDeriveApprovalReservationIDDiffersOnEachField(t *testing.T) {
	// Profile is deliberately excluded from this list: it is a closed enum
	// accepting only "bounded_acceptance" (see
	// TestDeriveApprovalReservationIDAcceptsOnlyBoundedAcceptanceProfile
	// below), so it can never legitimately "differ" to another valid value
	// the way SessionID/ExpectedVersion/PlanDigest can.
	base := validReservation()
	variants := []ApprovalReservationID{
		func() ApprovalReservationID { v := base; v.SessionID = "SESSION-002"; return v }(),
		func() ApprovalReservationID { v := base; v.ExpectedVersion = 4; return v }(),
		func() ApprovalReservationID { v := base; v.PlanDigest = "sha256:" + repeatHex(63) + "b"; return v }(),
	}
	baseID, err := DeriveApprovalReservationID(base)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{baseID: true}
	for index, variant := range variants {
		id, err := DeriveApprovalReservationID(variant)
		if err != nil {
			t.Fatalf("variant %d: %v", index, err)
		}
		if seen[id] {
			t.Fatalf("variant %d produced a colliding ID %q", index, id)
		}
		seen[id] = true
	}
}

// TestDeriveApprovalReservationIDRejectsNaiveConcatenationCollision guards
// the specific boundary-confusion shape a naive "join fields with no
// structure" primitive would be vulnerable to. Profile is now a closed
// enum (see TestDeriveApprovalReservationIDAcceptsOnlyBoundedAcceptanceProfile)
// and PlanDigest is fixed-format, so the field free enough to attempt an
// injection is SessionID: one containing a literal double-quote and colon
// (the exact characters that would matter if this primitive ever
// naively formatted JSON instead of using json.Marshal's own escaping)
// must still produce a safe, unique ID -- never one indistinguishable from
// a differently-shaped input.
func TestDeriveApprovalReservationIDRejectsNaiveConcatenationCollision(t *testing.T) {
	base := validReservation()
	injected := base
	injected.SessionID = `SESSION-001","expected_version":999,"plan_digest":"sha256:` + repeatHex(58) + `injected`
	baseID, err := DeriveApprovalReservationID(base)
	if err != nil {
		t.Fatal(err)
	}
	injectedID, err := DeriveApprovalReservationID(injected)
	if err != nil {
		t.Fatal(err)
	}
	if baseID == injectedID {
		t.Fatalf("a SessionID containing JSON-significant characters produced the same reservation ID %q as the clean input", baseID)
	}
}

// TestDeriveApprovalReservationIDAcceptsOnlyBoundedAcceptanceProfile is the
// PB-3an.2a P1 fix: Profile is a closed enum accepting exactly
// "bounded_acceptance" -- empty (standard), the literal string "standard",
// and any other arbitrary string must all be rejected. This mirrors the
// only value any production caller (ExecuteInteractionPlanApproveAndExecute,
// gated on record.Profile == interaction.ProfileBoundedAcceptance) ever
// actually passes.
func TestDeriveApprovalReservationIDAcceptsOnlyBoundedAcceptanceProfile(t *testing.T) {
	for _, profile := range []string{"", "standard", "A", "Bounded_Acceptance", "bounded_acceptance "} {
		invalid := validReservation()
		invalid.Profile = profile
		if _, err := DeriveApprovalReservationID(invalid); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("Profile=%q error = %v, want ErrInvalidRecord", profile, err)
		}
	}
	valid := validReservation()
	valid.Profile = "bounded_acceptance"
	if _, err := DeriveApprovalReservationID(valid); err != nil {
		t.Fatalf("Profile=bounded_acceptance error = %v, want accept", err)
	}
}

func TestDeriveApprovalReservationIDRejectsInvalidInput(t *testing.T) {
	tests := []ApprovalReservationID{
		{},
		func() ApprovalReservationID { v := validReservation(); v.SessionID = ""; return v }(),
		func() ApprovalReservationID { v := validReservation(); v.ExpectedVersion = 0; return v }(),
		func() ApprovalReservationID { v := validReservation(); v.PlanDigest = "not-a-digest"; return v }(),
	}
	for index, input := range tests {
		if _, err := DeriveApprovalReservationID(input); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("test %d: error = %v, want ErrInvalidRecord", index, err)
		}
	}
}
