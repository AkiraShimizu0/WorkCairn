package interaction

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProfileValidAcceptsClosedValuesRejectsUnknown(t *testing.T) {
	if !ProfileStandard.Valid() || !ProfileBoundedAcceptance.Valid() {
		t.Fatalf("closed values must be valid: standard=%v bounded=%v", ProfileStandard.Valid(), ProfileBoundedAcceptance.Valid())
	}
	for _, unknown := range []Profile{"bounded", "Bounded_Acceptance", "standard", " bounded_acceptance", "bounded_acceptance "} {
		if unknown.Valid() {
			t.Fatalf("Profile(%q).Valid() = true, want false", unknown)
		}
	}
}

func TestNewWithProfileDelegatesToNewAndValidatesProfile(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	standard, err := New("SESSION-BOUNDED-001", "依頼", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := NewWithProfile("SESSION-BOUNDED-001", "依頼", "Claude Sonnet 5", ProfileBoundedAcceptance, at)
	if err != nil {
		t.Fatal(err)
	}
	if bounded.Profile != ProfileBoundedAcceptance {
		t.Fatalf("bounded.Profile = %q, want %q", bounded.Profile, ProfileBoundedAcceptance)
	}
	// RequestDigest must be identical between the two -- Profile never
	// participates in this hash (ADR-0072).
	if bounded.RequestDigest != standard.RequestDigest {
		t.Fatalf("bounded RequestDigest = %s, standard = %s, want equal", bounded.RequestDigest, standard.RequestDigest)
	}
	if _, err := NewWithProfile("SESSION-BOUNDED-002", "依頼", "Claude Sonnet 5", Profile("unknown"), at); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("unknown profile error = %v, want ErrInvalidSession", err)
	}
	// NewWithProfile("", ..., ProfileStandard, ...) must be usable as an
	// exact drop-in for New: same fields, same Validate() outcome.
	explicitStandard, err := NewWithProfile("SESSION-BOUNDED-003", "依頼", "Claude Sonnet 5", ProfileStandard, at)
	if err != nil || explicitStandard.Profile != ProfileStandard {
		t.Fatalf("explicit standard = %#v, %v", explicitStandard, err)
	}
}

