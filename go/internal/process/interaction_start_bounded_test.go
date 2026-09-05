package process

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/interaction"
)

// unavailableProviderServer always fails, for tests that only care about
// what happens before any Provider call actually needs to succeed.
func unavailableProviderServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = response.Write([]byte(`{"error":{"type":"overloaded_error","message":"unavailable"}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestInteractionStartCommandDigestStandardCompatibleAndProfileChangeConflicts
// covers two required boundaries (PB-3an.2a P2/item 6): (1) a standard
// (Profile-omitted) interaction.start outer claim's own request digest is
// exactly what commandledger.RequestDigest computes over the pre-ADR-0072
// four-field shape {session_id, request_digest, model, current_time} --
// byte-for-byte, since Profile's omitempty tag drops it entirely when
// empty; (2) resubmitting the exact same Command ID with a different
// execution_profile is treated as a different request (commandledger's
// existing "same ID, different payload" conflict rule), never as a replay.
func TestInteractionStartCommandDigestStandardCompatibleAndProfileChangeConflicts(t *testing.T) {
	root := writeApproveAndExecuteVault(t)
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	sessionID := "SESSION-DIGEST-COMPAT"
	candidate, err := interaction.New(sessionID, "アプリを完成させる", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	server := unavailableProviderServer(t)
	provider := ClaudeProcessConfig{APIKey: "fake", ProviderModel: "claude-test", BaseURL: server.URL}
	commandID := "CMD-DIGEST-COMPAT-001"

	// (1) standard call: no Profile field at all.
	_, _ = ExecuteInteractionStart(context.Background(), InteractionStartInput{
		VaultRoot: root, SessionID: sessionID, Request: candidate.Request, RequestDigest: candidate.RequestDigest,
		Model: candidate.Model, CurrentTime: at, CommandID: commandID,
	}, provider, server.Client(), true)

	workspaceLedger, err := vault.NewWorkspaceCommandLedgerStore(root)
	if err != nil {
		t.Fatal(err)
	}
	outerRecord, err := workspaceLedger.Get(context.Background(), commandID)
	if err != nil {
		t.Fatal(err)
	}
	expectedDigest, err := commandledger.RequestDigest(struct {
		SessionID     string    `json:"session_id"`
		RequestDigest string    `json:"request_digest"`
		Model         string    `json:"model"`
		CurrentTime   time.Time `json:"current_time"`
	}{sessionID, candidate.RequestDigest, candidate.Model, at})
	if err != nil {
		t.Fatal(err)
	}
	if outerRecord.RequestDigest != expectedDigest {
		t.Fatalf("standard interaction.start claim digest = %s, want %s (byte-identical to the pre-ADR-0072 4-field shape)", outerRecord.RequestDigest, expectedDigest)
	}

	// (2) same Command ID, only execution_profile differs -> conflict, not replay.
	_, conflictErr := ExecuteInteractionStart(context.Background(), InteractionStartInput{
		VaultRoot: root, SessionID: sessionID, Request: candidate.Request, RequestDigest: candidate.RequestDigest,
		Model: candidate.Model, CurrentTime: at, CommandID: commandID, Profile: "bounded_acceptance",
	}, provider, server.Client(), true)
	if !errors.Is(conflictErr, commandledger.ErrRequestConflict) {
		t.Fatalf("same Command ID with a different execution_profile error = %v, want commandledger.ErrRequestConflict", conflictErr)
	}
}
