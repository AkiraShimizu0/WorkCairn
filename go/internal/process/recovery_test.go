package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/recovery"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

func TestTaskRecoveryCompletesCommittedDeliverableThroughTaskService(t *testing.T) {
	root := writePlanVault(t)
	started := startRecoveryTask(t, root)
	writeRecoveryDeliverable(t, root)
	input := RecoveryInput{VaultRoot: root, ProjectName: "ToDoアプリ"}

	report, err := InspectRecovery(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRecoveryFinding(report, recovery.FindingTaskCompletionPending, "TASK-001") {
		t.Fatalf("InspectRecovery() = %#v", report)
	}
	plan, err := PlanTaskRecovery(context.Background(), input, recovery.PlanRequest{
		TaskID: "TASK-001", Action: recovery.ActionCompleteTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Executable || plan.ExpectedVersion != started.Version || plan.EvidenceDigest == "" || !plan.ApprovalRequired {
		t.Fatalf("PlanTaskRecovery() = %#v", plan)
	}

	before := planVaultSnapshot(t, root)
	if result, err := ExecuteTaskRecovery(context.Background(), input, plan, false); !errors.Is(err, ErrRecoveryApprovalRequired) || !reflect.DeepEqual(result, recovery.Result{}) {
		t.Fatalf("unapproved ExecuteTaskRecovery() = %#v, %v", result, err)
	}
	if after := planVaultSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("unapproved recovery changed the temporary Vault")
	}

	result, err := ExecuteTaskRecovery(context.Background(), input, plan, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.Task == nil || result.Task.Status != task.StatusCompleted || result.Task.Version != started.Version+1 {
		t.Fatalf("ExecuteTaskRecovery() = %#v", result)
	}
	audit, err := os.ReadFile(filepath.Join(root, "プロジェクト", "ToDoアプリ", "Audit Log.md"))
	if err != nil || !strings.Contains(string(audit), `"type": "task.completed"`) {
		t.Fatalf("recovery Audit = %q, %v", audit, err)
	}
}

func TestTaskRecoveryFailsAndHoldsInterruptedExecution(t *testing.T) {
	root := writePlanVault(t)
	started := startRecoveryTask(t, root)
	input := RecoveryInput{VaultRoot: root, ProjectName: "ToDoアプリ"}
	plan, err := PlanTaskRecovery(context.Background(), input, recovery.PlanRequest{
		TaskID: "TASK-001", Action: recovery.ActionFailAndHold, Reason: "process stopped before Deliverable commit",
	})
	if err != nil || !plan.Executable {
		t.Fatalf("PlanTaskRecovery() = %#v, %v", plan, err)
	}
	result, err := ExecuteTaskRecovery(context.Background(), input, plan, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "held" || !result.FailureCommitted || !result.HoldCommitted || result.Task == nil ||
		result.Task.Status != task.StatusOnHold || result.Task.Version != started.Version+2 ||
		result.Task.LastFailureReason != plan.Reason || result.Task.HoldReason != "manual recovery: "+plan.Reason {
		t.Fatalf("ExecuteTaskRecovery() = %#v", result)
	}
}

func TestTaskRecoveryRejectsEvidenceOrVersionChangedAfterPlan(t *testing.T) {
	root := writePlanVault(t)
	started := startRecoveryTask(t, root)
	writeRecoveryDeliverable(t, root)
	input := RecoveryInput{VaultRoot: root, ProjectName: "ToDoアプリ"}
	plan, err := PlanTaskRecovery(context.Background(), input, recovery.PlanRequest{TaskID: "TASK-001", Action: recovery.ActionCompleteTask})
	if err != nil {
		t.Fatal(err)
	}

	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := started.Fail("concurrent operator decision")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), failed, started.Version); err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteTaskRecovery(context.Background(), input, plan, true)
	if !errors.Is(err, ErrRecoveryPlanStale) || !reflect.DeepEqual(result, recovery.Result{}) {
		t.Fatalf("ExecuteTaskRecovery() = %#v, %v", result, err)
	}
	stored, getErr := store.Get(context.Background(), "TASK-001")
	if getErr != nil || stored.Version != failed.Version || stored.Status != task.StatusInProgress || stored.LastFailureReason != "concurrent operator decision" {
		t.Fatalf("stale recovery changed Task: %#v, %v", stored, getErr)
	}
}

func startRecoveryTask(t *testing.T, root string) task.Task {
	t.Helper()
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Get(context.Background(), "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	started, err := current.Start()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), started, current.Version); err != nil {
		t.Fatal(err)
	}
	return started
}

func writeRecoveryDeliverable(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables", "TASK-001.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writePlanFile(t, path, "---\ntype: task-deliverable\nproject: ToDoアプリ\ntask_id: TASK-001\nassignee_id: PLAN-001\n---\n\n# immutable result\n")
}

func hasRecoveryFinding(report recovery.Report, kind recovery.FindingKind, taskID string) bool {
	for _, finding := range report.Findings {
		if finding.Kind == kind && finding.TaskID == taskID {
			return true
		}
	}
	return false
}
