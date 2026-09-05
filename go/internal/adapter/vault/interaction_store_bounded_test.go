package vault

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/interaction"
)

// TestInteractionStoreRejectsUnreservedBoundedPlanOnWrite is the Store-write
// boundary for ADR-0072's reservation invariant: a hand-crafted Record
// (bypassing RecordPlan's own domain-level guard entirely) that carries a
// bounded_acceptance TurnPlanGenerated Turn with no preceding
// TurnPlanGenerationReserved Turn must be rejected at write time --
// encodeInteractionRecord calls record.Validate() before ever marshaling.
func TestInteractionStoreRejectsUnreservedBoundedPlanOnWrite(t *testing.T) {
	root := t.TempDir()
	store, err := NewInteractionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	record, err := interaction.NewWithProfile("SESSION-BOUNDED-STORE-WRITE", "依頼", "Claude Sonnet 5", interaction.ProfileBoundedAcceptance, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	plan := vaultInteractionPlan()
	plan.CEOQuestions = []string{}
	digest, err := interaction.DigestPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	malformed := record.Clone()
	malformed.Turns = append(malformed.Turns, interaction.Turn{Kind: "plan_generated", At: at.Add(time.Minute), Plan: &plan, PlanDigest: digest})
	malformed.Version++
	malformed.State = interaction.StatePlanApprovalRequired
	if err := store.Update(context.Background(), malformed, record.Version); !errors.Is(err, interaction.ErrInvalidSession) {
		t.Fatalf("Update() with an unreserved bounded Plan Turn = %v, want ErrInvalidSession", err)
	}
	// The bad Turn must never have reached disk: a fresh Get() still
	// returns the original, unmodified record.
	stored, getErr := store.Get(context.Background(), record.SessionID)
	if getErr != nil || stored.Version != record.Version || len(stored.Turns) != 0 {
		t.Fatalf("Get() after rejected write = %#v, %v, want the original unmodified record", stored, getErr)
	}
}

// TestInteractionStoreRejectsUnknownProfileOnWrite is the Store-write
// boundary for ADR-0072's closed Profile enum: a hand-crafted Record
// carrying an unrecognized Profile string (never standard, never
// bounded_acceptance) must be rejected before it ever reaches disk.
func TestInteractionStoreRejectsUnknownProfileOnWrite(t *testing.T) {
	root := t.TempDir()
	store, err := NewInteractionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	record, err := interaction.New("SESSION-UNKNOWN-PROFILE-WRITE", "依頼", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	corrupted := record.Clone()
	corrupted.Profile = interaction.Profile("corrupted-value")
	if err := store.Create(context.Background(), corrupted); !errors.Is(err, interaction.ErrInvalidSession) {
		t.Fatalf("Create() with an unknown Profile = %v, want ErrInvalidSession", err)
	}
}

// TestInteractionStoreRejectsUnknownProfileOnDecode is the Store-read
// boundary: a file written with an unrecognized profile value (e.g. by an
// older or buggy version of this code, or external tampering) must be
// rejected on load.
func TestInteractionStoreRejectsUnknownProfileOnDecode(t *testing.T) {
	root := t.TempDir()
	store, err := NewInteractionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	record, err := interaction.New("SESSION-UNKNOWN-PROFILE-DECODE", "依頼", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	path := store.recordPath(record.SessionID)
	malformedJSON := `{
		"schema_version": 1,
		"session_id": "SESSION-UNKNOWN-PROFILE-DECODE",
		"request": "依頼",
		"request_digest": "` + record.RequestDigest + `",
		"model": "Claude Sonnet 5",
		"created_at": "2026-08-09T12:00:00Z",
		"state": "plan_generation_approval_required",
		"version": 1,
		"profile": "corrupted-value",
		"turns": []
	}`
	if err := os.WriteFile(path, []byte(malformedJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), record.SessionID); !errors.Is(err, interaction.ErrInvalidSession) {
		t.Fatalf("Get() on a decoded unknown Profile = %v, want ErrInvalidSession", err)
	}
}

// TestInteractionStoreRejectsUnreservedBoundedPlanOnDecode is the Store-read
// boundary: a file that was somehow written with an unreserved bounded Plan
// Turn (e.g. by an older or buggy version of this code) must be rejected on
// load, not silently accepted -- Get()/read() calls record.Validate()
// immediately after JSON decode.
func TestInteractionStoreRejectsUnreservedBoundedPlanOnDecode(t *testing.T) {
	root := t.TempDir()
	store, err := NewInteractionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	record, err := interaction.NewWithProfile("SESSION-BOUNDED-STORE-DECODE", "依頼", "Claude Sonnet 5", interaction.ProfileBoundedAcceptance, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	path := store.recordPath(record.SessionID)
	// Hand-write a JSON body carrying an unreserved TurnPlanGenerated Turn
	// directly to disk, bypassing every Go-level constructor and Update()'s
	// own pre-write Validate() call.
	malformedJSON := `{
		"schema_version": 1,
		"session_id": "SESSION-BOUNDED-STORE-DECODE",
		"request": "依頼",
		"request_digest": "` + record.RequestDigest + `",
		"model": "Claude Sonnet 5",
		"created_at": "2026-08-09T12:00:00Z",
		"state": "plan_approval_required",
		"version": 2,
		"profile": "bounded_acceptance",
		"turns": [
			{"kind": "plan_generated", "at": "2026-08-09T12:01:00Z",
			 "plan": {"project_name":"案件","objective":"目的","summary":"概要","required_departments":[],"required_roles":[],"assigned_existing_employees":["PLAN-001"],"missing_roles":[],"proposed_tasks":[{"proposal_id":"PROPOSED-001","title":"実行する","assignee_id":"PLAN-001","dependency_ids":[],"rationale":"必要"}],"risks":[],"ceo_questions":[],"plan_only":true},
			 "plan_digest": "sha256:0000000000000000000000000000000000000000000000000000000000000000"}
		]
	}`
	if err := os.WriteFile(path, []byte(malformedJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), record.SessionID); !errors.Is(err, interaction.ErrInvalidSession) {
		t.Fatalf("Get() on a decoded unreserved bounded Plan Turn = %v, want ErrInvalidSession", err)
	}
}
