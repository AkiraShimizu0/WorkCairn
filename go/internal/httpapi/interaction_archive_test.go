package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
	workspaceprocess "github.com/AkiraShimizu0/workcairn/go/internal/process"
)

// TestInteractionArchiveOperationAllowListedAndPayloadValidated covers §18's
// "operation allow-list" and "payload validation" items: interaction.archive
// is reachable via the standard POST /v1/commands product edge (proving it
// is on the Public Beta allow-list, since any operation missing from
// publicBetaCommandAllowed is rejected before ever reaching the Executor),
// and a payload missing the required session_id is rejected by
// commandcontract.ValidatePayload before any Session is touched.
func TestInteractionArchiveOperationAllowListedAndPayloadValidated(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	seeded := seedArchiveHTTPSession(t, root, "SESSION-ARCHIVE-HTTP-ALLOWLIST", at)
	handler := newArchiveTestHandler(t, root)

	malformed, _ := json.Marshal(map[string]any{
		"version": ContractVersion, "command_id": "CMD-ARCHIVE-HTTP-MALFORMED", "operation": "interaction.archive", "approved": true,
		"payload": map[string]any{"expected_version": seeded.Version, "current_time": at.Add(time.Minute)},
	})
	malformedRequest := httptest.NewRequest(http.MethodPost, "/v1/commands", strings.NewReader(string(malformed)))
	malformedRequest.Header.Set("Content-Type", "application/json")
	malformedResponse := httptest.NewRecorder()
	handler.ServeHTTP(malformedResponse, malformedRequest)
	if malformedResponse.Code != http.StatusBadRequest || !strings.Contains(malformedResponse.Body.String(), `"code":"INVALID_COMMAND"`) {
		t.Fatalf("missing session_id payload = %d %s", malformedResponse.Code, malformedResponse.Body.String())
	}
	stored, err := workspaceprocess.InspectInteraction(context.Background(), root, seeded.SessionID)
	if err != nil || stored.IsArchived() {
		t.Fatalf("rejected payload touched Session: %#v, %v", stored, err)
	}

	valid, _ := json.Marshal(map[string]any{
		"version": ContractVersion, "command_id": "CMD-ARCHIVE-HTTP-VALID", "operation": "interaction.archive", "approved": true,
		"payload": map[string]any{"session_id": seeded.SessionID, "expected_version": seeded.Version, "current_time": at.Add(time.Minute)},
	})
	validRequest := httptest.NewRequest(http.MethodPost, "/v1/commands", strings.NewReader(string(valid)))
	validRequest.Header.Set("Content-Type", "application/json")
	validResponse := httptest.NewRecorder()
	handler.ServeHTTP(validResponse, validRequest)
	if validResponse.Code != http.StatusOK {
		t.Fatalf("valid interaction.archive Command = %d %s", validResponse.Code, validResponse.Body.String())
	}
}

// TestInteractionListDefaultsToActiveOnlyAndSupportsArchivedFilterValues
// covers §18's "default GET /v1/interactions excludes archived" and
// "archived list inspection" items across all three documented filter
// values, plus rejection of an unsupported value.
func TestInteractionListDefaultsToActiveOnlyAndSupportsArchivedFilterValues(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	active := seedArchiveHTTPSession(t, root, "SESSION-ARCHIVE-LIST-ACTIVE", at)
	archivedSeed := seedArchiveHTTPSession(t, root, "SESSION-ARCHIVE-LIST-ARCHIVED", at)
	handler := newArchiveTestHandler(t, root)
	archiveHTTPSession(t, handler, archivedSeed.SessionID, archivedSeed.Version, at.Add(time.Minute), "CMD-ARCHIVE-LIST-SEED")

	for _, testCase := range []struct {
		query       string
		wantActive  bool
		wantArchive bool
	}{
		{"", true, false},
		{"?archived=false", true, false},
		{"?archived=true", false, true},
		{"?archived=all", true, true},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/interactions"+testCase.query, nil))
		body := response.Body.String()
		if response.Code != http.StatusOK {
			t.Fatalf("query %q = %d %s", testCase.query, response.Code, body)
		}
		if strings.Contains(body, active.SessionID) != testCase.wantActive || strings.Contains(body, archivedSeed.SessionID) != testCase.wantArchive {
			t.Fatalf("query %q body = %s, want active=%t archived=%t", testCase.query, body, testCase.wantActive, testCase.wantArchive)
		}
	}

	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/v1/interactions?archived=bogus", nil))
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), `"code":"INVALID_QUERY_PARAMETER"`) {
		t.Fatalf("invalid archived filter = %d %s", invalid.Code, invalid.Body.String())
	}
}

