package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	workspaceprocess "github.com/AkiraShimizu0/workcairn/go/internal/process"
	"github.com/AkiraShimizu0/workcairn/go/internal/responsibility"
	"github.com/AkiraShimizu0/workcairn/go/internal/routine"
	"github.com/AkiraShimizu0/workcairn/go/internal/scheduler"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

// TestSchedulerDispatchesRoutinePlanThroughRealProcessExecutor is the one
// genuine end-to-end proof that "routine.plan" is really wired into the
// same Scheduler -> Dispatcher pipeline every other schedulable operation
// already uses (ADR-0025): a real SchedulerService.RunDue tick, against a
// real ProcessExecutor (the same type workcairn-daemon actually
// constructs), dispatches the Schedule created by ExecuteRoutineActivate
// and reaches the fake Provider. Routine/Responsibility creation and
// activation themselves are called directly via workspaceprocess (they are
// operator-CLI/process-only, deliberately not wired into
// ProcessExecutor.Execute -- matching Goal/Responsibility's own "no
// workcairn-daemon HTTP allow-list change" scoping decision).
func TestSchedulerDispatchesRoutinePlanThroughRealProcessExecutor(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "社員"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "社員", "田中 美咲.md"), []byte("---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	intentOutput, _ := json.Marshal(map[string]any{
		"project_name": "オンボーディング改善", "objective": "新規ユーザーのオンボーディング体験を改善する",
		"summary": "今週の改善項目を計画する",
		"steps": []map[string]any{
			{"kind": "write", "description": "改善候補を整理する", "required_role": "Product Manager"},
		},
		"ceo_questions": []string{},
	})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"model": "claude-test", "content": []map[string]string{{"type": "text", "text": string(intentOutput)}},
			"usage": map[string]int{"input_tokens": 10, "output_tokens": 20},
		})
	}))
	defer server.Close()

	executor, err := NewProcessExecutor(root, workspaceprocess.ClaudeProcessConfig{
		APIKey: "fake-key", ProviderModel: "claude-test", BaseURL: server.URL,
	}, server.Client())
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
	activated, err := workspaceprocess.ExecuteRoutineActivate(context.Background(), workspaceprocess.RoutineTransitionInput{
		VaultRoot: root, RoutineID: created.RoutineID, Scope: routine.ScopeCompany, ExpectedVersion: created.Version,
		CommandID: "CMD-ACTIVATE-1", CurrentTime: createdAt,
	}, true)
	if err != nil || activated.NextScheduleID == "" {
		t.Fatalf("ExecuteRoutineActivate() = %#v, %v", activated, err)
	}

	store, err := vault.NewScheduleStore(root)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.Get(context.Background(), activated.NextScheduleID)
	if err != nil || pending.State != scheduler.StatePending {
		t.Fatalf("Schedule %s = %#v, %v, want Pending", activated.NextScheduleID, pending, err)
	}

	schedulerService, err := service.NewSchedulerService(store, executor, service.SchedulerConfig{PollInterval: time.Hour, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	run, err := schedulerService.RunDue(context.Background(), pending.DueAt)
	if err != nil || len(run.Records) != 1 || run.Records[0].State != scheduler.StateSucceeded {
		t.Fatalf("RunDue() = %#v, %v", run, err)
	}

	all, err := store.List(context.Background())
	if err != nil || len(all) != 2 {
		t.Fatalf("Schedules after dispatch = %#v, %v, want 2 (the dispatched one, now terminal, plus the freshly chained next occurrence)", all, err)
	}
}
