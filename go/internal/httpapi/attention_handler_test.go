package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	workspaceprocess "github.com/AkiraShimizu0/WorkCairn/go/internal/process"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/responsibility"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/routine"
)

// TestGetAttentionReturnsRoutineRecoveryItemThroughRealHandler is the one
// HTTP-level proof that GET /v1/attention is really wired end to end
// through the real ProcessExecutor -- the same read-only aggregation
// exercised directly in internal/process's own attention_test.go, reached
// here via the actual HTTP route.
func TestGetAttentionReturnsRoutineRecoveryItemThroughRealHandler(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	executor, err := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(executor, executor)
	if err != nil {
		t.Fatal(err)
	}

	createdAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if _, err := workspaceprocess.ExecuteResponsibilityCreate(context.Background(), workspaceprocess.ResponsibilityCreateInput{
		VaultRoot: root, ResponsibilityID: "RESP-1", Scope: responsibility.ScopeCompany, Title: "Continuously improve onboarding",
		CurrentTime: createdAt, CommandID: "CMD-RESP-1",
	}, true); err != nil {
		t.Fatal(err)
	}
	created, err := workspaceprocess.ExecuteRoutineCreate(context.Background(), workspaceprocess.RoutineCreateInput{
		VaultRoot: root, RoutineID: "ROUTINE-1", Scope: routine.ScopeCompany, ResponsibilityID: "RESP-1",
		Instruction: "今週の改善項目を計画する", Model: "Claude Sonnet 5",
		Trigger:     routine.Trigger{Cadence: routine.CadenceDaily, TimeOfDayUTC: 9 * time.Hour},
		CurrentTime: createdAt, CommandID: "CMD-ROUTINE-1",
	}, true)
	if err != nil {
		t.Fatal(err)
	}

	// Before activation: no Routine-sourced item exists yet.
	before := httptest.NewRequest(http.MethodGet, "/v1/attention", nil)
	beforeResponse := httptest.NewRecorder()
	handler.ServeHTTP(beforeResponse, before)
	if beforeResponse.Code != http.StatusOK || bytes.Contains(beforeResponse.Body.Bytes(), []byte(`"routine_recovery_required"`)) {
		t.Fatalf("GET /v1/attention before activation = %d %s", beforeResponse.Code, beforeResponse.Body.String())
	}

	// Simulate the exact durable state this needs: Active with no Schedule.
	store, err := vault.NewWorkspaceRoutineStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), created.RoutineID)
	if err != nil {
		t.Fatal(err)
	}
	active, err := record.Activate()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), active, record.Version); err != nil {
		t.Fatal(err)
	}

	after := httptest.NewRequest(http.MethodGet, "/v1/attention", nil)
	afterResponse := httptest.NewRecorder()
	handler.ServeHTTP(afterResponse, after)
	if afterResponse.Code != http.StatusOK ||
		!bytes.Contains(afterResponse.Body.Bytes(), []byte(`"type":"routine_recovery_required"`)) ||
		!bytes.Contains(afterResponse.Body.Bytes(), []byte(`"entity_id":"ROUTINE-1"`)) {
		t.Fatalf("GET /v1/attention after Active-but-unhealthy = %d %s", afterResponse.Code, afterResponse.Body.String())
	}
}