// TestInteractionArchivedSessionDetailConversationAndWorkReportRemainAccessible
// covers §18's "archived detail retrieval", "archived conversation
// retrieval", and "archived work-report retrieval" items: archive hides a
// Session from the default list (already covered above), never restricts
// direct access to it -- matching §11.
func TestInteractionArchivedSessionDetailConversationAndWorkReportRemainAccessible(t *testing.T) {
	root := t.TempDir()
	at := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	handler := newArchiveTestHandler(t, root)
	// InspectConversation additionally requires a resolvable Organization
	// roster (unrelated to archive itself); workspace.setup provides the
	// minimal starter roster the same way every other conversation-endpoint
	// test does.
	setupWorkspaceHTTP(t, handler, at)
	seeded := seedArchiveHTTPSession(t, root, "SESSION-ARCHIVE-DETAIL", at)
	archiveHTTPSession(t, handler, seeded.SessionID, seeded.Version, at.Add(time.Minute), "CMD-ARCHIVE-DETAIL-SEED")

	detail := httptest.NewRecorder()
	handler.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/v1/interactions/"+seeded.SessionID, nil))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"archived":true`) {
		t.Fatalf("archived detail = %d %s", detail.Code, detail.Body.String())
	}

	conversation := httptest.NewRecorder()
	handler.ServeHTTP(conversation, httptest.NewRequest(http.MethodGet, "/v1/interactions/"+seeded.SessionID+"/conversation", nil))
	if conversation.Code != http.StatusOK {
		t.Fatalf("archived conversation = %d %s", conversation.Code, conversation.Body.String())
	}

	workReport := httptest.NewRecorder()
	handler.ServeHTTP(workReport, httptest.NewRequest(http.MethodGet, "/v1/interactions/"+seeded.SessionID+"/work-report", nil))
	if workReport.Code != http.StatusOK {
		t.Fatalf("archived work-report = %d %s", workReport.Code, workReport.Body.String())
	}
}

func newArchiveTestHandler(t *testing.T, root string) *Handler {
	t.Helper()
	executor, err := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(executor, executor)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func setupWorkspaceHTTP(t *testing.T, handler *Handler, at time.Time) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"version": ContractVersion, "command_id": "CMD-ARCHIVE-WORKSPACE-SETUP", "operation": "workspace.setup", "approved": true,
		"payload": map[string]any{"current_time": at},
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/commands", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("workspace.setup seed = %d %s", response.Code, response.Body.String())
	}
}

func archiveHTTPSession(t *testing.T, handler *Handler, sessionID string, expectedVersion uint64, at time.Time, commandID string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"version": ContractVersion, "command_id": commandID, "operation": "interaction.archive", "approved": true,
		"payload": map[string]any{"session_id": sessionID, "expected_version": expectedVersion, "current_time": at},
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/commands", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("archive seed Command %s = %d %s", commandID, response.Code, response.Body.String())
	}
}

func seedArchiveHTTPSession(t *testing.T, root, sessionID string, at time.Time) interaction.Record {
	t.Helper()
	record, err := interaction.New(sessionID, "アーカイブHTTP確認用の依頼", "Claude Sonnet 5", at)
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
