package process

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
)

// TestExecuteInteractionArchiveApprovalGate confirms archive/unarchive
// follow the same approval-gate convention as every other Interaction
// Command (e.g. ExecuteInteractionAction): an unapproved call returns
// ErrInteractionApprovalRequired before ever claiming a Command Ledger
// entry or touching the Session.
func TestExecuteInteractionArchiveApprovalGate(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	seeded := writeArchiveTestSession(t, root, "SESSION-ARCHIVE-GATE", at)
	input := InteractionArchiveInput{
		VaultRoot: root, SessionID: seeded.SessionID, ExpectedVersion: seeded.Version,
		CurrentTime: at.Add(time.Minute), CommandID: "CMD-ARCHIVE-GATE",
	}
	if _, err := ExecuteInteractionArchive(context.Background(), input, false); !errors.Is(err, ErrInteractionApprovalRequired) {
		t.Fatalf("unapproved ExecuteInteractionArchive() error = %v", err)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	if _, err := ledger.Get(context.Background(), input.CommandID); !errors.Is(err, commandledger.ErrNotFound) {
		t.Fatalf("unapproved call claimed a Command Ledger entry: %v", err)
	}
	stored, err := InspectInteraction(context.Background(), root, seeded.SessionID)
	if err != nil || stored.IsArchived() {
		t.Fatalf("unapproved call touched Session: %#v, %v", stored, err)
	}
}

// TestExecuteInteractionArchiveSucceedsRecordsLedgerAndUnarchiveReverses
// covers the archive success Ledger entry, the unarchive success Ledger
// entry, and confirms a fresh reload of the Session (equivalent to a
// daemon restart, since InspectInteraction goes through the same Vault
// Adapter Store.Get as any other process boundary) still reports archived
// -- the canonical source of truth is the persisted Turn history, not any
// in-memory state.
func TestExecuteInteractionArchiveSucceedsRecordsLedgerAndUnarchiveReverses(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	seeded := writeArchiveTestSession(t, root, "SESSION-ARCHIVE-OK", at)

	archiveInput := InteractionArchiveInput{
		VaultRoot: root, SessionID: seeded.SessionID, ExpectedVersion: seeded.Version,
		CurrentTime: at.Add(time.Minute), CommandID: "CMD-ARCHIVE-OK",
	}
	result, err := ExecuteInteractionArchive(context.Background(), archiveInput, true)
	if err != nil || !result.SessionCommitted || !result.Session.IsArchived() || result.Session.State != seeded.State {
		t.Fatalf("ExecuteInteractionArchive() = %#v, %v", result, err)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	archiveRecord, ledgerErr := ledger.Get(context.Background(), archiveInput.CommandID)
	if ledgerErr != nil || archiveRecord.State != commandledger.StateSucceeded || archiveRecord.Failure != nil {
		t.Fatalf("archive Ledger record = %#v, %v", archiveRecord, ledgerErr)
	}

	// Reload independent of the in-process result, simulating a daemon
	// restart: archived-ness must still be derivable purely from the
	// persisted Turn history.
	reloaded, err := InspectInteraction(context.Background(), root, seeded.SessionID)
	if err != nil || !reloaded.IsArchived() {
		t.Fatalf("reload after archive = %#v, %v, want IsArchived() true", reloaded, err)
	}

	unarchiveInput := InteractionArchiveInput{
		VaultRoot: root, SessionID: seeded.SessionID, ExpectedVersion: result.Session.Version,
		CurrentTime: at.Add(2 * time.Minute), CommandID: "CMD-UNARCHIVE-OK",
	}
	unarchived, err := ExecuteInteractionUnarchive(context.Background(), unarchiveInput, true)
	if err != nil || !unarchived.SessionCommitted || unarchived.Session.IsArchived() {
		t.Fatalf("ExecuteInteractionUnarchive() = %#v, %v", unarchived, err)
	}
	unarchiveRecord, ledgerErr := ledger.Get(context.Background(), unarchiveInput.CommandID)
	if ledgerErr != nil || unarchiveRecord.State != commandledger.StateSucceeded || unarchiveRecord.Failure != nil {
		t.Fatalf("unarchive Ledger record = %#v, %v", unarchiveRecord, ledgerErr)
	}
	reloadedAgain, err := InspectInteraction(context.Background(), root, seeded.SessionID)
	if err != nil || reloadedAgain.IsArchived() {
		t.Fatalf("reload after unarchive = %#v, %v, want IsArchived() false", reloadedAgain, err)
	}
}

// TestExecuteInteractionArchiveStaleCASClaimsBeforeEffectAndRecordsFailure
// covers three §17 items together, since they are one causal chain: the
// Command Ledger claim happens before the CAS effect is attempted (proven
// by the claimed entry existing even though the effect below it fails),
// the stale expected_version itself is rejected, and the resulting
// RecordedCommandError/Ledger Failure carry the typed
// Code/Stage/Partial "failure envelope" -- Interaction's existing
// (Code, Stage, Partial) shape, matching every sibling Interaction Command
// this round explicitly leaves un-migrated to failure.Envelope (Review and
// Task execution only).
func TestExecuteInteractionArchiveStaleCASClaimsBeforeEffectAndRecordsFailure(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	seeded := writeArchiveTestSession(t, root, "SESSION-ARCHIVE-STALE", at)
	input := InteractionArchiveInput{
		VaultRoot: root, SessionID: seeded.SessionID, ExpectedVersion: seeded.Version + 1,
		CurrentTime: at.Add(time.Minute), CommandID: "CMD-ARCHIVE-STALE",
	}
	_, err := ExecuteInteractionArchive(context.Background(), input, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "INTERACTION_ARCHIVE_FAILED" || recorded.Stage != "interaction_preflight" || recorded.Partial {
		t.Fatalf("stale CAS ExecuteInteractionArchive() error = %v, recorded=%#v", err, recorded)
	}
	ledger, _ := vault.NewWorkspaceCommandLedgerStore(root)
	record, ledgerErr := ledger.Get(context.Background(), input.CommandID)
	if ledgerErr != nil {
		t.Fatalf("stale CAS did not durably claim the Command ID before the effect failed: %v", ledgerErr)
	}
	if record.State != commandledger.StateFailed || record.Failure == nil || record.Failure.Code != "INTERACTION_ARCHIVE_FAILED" {
		t.Fatalf("stale CAS Ledger record = %#v", record)
	}
	stored, err := InspectInteraction(context.Background(), root, seeded.SessionID)
	if err != nil || stored.IsArchived() {
		t.Fatalf("stale CAS mutated Session: %#v, %v", stored, err)
	}
}

// TestExecuteInteractionArchiveSameCommandIDReplaysWithoutSecondEffect
// covers the Command Ledger's replay semantics: resubmitting the exact
// same Command ID returns the identical cached result and never appends a
// second Turn, matching every other durable Command in this Domain.
func TestExecuteInteractionArchiveSameCommandIDReplaysWithoutSecondEffect(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	seeded := writeArchiveTestSession(t, root, "SESSION-ARCHIVE-REPLAY", at)
	input := InteractionArchiveInput{
		VaultRoot: root, SessionID: seeded.SessionID, ExpectedVersion: seeded.Version,
		CurrentTime: at.Add(time.Minute), CommandID: "CMD-ARCHIVE-REPLAY",
	}
	first, err := ExecuteInteractionArchive(context.Background(), input, true)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := ExecuteInteractionArchive(context.Background(), input, true)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(first)
	replayedJSON, _ := json.Marshal(replayed)
	if string(firstJSON) != string(replayedJSON) {
		t.Fatalf("replay result mismatch: first=%s replayed=%s", firstJSON, replayedJSON)
	}
	stored, err := InspectInteraction(context.Background(), root, seeded.SessionID)
	if err != nil || len(stored.Turns) != 1 {
		t.Fatalf("replay appended a second Turn: %#v, %v, want exactly 1 Turn", stored, err)
	}
	// A genuinely new Command ID against the now-archived Session is a
	// distinct request, not a replay, and is correctly rejected by
	// RecordArchive's own already-archived precondition -- not silently
	// treated as another success.
	secondCommand := input
	secondCommand.CommandID = "CMD-ARCHIVE-REPLAY-SECOND"
	if _, err := ExecuteInteractionArchive(context.Background(), secondCommand, true); err == nil {
		t.Fatalf("second distinct archive Command on an already-archived Session unexpectedly succeeded")
	}
}

// TestExecuteInteractionArchiveDoesNotChangeUnrelatedVaultState confirms
// archive/unarchive touch only the Interaction Store's own file tree
// (.workspace-os/interactions/): every other Vault path (Project, Task,
// Deliverable, Review, Revision evidence) is byte-for-byte unchanged,
// matching §12's "visibility metadata only" contract.
func TestExecuteInteractionArchiveDoesNotChangeUnrelatedVaultState(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	seeded := writeArchiveTestSession(t, root, "SESSION-ARCHIVE-ISOLATED", at)
	before := planVaultSnapshot(t, root)
	input := InteractionArchiveInput{
		VaultRoot: root, SessionID: seeded.SessionID, ExpectedVersion: seeded.Version,
		CurrentTime: at.Add(time.Minute), CommandID: "CMD-ARCHIVE-ISOLATED",
	}
	if _, err := ExecuteInteractionArchive(context.Background(), input, true); err != nil {
		t.Fatal(err)
	}
	after := planVaultSnapshot(t, root)
	changed := map[string]bool{}
	for path, content := range after {
		if before[path] != content {
			changed[path] = true
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			changed[path] = true
		}
	}
	if len(changed) == 0 {
		t.Fatalf("archive produced no Vault change at all")
	}
	// Only the Interaction Store's own file tree and the Command Ledger's
	// own claim/finish bookkeeping are expected to change -- Project, Task,
	// Deliverable, Review, and Revision evidence must be untouched.
	interactionsPrefix := filepath.ToSlash(filepath.Join(".workspace-os", "interactions")) + "/"
	commandsPrefix := filepath.ToSlash(filepath.Join(".workspace-os", "commands")) + "/"
	for path := range changed {
		slashPath := filepath.ToSlash(path)
		if !strings.Contains(slashPath, interactionsPrefix) && !strings.Contains(slashPath, commandsPrefix) {
			t.Fatalf("unexpected unrelated Vault change at %s, want only under %s or %s", path, interactionsPrefix, commandsPrefix)
		}
	}
}

func writeArchiveTestSession(t *testing.T, root, sessionID string, at time.Time) interaction.Record {
	t.Helper()
	record, err := interaction.New(sessionID, "アーカイブ確認用の依頼", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	store, err := vault.NewInteractionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return record
}
