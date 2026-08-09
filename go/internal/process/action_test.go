package process

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/action"
	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
	"github.com/AkiraShimizu0/workspace-os/go/internal/deliverable"
	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
)

func TestExternalActionPlanApprovalPublishEvidenceEventAndReplay(t *testing.T) {
	root := writeActionVault(t)
	input := actionPlanInput(root, "CMD-ACTION-001")
	beforePlan := planVaultSnapshot(t, root)
	plan, err := PlanExternalAction(context.Background(), input)
	if err != nil || !plan.Executable || !plan.ApprovalRequired || plan.Intent.Source.Title != "公開記事" || plan.Intent.Source.Content != "本文\n\n## 詳細" {
		t.Fatalf("PlanExternalAction() = %#v, %v", plan, err)
	}
	if afterPlan := planVaultSnapshot(t, root); !reflect.DeepEqual(beforePlan, afterPlan) {
		t.Fatal("Action plan changed temporary Vault")
	}
	input.ExpectedSourceSHA256 = plan.Intent.Source.SHA256

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		var payload map[string]string
		_ = json.NewDecoder(request.Body).Decode(&payload)
		if payload["title"] != "公開記事" || payload["content"] != "本文\n\n## 詳細" || payload["status"] != "publish" {
			t.Errorf("WordPress payload = %#v", payload)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":42,"link":"https://example.test/posts/42","status":"publish"}`))
	}))
	defer server.Close()
	config := WordPressProcessConfig{TargetID: "site-main", BaseURL: server.URL, Username: "fake-user", ApplicationPassword: "fake-password"}

	beforeApproval := planVaultSnapshot(t, root)
	if _, err := ExecuteExternalAction(context.Background(), ExecuteActionInput{ActionPlanInput: input}, config, server.Client()); !errors.Is(err, ErrActionApprovalRequired) || calls.Load() != 0 {
		t.Fatalf("unapproved ExecuteExternalAction() error = %v calls=%d", err, calls.Load())
	}
	if afterApproval := planVaultSnapshot(t, root); !reflect.DeepEqual(beforeApproval, afterApproval) {
		t.Fatal("unapproved Action changed temporary Vault")
	}

	notifications, err := vault.NewNotificationSubscriber(root)
	if err != nil {
		t.Fatal(err)
	}
	executeInput := ExecuteActionInput{
		ActionPlanInput: input, Approved: true,
		EventObservers: []event.Observer{{Types: []event.Type{event.ActionCompleted}, Handler: notifications.Handler()}},
	}
	result, err := ExecuteExternalAction(context.Background(), executeInput, config, server.Client())
	if err != nil || result.Status != "published" || result.Intent == nil || !result.Intent.Committed || result.Outcome == nil || !result.Outcome.Committed || result.Publication == nil || result.Publication.ExternalID != "42" || !result.EventPublished {
		t.Fatalf("ExecuteExternalAction() = %#v, %v", result, err)
	}
	project := filepath.Join(root, "プロジェクト", "記事案件")
	for _, evidence := range []*action.Evidence{result.Intent, result.Outcome} {
		if _, err := os.Stat(filepath.Join(project, filepath.FromSlash(evidence.RelativePath))); err != nil {
			t.Fatalf("Action evidence missing: %v", err)
		}
	}
	audit, err := os.ReadFile(filepath.Join(project, "Audit Log.md"))
	if err != nil || !strings.Contains(string(audit), "action.completed") || !strings.Contains(string(audit), result.EventID) {
		t.Fatalf("Action Audit = %v %s", err, audit)
	}
	notificationRecords, err := notifications.List(context.Background())
	if err != nil || len(notificationRecords) != 1 || notificationRecords[0].EventType != event.ActionCompleted {
		t.Fatalf("Action Notifications = %#v, %v", notificationRecords, err)
	}

	beforeReplay := planVaultSnapshot(t, root)
	replayed, err := ExecuteExternalAction(context.Background(), executeInput, config, server.Client())
	if err != nil || !reflect.DeepEqual(replayed, result) || calls.Load() != 1 || !reflect.DeepEqual(beforeReplay, planVaultSnapshot(t, root)) {
		t.Fatalf("Action replay = %#v, %v calls=%d", replayed, err, calls.Load())
	}
	conflict := executeInput
	conflict.TargetID = "site-other"
	if _, err := ExecuteExternalAction(context.Background(), conflict, config, server.Client()); !errors.Is(err, commandledger.ErrRequestConflict) || calls.Load() != 1 {
		t.Fatalf("Action conflict = %v calls=%d", err, calls.Load())
	}
}

func TestExternalActionProviderFailureKeepsIntentAndDoesNotAutoRetry(t *testing.T) {
	root := writeActionVault(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	planInput := actionPlanInput(root, "CMD-ACTION-FAIL-001")
	plan, err := PlanExternalAction(context.Background(), planInput)
	if err != nil {
		t.Fatal(err)
	}
	planInput.ExpectedSourceSHA256 = plan.Intent.Source.SHA256
	input := ExecuteActionInput{ActionPlanInput: planInput, Approved: true}
	config := WordPressProcessConfig{TargetID: "site-main", BaseURL: server.URL, Username: "fake-user", ApplicationPassword: "fake-password"}
	result, err := ExecuteExternalAction(context.Background(), input, config, server.Client())
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || !recorded.Partial || recorded.Stage != "action_publish" || result.Intent == nil || !result.Intent.Committed || result.Publication != nil || result.Outcome != nil || calls.Load() != 1 {
		t.Fatalf("failed Action = %#v, %v calls=%d", result, err, calls.Load())
	}
	replayed, err := ExecuteExternalAction(context.Background(), input, config, server.Client())
	if !errors.As(err, &recorded) || !reflect.DeepEqual(replayed, result) || calls.Load() != 1 {
		t.Fatalf("failed Action replay = %#v, %v calls=%d", replayed, err, calls.Load())
	}
}

func TestExternalActionRejectsStaleApprovedSourceBeforeEvidenceOrProvider(t *testing.T) {
	root := writeActionVault(t)
	input := actionPlanInput(root, "CMD-ACTION-STALE-001")
	input.ExpectedSourceSHA256 = strings.Repeat("b", 64)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	result, err := ExecuteExternalAction(context.Background(), ExecuteActionInput{ActionPlanInput: input, Approved: true}, WordPressProcessConfig{
		TargetID: "site-main", BaseURL: server.URL, Username: "u", ApplicationPassword: "p",
	}, server.Client())
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || recorded.Code != "ACTION_PREFLIGHT_FAILED" || result.Intent != nil || calls.Load() != 0 {
		t.Fatalf("stale Action = %#v, %v calls=%d", result, err, calls.Load())
	}
	if _, statErr := os.Stat(filepath.Join(root, "プロジェクト", "記事案件", ".workspace-os", "actions")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stale Action created evidence directory: %v", statErr)
	}
}

func writeActionVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "プロジェクト", "記事案件")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := vault.NewDeliverableStore(root, "記事案件")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Save(context.Background(), deliverable.Document{
		ProjectID: "PROJECT-001", ProjectName: "記事案件", TaskTitle: "公開記事",
		ExecutedAt: time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC),
		Execution: worker.ExecutionResult{
			Content: "本文\n\n## 詳細", EmployeeID: "WRITER-001", TaskID: "TASK-001",
			Runner: "fake", Model: "fake-model", Status: worker.StatusCompleted,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func actionPlanInput(root, commandID string) ActionPlanInput {
	return ActionPlanInput{
		VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "記事案件", TaskID: "TASK-001",
		TargetID: "site-main", CurrentTime: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC), CommandID: commandID,
	}
}
