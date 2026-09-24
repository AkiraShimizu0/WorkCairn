package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/event"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/recovery"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/task"
)

func completeTaskRecoveryFixture(t *testing.T) (string, CompleteTaskRecoveryPrepareView) {
	t.Helper()
	root := writePlanVault(t)
	startRecoveryTask(t, root)
	writeRecoveryDeliverable(t, root)
	prepared, err := PrepareCompleteTaskRecovery(context.Background(), RecoveryInput{VaultRoot: root, ProjectName: "ToDoアプリ"}, "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	return root, prepared
}

func applyInput(root, commandID string, prepared CompleteTaskRecoveryPrepareView) CompleteTaskRecoveryApplyInput {
	return CompleteTaskRecoveryApplyInput{
		VaultRoot: root, ProjectName: prepared.ProjectName, TaskID: prepared.TaskID,
		CommandID: commandID, Approval: prepared.Approval,
	}
}

func TestCompleteTaskRecoveryPrepareAndApplyUseTypedCommitmentAndSafeReplay(t *testing.T) {
	root, prepared := completeTaskRecoveryFixture(t)
	if prepared.Approval.Domain != CompleteTaskPlanCommitmentDomain || prepared.Approval.PlanCommitment == "" ||
		prepared.Action != recovery.ActionCompleteTask || !prepared.ApprovalRequired {
		t.Fatalf("prepared = %#v", prepared)
	}

	input := applyInput(root, "RECOVERY-APPLY-001", prepared)
	result, err := ExecuteCompleteTaskRecoveryApply(context.Background(), input, true)
	if err != nil {
		t.Fatal(err)
	}
	want := CompleteTaskRecoveryResult{
		SchemaVersion: recovery.SchemaVersion, ProjectName: "ToDoアプリ", TaskID: "TASK-001",
		Action: recovery.ActionCompleteTask, Status: "completed", TaskStateCommitted: true,
		TaskVersion: prepared.TaskVersion + 1,
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	replayed, err := ExecuteCompleteTaskRecoveryApply(context.Background(), input, true)
	if err != nil || !reflect.DeepEqual(replayed, result) {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil || len(fields) != 7 {
		t.Fatalf("safe result = %s, fields=%d, err=%v", encoded, len(fields), err)
	}
	for _, forbidden := range []string{"evidence", "source_revision", "last_failure", "title", "assignee"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("safe result leaked %q: %s", forbidden, encoded)
		}
	}
	ledger, err := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	if err != nil {
		t.Fatal(err)
	}
	record, err := ledger.Get(context.Background(), input.CommandID)
	var storedResult CompleteTaskRecoveryResult
	decodeErr := json.Unmarshal(record.Result, &storedResult)
	if err != nil || decodeErr != nil || record.State != commandledger.StateSucceeded || !reflect.DeepEqual(storedResult, result) {
		t.Fatalf("record = %#v, err=%v", record, err)
	}
}

func TestCompleteTaskRecoveryApplyClosesSameIDConflictAndRunning(t *testing.T) {
	root, prepared := completeTaskRecoveryFixture(t)
	input := applyInput(root, "RECOVERY-APPLY-CONFLICT", prepared)
	if _, err := ExecuteCompleteTaskRecoveryApply(context.Background(), input, true); err != nil {
		t.Fatal(err)
	}
	conflict := input
	conflict.Approval.PlanCommitment = "sha256:" + strings.Repeat("0", 64)
	if _, err := ExecuteCompleteTaskRecoveryApply(context.Background(), conflict, true); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("conflict error = %v", err)
	}

	root, prepared = completeTaskRecoveryFixture(t)
	input = applyInput(root, "RECOVERY-APPLY-RUNNING", prepared)
	entered := make(chan struct{})
	release := make(chan struct{})
	input.EventObservers = []event.Observer{{
		Types: []event.Type{event.TaskCompleted},
		Handler: func(context.Context, event.Event) error {
			close(entered)
			<-release
			return nil
		},
	}}
	done := make(chan error, 1)
	go func() {
		_, err := ExecuteCompleteTaskRecoveryApply(context.Background(), input, true)
		done <- err
	}()
	<-entered
	if _, err := ExecuteCompleteTaskRecoveryApply(context.Background(), input, true); !errors.Is(err, commandledger.ErrInProgress) {
		t.Fatalf("running error = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCompleteTaskRecoveryApplyDifferentCommandIDsHaveAtMostOneEffect(t *testing.T) {
	root, prepared := completeTaskRecoveryFixture(t)
	inputs := []CompleteTaskRecoveryApplyInput{
		applyInput(root, "RECOVERY-APPLY-RACE-A", prepared),
		applyInput(root, "RECOVERY-APPLY-RACE-B", prepared),
	}
	var wait sync.WaitGroup
	wait.Add(len(inputs))
	results := make([]CompleteTaskRecoveryResult, len(inputs))
	errs := make([]error, len(inputs))
	for index := range inputs {
		go func(index int) {
			defer wait.Done()
			results[index], errs[index] = ExecuteCompleteTaskRecoveryApply(context.Background(), inputs[index], true)
		}(index)
	}
	wait.Wait()
	committed := 0
	for index := range results {
		if errs[index] == nil && results[index].TaskStateCommitted {
			committed++
		}
	}
	if committed != 1 {
		t.Fatalf("committed successes = %d; results=%#v errors=%v", committed, results, errs)
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(context.Background(), "TASK-001")
	if err != nil || stored.Status != task.StatusCompleted || stored.Version != prepared.TaskVersion+1 {
		t.Fatalf("stored Task = %#v, %v", stored, err)
	}
}

func TestCompleteTaskRecoveryApplyRederivesFreshCanonicalPlan(t *testing.T) {
	root, prepared := completeTaskRecoveryFixture(t)
	deliverable := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Deliverables", "TASK-001.md")
	content, err := os.ReadFile(deliverable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deliverable, append(content, []byte("\nchanged after approval\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := ExecuteCompleteTaskRecoveryApply(context.Background(), applyInput(root, "RECOVERY-APPLY-STALE", prepared), true)
	if !errors.Is(err, ErrRecoveryPlanStale) || result.TaskStateCommitted || result.Status != "failed" {
		t.Fatalf("stale result = %#v, err=%v", result, err)
	}
	store, storeErr := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	stored, getErr := store.Get(context.Background(), "TASK-001")
	if getErr != nil || stored.Status != task.StatusInProgress || stored.Version != prepared.TaskVersion {
		t.Fatalf("stale Apply changed Task: %#v, %v", stored, getErr)
	}
}

func TestCompleteTaskRecoveryApplyRecordsPartialEventFailureWithoutRawError(t *testing.T) {
	root, prepared := completeTaskRecoveryFixture(t)
	input := applyInput(root, "RECOVERY-APPLY-PARTIAL", prepared)
	input.EventObservers = []event.Observer{{
		Types: []event.Type{event.TaskCompleted},
		Handler: func(context.Context, event.Event) error {
			return errors.New("private /Vault/path must not persist")
		},
	}}
	result, err := ExecuteCompleteTaskRecoveryApply(context.Background(), input, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || !recorded.Partial || result.Status != "partial_failure" || !result.TaskStateCommitted {
		t.Fatalf("partial result = %#v, err=%v", result, err)
	}
	ledger, _ := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	record, getErr := ledger.Get(context.Background(), input.CommandID)
	encoded, _ := json.Marshal(record)
	if getErr != nil || record.State != commandledger.StatePartialFailure || strings.Contains(string(encoded), "/Vault/path") {
		t.Fatalf("partial record = %s, err=%v", encoded, getErr)
	}
}

func TestCompleteTaskRecoveryApplyPersistsTerminalOutcomeAfterRequestCancellation(t *testing.T) {
	root, prepared := completeTaskRecoveryFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	input := applyInput(root, "RECOVERY-APPLY-CANCELED", prepared)
	input.EventObservers = []event.Observer{{
		Types: []event.Type{event.TaskCompleted},
		Handler: func(context.Context, event.Event) error {
			cancel()
			return context.Canceled
		},
	}}
	result, err := ExecuteCompleteTaskRecoveryApply(ctx, input, true)
	if err == nil || result.Status != "partial_failure" || !result.TaskStateCommitted {
		t.Fatalf("canceled result = %#v, err=%v", result, err)
	}
	ledger, _ := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	record, getErr := ledger.Get(context.Background(), input.CommandID)
	if getErr != nil || record.State != commandledger.StatePartialFailure || record.Version != 2 {
		t.Fatalf("canceled terminal record = %#v, err=%v", record, getErr)
	}
}

func TestCompleteTaskRecoveryApplyRecordsPartialAuditFailure(t *testing.T) {
	root, prepared := completeTaskRecoveryFixture(t)
	auditLockPath := filepath.Join(root, "プロジェクト", "ToDoアプリ", ".workspace-os-audit.lock")
	if err := os.Mkdir(auditLockPath, 0o755); err != nil {
		t.Fatal(err)
	}
	input := applyInput(root, "RECOVERY-APPLY-AUDIT-PARTIAL", prepared)
	result, err := ExecuteCompleteTaskRecoveryApply(context.Background(), input, true)
	var recorded *RecordedCommandError
	if !errors.As(err, &recorded) || !recorded.Partial || result.Status != "partial_failure" || !result.TaskStateCommitted {
		t.Fatalf("audit partial result = %#v, err=%v", result, err)
	}
	ledger, _ := vault.NewCommandLedgerStore(root, "ToDoアプリ")
	record, getErr := ledger.Get(context.Background(), input.CommandID)
	if getErr != nil || record.State != commandledger.StatePartialFailure || record.Failure == nil || record.Failure.Code != "RECOVERY_APPLY_PARTIAL" {
		t.Fatalf("audit partial record = %#v, err=%v", record, getErr)
	}
}

func TestCompleteTaskRecoveryApplyDoesNotHideLedgerFinishFailure(t *testing.T) {
	root, prepared := completeTaskRecoveryFixture(t)
	input := applyInput(root, "RECOVERY-APPLY-LEDGER-FAIL", prepared)
	input.EventObservers = []event.Observer{{
		Types: []event.Type{event.TaskCompleted},
		Handler: func(context.Context, event.Event) error {
			directory := filepath.Join(root, "プロジェクト", "ToDoアプリ", ".workspace-os", "commands")
			entries, err := os.ReadDir(directory)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if filepath.Ext(entry.Name()) == ".json" {
					return os.Remove(filepath.Join(directory, entry.Name()))
				}
			}
			return errors.New("running Ledger record not found")
		},
	}}
	result, err := ExecuteCompleteTaskRecoveryApply(context.Background(), input, true)
	if !errors.Is(err, ErrCommandLedgerCommit) || result.Status != "completed" || !result.TaskStateCommitted {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
}

func TestCompleteTaskPlanCommitmentCopiesEveryPlanField(t *testing.T) {
	root, _ := completeTaskRecoveryFixture(t)
	plan, err := PlanTaskRecovery(context.Background(), RecoveryInput{VaultRoot: root, ProjectName: "ToDoアプリ"}, recovery.PlanRequest{TaskID: "TASK-001", Action: recovery.ActionCompleteTask})
	if err != nil {
		t.Fatal(err)
	}
	commitment, err := NewCompleteTaskPlanCommitmentV1(plan)
	if err != nil {
		t.Fatal(err)
	}
	want := CompleteTaskPlanCommitmentV1{
		SchemaVersion: plan.SchemaVersion, ProjectName: plan.ProjectName, TaskID: plan.TaskID,
		Action: plan.Action, ExpectedStatus: plan.ExpectedStatus, ExpectedVersion: plan.ExpectedVersion,
		EvidenceRef: plan.EvidenceRef, EvidenceDigest: plan.EvidenceDigest, Reason: plan.Reason,
		SourceRevision: plan.SourceRevision, Executable: plan.Executable,
		BlockingReasons: append([]string(nil), plan.BlockingReasons...), ApprovalRequired: plan.ApprovalRequired,
	}
	if !reflect.DeepEqual(commitment, want) {
		t.Fatalf("commitment = %#v, want %#v", commitment, want)
	}
	planType := reflect.TypeOf(plan)
	commitmentType := reflect.TypeOf(commitment)
	commitmentFields := make(map[string]reflect.Type, commitmentType.NumField())
	for index := 0; index < commitmentType.NumField(); index++ {
		field := commitmentType.Field(index)
		commitmentFields[strings.Split(field.Tag.Get("json"), ",")[0]] = field.Type
	}
	if len(commitmentFields) != planType.NumField() {
		t.Fatalf("commitment field count = %d, Plan field count = %d", len(commitmentFields), planType.NumField())
	}
	for index := 0; index < planType.NumField(); index++ {
		field := planType.Field(index)
		jsonName := strings.Split(field.Tag.Get("json"), ",")[0]
		if commitmentFields[jsonName] != field.Type {
			t.Fatalf("Plan field %q (%v) is not explicitly represented with the same type", jsonName, field.Type)
		}
	}
}