func TestStandardProfileJSONOmitsFieldAndByteIdenticalToPreExistingShape(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	standard, err := New("SESSION-BOUNDED-JSON", "依頼", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(standard)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"profile"`) {
		t.Fatalf("standard Session JSON must omit profile entirely, got %s", encoded)
	}
	bounded, err := NewWithProfile("SESSION-BOUNDED-JSON-2", "依頼", "Claude Sonnet 5", ProfileBoundedAcceptance, at)
	if err != nil {
		t.Fatal(err)
	}
	boundedEncoded, err := json.Marshal(bounded)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(boundedEncoded), `"profile":"bounded_acceptance"`) {
		t.Fatalf("bounded Session JSON must include profile, got %s", boundedEncoded)
	}
}

func TestRecordValidateRejectsUnknownProfile(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	record, err := New("SESSION-BOUNDED-UNKNOWN", "依頼", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a corrupted/forged Profile value that never went through
	// NewWithProfile's own validation -- Validate() itself must still
	// reject it (ADR-0072's fail-closed guard, not fail-open to standard).
	record.Profile = Profile("corrupted-value")
	if err := record.Validate(); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Validate() with unknown Profile = %v, want ErrInvalidSession", err)
	}
}

func TestValidateTransitionEnforcesProfileImmutability(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	current, err := New("SESSION-BOUNDED-IMMUTABLE", "依頼", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	// next differs from current only by Profile (no Turn appended) -- this
	// isolates ValidateTransition's own Profile-immutability check from
	// every other invariant it also enforces.
	next := current.Clone()
	next.Profile = ProfileBoundedAcceptance
	if err := ValidateTransition(current, next, current.Version); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("ValidateTransition() with changed Profile and no Turn appended = %v, want ErrVersionConflict", err)
	}
}

func TestPlanGenerationReservationVersionSequenceAndCrossKindValidation(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	// v1: bounded Session created.
	record, err := NewWithProfile("SESSION-BOUNDED-RESERVE", "依頼", "Claude Sonnet 5", ProfileBoundedAcceptance, at)
	if err != nil || record.Version != 1 {
		t.Fatalf("v1 Session = %#v, %v", record, err)
	}
	if _, reserved := record.PlanGenerationReservation(); reserved {
		t.Fatal("fresh Session must not already report a reservation")
	}
	// v2: reservation Turn committed, before any Provider call.
	reserved, err := record.RecordPlanGenerationReservation("CHILD-0000000000000000000000000000ab", at.Add(time.Minute))
	if err != nil || reserved.Version != 2 || reserved.State != StatePlanGenerationApprovalRequired {
		t.Fatalf("v2 reservation = %#v, %v", reserved, err)
	}
	childCommandID, isReserved := reserved.PlanGenerationReservation()
	if !isReserved || childCommandID != "CHILD-0000000000000000000000000000ab" {
		t.Fatalf("PlanGenerationReservation() = %q, %v", childCommandID, isReserved)
	}
	// A second reservation attempt on an already-reserved Session must be
	// rejected -- at most one reservation Turn ever, regardless of outcome.
	if _, err := reserved.RecordPlanGenerationReservation("CHILD-1111111111111111111111111111ab", at.Add(2*time.Minute)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second reservation error = %v, want ErrInvalidState", err)
	}
	// v3: the actual Plan generation result, appended on top of v2.
	plan := interactionTestPlan([]string{})
	withPlan, err := reserved.RecordPlan(plan, at.Add(3*time.Minute))
	if err != nil || withPlan.Version != 3 || withPlan.State != StatePlanApprovalRequired {
		t.Fatalf("v3 RecordPlan() = %#v, %v", withPlan, err)
	}

	// Cross-kind validation: ReservedChildCommandID must be empty on every
	// other Turn Kind (here TurnArchived, an arbitrary example of an
	// unrelated Kind), and standard Sessions can never carry
	// TurnPlanGenerationReserved at all.
	archived := withPlan.Clone()
	archived.Turns = append(archived.Turns, Turn{Kind: TurnArchived, At: at.Add(4 * time.Minute), ReservedChildCommandID: "CHILD-0000000000000000000000000000ab"})
	archived.Version++
	if err := archived.Validate(); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("TurnArchived with ReservedChildCommandID set = %v, want ErrInvalidSession", err)
	}

	standard, err := New("SESSION-BOUNDED-RESERVE-STD", "依頼", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := standard.RecordPlanGenerationReservation("CHILD-2222222222222222222222222222ab", at.Add(time.Minute)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("standard profile reservation error = %v, want ErrInvalidState", err)
	}
	forced := standard.Clone()
	forced.Turns = append(forced.Turns, Turn{Kind: TurnPlanGenerationReserved, At: at.Add(time.Minute), ReservedChildCommandID: "CHILD-2222222222222222222222222222ab"})
	forced.Version++
	if err := forced.Validate(); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("standard profile with forced TurnPlanGenerationReserved = %v, want ErrInvalidSession", err)
	}
}

// TestRecordPlanRequiresPrecedingReservationForBoundedProfile is the
// PB-3an.2a P1 fix: RecordPlan itself (not only the Process layer's own
// call-site discipline) must refuse to append a TurnPlanGenerated Turn for
// a bounded_acceptance Session that never reserved its one allowed attempt.
func TestRecordPlanRequiresPrecedingReservationForBoundedProfile(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	unreserved, err := NewWithProfile("SESSION-BOUNDED-NORESERVE", "依頼", "Claude Sonnet 5", ProfileBoundedAcceptance, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unreserved.RecordPlan(interactionTestPlan([]string{}), at.Add(time.Minute)); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("RecordPlan() without a reservation = %v, want ErrInvalidState", err)
	}

	// Domain-level Validate() must independently reject a hand-crafted
	// Record that reached a TurnPlanGenerated Turn without ever having a
	// TurnPlanGenerationReserved Turn precede it -- not just RecordPlan's
	// own guard, since a Record can also arrive via Store decode.
	plan := interactionTestPlan([]string{})
	digest, err := DigestPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	malformed := unreserved.Clone()
	malformed.Turns = append(malformed.Turns, Turn{Kind: TurnPlanGenerated, At: at.Add(time.Minute), Plan: &plan, PlanDigest: digest})
	malformed.Version++
	malformed.State = StatePlanApprovalRequired
	if err := malformed.Validate(); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Validate() with unreserved bounded TurnPlanGenerated = %v, want ErrInvalidSession", err)
	}
}

// TestRecordPlanRejectsMismatchedReservedChildCommandID guards the binding
// between a reservation and the Plan it authorizes: the Plan Turn's own
// child Command ID must equal the reservation's exactly, not merely "some"
// child ID.
func TestRecordPlanRejectsMismatchedReservedChildCommandID(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	reserved, err := NewWithProfile("SESSION-BOUNDED-MISMATCH", "依頼", "Claude Sonnet 5", ProfileBoundedAcceptance, at)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err = reserved.RecordPlanGenerationReservation("CHILD-3333333333333333333333333333aa", at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	withPlan, err := reserved.RecordPlan(interactionTestPlan([]string{}), at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	mismatched := withPlan.Clone()
	mismatched.Turns[len(mismatched.Turns)-1].ReservedChildCommandID = "CHILD-4444444444444444444444444444bb"
	if err := mismatched.Validate(); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Validate() with a Plan Turn whose child Command ID diverges from the reservation = %v, want ErrInvalidSession", err)
	}
}

// TestRecordPlanRejectsReservationAfterPlanIsAlreadyGenerated guards
// ordering: a reservation Turn appended after TurnPlanGenerated (rather
// than before it) must still be rejected -- this exercises the ordering
// requirement independently of the "at most one reservation ever" rule
// (TestPlanGenerationReservationVersionSequenceAndCrossKindValidation
// already covers the latter).
func TestRecordPlanRejectsReservationAfterPlanIsAlreadyGenerated(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	reserved, err := NewWithProfile("SESSION-BOUNDED-LATE-RESERVE", "依頼", "Claude Sonnet 5", ProfileBoundedAcceptance, at)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err = reserved.RecordPlanGenerationReservation("CHILD-5555555555555555555555555555cc", at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	withPlan, err := reserved.RecordPlan(interactionTestPlan([]string{}), at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	// A hand-crafted history with a *second* reservation Turn appended
	// after the Plan was already generated must be rejected regardless of
	// what state it claims -- Validate()'s TurnPlanGenerationReserved case
	// requires state == StatePlanGenerationApprovalRequired, which no
	// longer holds once a Plan (with no CEOQuestions) has advanced the
	// Session to StatePlanApprovalRequired.
	late := withPlan.Clone()
	late.Turns = append(late.Turns, Turn{Kind: TurnPlanGenerationReserved, At: at.Add(3 * time.Minute), ReservedChildCommandID: "CHILD-6666666666666666666666666666dd"})
	late.Version++
	if err := late.Validate(); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Validate() with a reservation appended after Plan generation = %v, want ErrInvalidSession", err)
	}
}

// TestRecordPlanStandardProfileUnaffectedByReservationRequirement pins that
// a standard Session's RecordPlan behavior (no reservation concept at all)
// is byte-for-byte unchanged by the bounded-only guard added above.
func TestRecordPlanStandardProfileUnaffectedByReservationRequirement(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	record, err := New("SESSION-STANDARD-RESERVE-UNAFFECTED", "依頼", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	withPlan, err := record.RecordPlan(interactionTestPlan([]string{}), at.Add(time.Minute))
	if err != nil || withPlan.Version != 2 || withPlan.State != StatePlanApprovalRequired ||
		withPlan.Turns[0].ReservedChildCommandID != "" {
		t.Fatalf("standard RecordPlan() = %#v, %v, want unaffected", withPlan, err)
	}
}

func TestNextIsInspectOnlyForBoundedPlanGenerationAndClarification(t *testing.T) {
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	fresh, err := NewWithProfile("SESSION-BOUNDED-NEXT-1", "依頼", "Claude Sonnet 5", ProfileBoundedAcceptance, at)
	if err != nil {
		t.Fatal(err)
	}
	// Before any reservation, a bounded Session's first attempt is still
	// offered normally, exactly like standard.
	next, err := fresh.Next()
	if err != nil || next.Kind != NextApprovePlanGeneration || next.Operation != "interaction.plan.generate" {
		t.Fatalf("Next() before reservation = %#v, %v", next, err)
	}

	reserved, err := fresh.RecordPlanGenerationReservation("CHILD-3333333333333333333333333333ab", at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	stuck, err := reserved.Next()
	if err != nil {
		t.Fatal(err)
	}
	if stuck.Kind != NextInspectWorkflow || stuck.Operation != "" || stuck.ApprovalRequired {
		t.Fatalf("Next() after reservation with no result = %#v, want inspect-only NextInspectWorkflow", stuck)
	}
	if len(stuck.Commands) != 1 || stuck.Commands[0].CommandID != "CHILD-3333333333333333333333333333ab" {
		t.Fatalf("Next().Commands = %#v, want the reserved child Command ID", stuck.Commands)
	}

	withQuestions, err := reserved.RecordPlan(interactionTestPlan([]string{"対象端末は？"}), at.Add(2*time.Minute))
	if err != nil || withQuestions.State != StateClarificationRequired {
		t.Fatalf("RecordPlan() with questions = %#v, %v", withQuestions, err)
	}
	clarification, err := withQuestions.Next()
	if err != nil {
		t.Fatal(err)
	}
	if clarification.Kind != NextInspectWorkflow || clarification.Operation != "" || clarification.ApprovalRequired {
		t.Fatalf("Next() at StateClarificationRequired for bounded Session = %#v, want inspect-only", clarification)
	}

	// Standard Sessions are completely unaffected: same state, normal
	// answer_clarifications offer.
	standardWithQuestions, _ := New("SESSION-BOUNDED-NEXT-2", "依頼", "Claude Sonnet 5", at)
	standardWithQuestions, _ = standardWithQuestions.RecordPlan(interactionTestPlan([]string{"対象端末は？"}), at.Add(time.Minute))
	standardNext, err := standardWithQuestions.Next()
	if err != nil || standardNext.Kind != NextAnswerClarifications || standardNext.Operation != "interaction.answer" {
		t.Fatalf("Next() for standard clarification = %#v, %v, want unaffected NextAnswerClarifications", standardNext, err)
	}
}
