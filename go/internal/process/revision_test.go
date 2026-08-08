package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/commandledger"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

func TestPlanRevisionIsReadOnlyAndUsesCanonicalReview(t *testing.T) {
	root := revisionProcessVault(t, `{"verdict":"Request Changes","issues":[{"category":"requirements","severity":"medium","description":"要件が不足しています。","suggested_action":"要件を追記してください。"}]}`)
	before := planVaultSnapshot(t, root)
	plan, err := PlanRevision(context.Background(), revisionPlanInput(root))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Executable || !plan.ApprovalRequired || plan.RevisionTaskID != "TASK-002" ||
		plan.AssigneeID != "PLAN-001" || plan.SourceReviewCanonical != "Reviews/TASK-001.review.json" ||
		plan.IntentPath != "Revisions/TASK-002.revision.md" || len(plan.ReviewDecision.Issues) != 1 {
		t.Fatalf("PlanRevision() = %#v", plan)
	}
	if after := planVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("Revision plan changed temporary Vault")
	}
}

func TestExecuteRevisionRequiresApprovalBeforeEffects(t *testing.T) {
	root := revisionProcessVault(t, `{"verdict":"Request Changes","issues":[{"category":"requirements","severity":"medium","description":"要件不足","suggested_action":"追記する"}]}`)
	before := planVaultSnapshot(t, root)
	result, err := ExecuteRevision(context.Background(), ExecuteRevisionInput{RevisionPlanInput: revisionPlanInput(root)})
	if !errors.Is(err, ErrRevisionApprovalRequired) || result.Status != "" {
		t.Fatalf("ExecuteRevision() = %#v, %v", result, err)
	}
	if after := planVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("unapproved Revision changed temporary Vault")
	}
}

func TestExecuteRevisionCommitsIntentTaskEventsAndAudit(t *testing.T) {
	root := revisionProcessVault(t, `{"verdict":"Request Changes","issues":[{"category":"requirements","severity":"medium","description":"要件が不足しています。","suggested_action":"要件を追記してください。"}]}`)
	result, err := ExecuteRevision(context.Background(), ExecuteRevisionInput{
		RevisionPlanInput: revisionPlanInput(root), Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "created" || result.Intent == nil || !result.Intent.Committed ||
		result.Task == nil || result.Task.ID != "TASK-002" || result.Task.Status != task.StatusUnstarted ||
		!result.EventPublished || result.EventID == "" {
		t.Fatalf("ExecuteRevision() = %#v", result)
	}
	project := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	intent, err := os.ReadFile(filepath.Join(project, "Revisions", "TASK-002.revision.md"))
	if err != nil || !strings.Contains(string(intent), "state: intent_committed") ||
		!strings.Contains(string(intent), "source_review_canonical: Reviews/TASK-001.review.json") {
		t.Fatalf("intent=%s err=%v", intent, err)
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Get(context.Background(), "TASK-002")
	if err != nil || created.AssigneeID == nil || *created.AssigneeID != "PLAN-001" || created.Version != 1 {
		t.Fatalf("created Task=%#v err=%v", created, err)
	}
	audit, err := os.ReadFile(filepath.Join(project, "Audit Log.md"))
	if err != nil {
		t.Fatal(err)
	}
	taskCreated := strings.LastIndex(string(audit), "task.created")
	revisionCreated := strings.LastIndex(string(audit), "revision.created")
	if taskCreated < 0 || revisionCreated <= taskCreated || !strings.Contains(string(audit), result.EventID) {
		t.Fatalf("Revision Audit order is invalid:\n%s", audit)
	}
	second, err := ExecuteRevision(context.Background(), ExecuteRevisionInput{
		RevisionPlanInput: revisionPlanInput(root), Approved: true,
	})
	var preflightError *RevisionPreflightError
	if !errors.As(err, &preflightError) || second.Status != "" || !containsRevisionReason(preflightError.Plan.BlockingReasons, "revision_for_review_already_exists") {
		t.Fatalf("duplicate ExecuteRevision() = %#v, %v", second, err)
	}
}

func TestExecuteRevisionCommandReplayAndConflict(t *testing.T) {
	root := revisionProcessVault(t, `{"verdict":"Request Changes","issues":[{"category":"requirements","severity":"medium","description":"要件不足","suggested_action":"追記する"}]}`)
	input := ExecuteRevisionInput{RevisionPlanInput: revisionPlanInput(root), Approved: true, CommandID: "CMD-REVISION-001"}
	first, err := ExecuteRevision(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	beforeReplay := planVaultSnapshot(t, root)
	second, err := ExecuteRevision(context.Background(), input)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("replay = %#v, %v; first = %#v", second, err, first)
	}
	if after := planVaultSnapshot(t, root); !reflect.DeepEqual(beforeReplay, after) {
		t.Fatal("Revision replay changed Vault")
	}
	input.ReviewVersion = "v2"
	if _, err := ExecuteRevision(context.Background(), input); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("conflicting Revision command error = %v", err)
	}
}

func TestPlanRevisionRejectsApproveReview(t *testing.T) {
	root := revisionProcessVault(t, `{"verdict":"Approve","issues":[]}`)
	plan, err := PlanRevision(context.Background(), revisionPlanInput(root))
	if err != nil || plan.Executable || !containsRevisionReason(plan.BlockingReasons, "review_does_not_request_changes") {
		t.Fatalf("PlanRevision() = %#v, %v", plan, err)
	}
}

func revisionProcessVault(t *testing.T, canonical string) string {
	t.Helper()
	root := writeReviewProcessVault(t)
	completeReviewSourceTask(t, root)
	directory := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Reviews")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	writePlanFile(t, filepath.Join(directory, "TASK-001.review.json"), canonical+"\n")
	writePlanFile(t, filepath.Join(directory, "TASK-001.review.md"), "---\ntype: review\nproject: ToDoアプリ\ntask_id: TASK-001\n---\n")
	return root
}

func revisionPlanInput(root string) RevisionPlanInput {
	return RevisionPlanInput{
		VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", SourceTaskID: "TASK-001",
		CurrentTime: time.Date(2026, time.August, 6, 18, 0, 0, 0, time.FixedZone("JST", 9*60*60)),
	}
}

func containsRevisionReason(reasons []string, expected string) bool {
	return strings.Contains(strings.Join(reasons, "\n"), expected)
}
