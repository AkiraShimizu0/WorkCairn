package process

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/attention"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/interaction"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/responsibility"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/routine"
)

func attentionVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// --- Routine sources ---

func TestInspectAttentionHealthyActiveRoutineProducesNoItem(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	if _, err := ExecuteRoutineActivate(context.Background(), RoutineTransitionInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, ExpectedVersion: 1, CommandID: "CMD-ACTIVATE-1", CurrentTime: at,
	}, true); err != nil {
		t.Fatal(err)
	}
	items, err := InspectAttention(context.Background(), root, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Type == attention.TypeRoutineRecoveryRequired {
			t.Fatalf("healthy Active Routine produced an item: %#v", item)
		}
	}
}

func TestInspectAttentionUnhealthyActiveRoutineProducesRecoveryItem(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	store, err := routineStoreFor(root, routine.ScopeCompany, "")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), routineID)
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
	now := time.Date(2026, 8, 26, 13, 0, 0, 0, time.UTC)
	items, err := InspectAttention(context.Background(), root, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %#v, want exactly 1", items)
	}
	item := items[0]
	if item.Type != attention.TypeRoutineRecoveryRequired || item.EntityType != attention.EntityRoutine ||
		item.EntityID != routineID || item.ResponsibilityID != "RESP-1" ||
		item.Action.Kind != attention.ActionReconcile || item.Action.Operation != "routine-reconcile" ||
		!item.ObservedAt.Equal(now) {
		t.Fatalf("item = %#v", item)
	}
}

func TestInspectAttentionInactiveRoutineProducesNoItem(t *testing.T) {
	root, _, _ := routinePlanFixture(t) // stays Inactive
	items, err := InspectAttention(context.Background(), root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %#v, want none for an Inactive Routine", items)
	}
}

func TestInspectAttentionReconcileMakesRoutineItemDisappear(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	store, err := routineStoreFor(root, routine.ScopeCompany, "")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), routineID)
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
	before, err := InspectAttention(context.Background(), root, time.Now())
	if err != nil || len(before) != 1 {
		t.Fatalf("before reconcile = %#v, %v, want exactly 1 item", before, err)
	}
	if _, err := ExecuteRoutineReconcile(context.Background(), RoutineReconcileInput{
		VaultRoot: root, RoutineID: routineID, Scope: routine.ScopeCompany, CurrentTime: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC), CommandID: "CMD-RECONCILE-1",
	}, true); err != nil {
		t.Fatal(err)
	}
	after, err := InspectAttention(context.Background(), root, time.Now())
	if err != nil || len(after) != 0 {
		t.Fatalf("after reconcile = %#v, %v, want no items", after, err)
	}
}

// --- Interaction sources ---

func newAttentionSession(t *testing.T, root, sessionID string, at time.Time) interaction.Record {
	t.Helper()
	store, err := vault.NewInteractionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := interaction.New(sessionID, "オンボーディングを改善したい", "Claude Sonnet 5", at)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return record
}

func updateAttentionSession(t *testing.T, root string, next interaction.Record, expectedVersion uint64) interaction.Record {
	t.Helper()
	store, err := vault.NewInteractionStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), next, expectedVersion); err != nil {
		t.Fatal(err)
	}
	return next
}

func TestInspectAttentionPlanGenerationApprovalRequiredProducesApprovalItem(t *testing.T) {
	root := attentionVault(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	session := newAttentionSession(t, root, "SESSION-1", at)
	items, err := InspectAttention(context.Background(), root, at)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %#v, %v, want exactly 1", items, err)
	}
	item := items[0]
	if item.Type != attention.TypeApprovalRequired || item.EntityType != attention.EntityInteraction ||
		item.EntityID != session.SessionID || item.Action.Kind != attention.ActionApprove ||
		item.Action.Operation != "interaction.plan.generate" || !item.ObservedAt.Equal(at) {
		t.Fatalf("item = %#v", item)
	}
}

func TestInspectAttentionClarificationRequiredProducesHumanInputItem(t *testing.T) {
	root := attentionVault(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	session := newAttentionSession(t, root, "SESSION-1", at)
	fixture := loadCEOPlanFixture(t)
	plan := fixture.ExpectedPlan
	plan.CEOQuestions = []string{"どのUIを優先しますか？"}
	withPlan, err := session.RecordPlan(plan, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	session = updateAttentionSession(t, root, withPlan, session.Version)
	if session.State != interaction.StateClarificationRequired {
		t.Fatalf("session.State = %v, want StateClarificationRequired", session.State)
	}
	items, err := InspectAttention(context.Background(), root, at)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %#v, %v, want exactly 1", items, err)
	}
	item := items[0]
	if item.Type != attention.TypeHumanInputRequired || item.Action.Kind != attention.ActionAnswer ||
		item.Action.Operation != "interaction.answer" {
		t.Fatalf("item = %#v", item)
	}
}

func TestInspectAttentionPlanApprovalRequiredProducesApprovalItem(t *testing.T) {
	root := attentionVault(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	session := newAttentionSession(t, root, "SESSION-1", at)
	fixture := loadCEOPlanFixture(t)
	plan := fixture.ExpectedPlan
	plan.CEOQuestions = []string{}
	withPlan, err := session.RecordPlan(plan, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	session = updateAttentionSession(t, root, withPlan, session.Version)
	if session.State != interaction.StatePlanApprovalRequired {
		t.Fatalf("session.State = %v, want StatePlanApprovalRequired", session.State)
	}
	items, err := InspectAttention(context.Background(), root, at)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %#v, %v, want exactly 1", items, err)
	}
	item := items[0]
	if item.Type != attention.TypeApprovalRequired || item.Action.Operation != "interaction.plan.approve_and_execute" ||
		item.ProjectName != plan.ProjectName {
		t.Fatalf("item = %#v", item)
	}
}

func TestInspectAttentionReadyToExecuteProducesApprovalItemUnlessPreAuthorized(t *testing.T) {
	root := attentionVault(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	session := newAttentionSession(t, root, "SESSION-1", at)
	fixture := loadCEOPlanFixture(t)
	plan := fixture.ExpectedPlan
	plan.CEOQuestions = []string{}
	withPlan, err := session.RecordPlan(plan, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	withPlan = updateAttentionSession(t, root, withPlan, session.Version)
	_, planDigest, _ := withPlan.CurrentPlan()
	applied, err := withPlan.RecordApplied("PROJECT-001", plan.ProjectName, planDigest, "", at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	session = updateAttentionSession(t, root, applied, withPlan.Version)
	if session.State != interaction.StateReadyToExecute {
		t.Fatalf("session.State = %v, want StateReadyToExecute", session.State)
	}
	items, err := InspectAttention(context.Background(), root, at)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %#v, %v, want exactly 1", items, err)
	}
	if items[0].Type != attention.TypeApprovalRequired || items[0].Action.Operation != "interaction.workflow.execute" {
		t.Fatalf("item = %#v", items[0])
	}
}

func TestInspectAttentionPreAuthorizedReadyToExecuteProducesNoItem(t *testing.T) {
	root := attentionVault(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	session := newAttentionSession(t, root, "SESSION-1", at)
	fixture := loadCEOPlanFixture(t)
	plan := fixture.ExpectedPlan
	plan.CEOQuestions = []string{}
	withPlan, err := session.RecordPlan(plan, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	withPlan = updateAttentionSession(t, root, withPlan, session.Version)
	_, planDigest, _ := withPlan.CurrentPlan()
	applied, err := withPlan.RecordApplied("PROJECT-001", plan.ProjectName, planDigest, "CMD-PREAUTH-1", at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	updateAttentionSession(t, root, applied, withPlan.Version)
	items, err := InspectAttention(context.Background(), root, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %#v, want none for a transient pre-authorized ReadyToExecute Session", items)
	}
}

func TestInspectAttentionArchivedSessionProducesNoItem(t *testing.T) {
	root := attentionVault(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	session := newAttentionSession(t, root, "SESSION-1", at)
	archived, err := session.RecordArchive(at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	updateAttentionSession(t, root, archived, session.Version)
	items, err := InspectAttention(context.Background(), root, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %#v, want none for an archived Session", items)
	}
}

func TestInspectAttentionWorkflowAttentionRequiredProducesInteractionAttentionItem(t *testing.T) {
	root := attentionVault(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	session := newAttentionSession(t, root, "SESSION-1", at)
	fixture := loadCEOPlanFixture(t)
	plan := fixture.ExpectedPlan
	plan.CEOQuestions = []string{}
	withPlan, err := session.RecordPlan(plan, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	withPlan = updateAttentionSession(t, root, withPlan, session.Version)
	_, planDigest, _ := withPlan.CurrentPlan()
	applied, err := withPlan.RecordApplied("PROJECT-001", plan.ProjectName, planDigest, "", at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	applied = updateAttentionSession(t, root, applied, withPlan.Version)
	failed, err := applied.RecordWorkflow(interaction.WorkflowEvidence{
		SchemaVersion: interaction.SchemaVersion, CommandID: "CMD-WORKFLOW-1", WorkflowCommandID: "CMD-WORKFLOW-EXEC-1",
		ProjectID: "PROJECT-001", ProjectName: plan.ProjectName, ReviewerID: "QA-001", MaxTasks: 10,
		Status: interaction.WorkflowStatusFailed, ResultDigest: "sha256:" + fixedDigestSuffix(), Tasks: []interaction.WorkflowTaskEvidence{},
		Failure: &interaction.WorkflowFailure{Code: "SOME_FAILURE", Stage: "workflow"},
	}, at.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	session = updateAttentionSession(t, root, failed, applied.Version)
	if session.State != interaction.StateWorkflowAttentionRequired {
		t.Fatalf("session.State = %v, want StateWorkflowAttentionRequired", session.State)
	}
	items, err := InspectAttention(context.Background(), root, at)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %#v, %v, want exactly 1", items, err)
	}
	item := items[0]
	if item.Type != attention.TypeInteractionAttentionRequired || item.Action.Kind != attention.ActionInspect {
		t.Fatalf("item = %#v, want TypeInteractionAttentionRequired/ActionInspect (no eligible recovery target for this Failure code)", item)
	}
}

func TestInspectAttentionCompletedWorkflowProducesNoItem(t *testing.T) {
	root := attentionVault(t)
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	session := newAttentionSession(t, root, "SESSION-1", at)
	fixture := loadCEOPlanFixture(t)
	plan := fixture.ExpectedPlan
	plan.CEOQuestions = []string{}
	withPlan, err := session.RecordPlan(plan, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	withPlan = updateAttentionSession(t, root, withPlan, session.Version)
	_, planDigest, _ := withPlan.CurrentPlan()
	applied, err := withPlan.RecordApplied("PROJECT-001", plan.ProjectName, planDigest, "", at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	applied = updateAttentionSession(t, root, applied, withPlan.Version)
	completed, err := applied.RecordWorkflow(interaction.WorkflowEvidence{
		SchemaVersion: interaction.SchemaVersion, CommandID: "CMD-WORKFLOW-1", WorkflowCommandID: "CMD-WORKFLOW-EXEC-1",
		ProjectID: "PROJECT-001", ProjectName: plan.ProjectName, ReviewerID: "QA-001", MaxTasks: 10,
		Status: interaction.WorkflowStatusCompleted, ResultDigest: "sha256:" + fixedDigestSuffix(), Tasks: []interaction.WorkflowTaskEvidence{},
	}, at.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	session = updateAttentionSession(t, root, completed, applied.Version)
	if session.State != interaction.StateCompleted {
		t.Fatalf("session.State = %v, want StateCompleted", session.State)
	}
	items, err := InspectAttention(context.Background(), root, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %#v, want none: StateCompleted's only next step (optional external publish) is not a required decision", items)
	}
}

// --- Ordering / dedupe / governance ---

func TestInspectAttentionDeterministicOrderingAndRepeatedScans(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	store, err := routineStoreFor(root, routine.ScopeCompany, "")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(context.Background(), routineID)
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
	at := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	newAttentionSession(t, root, "SESSION-1", at)

	now := time.Date(2026, 8, 26, 13, 0, 0, 0, time.UTC)
	first, err := InspectAttention(context.Background(), root, now)
	if err != nil || len(first) != 2 {
		t.Fatalf("first scan = %#v, %v, want 2 items", first, err)
	}
	if first[0].Type != attention.TypeApprovalRequired || first[1].Type != attention.TypeRoutineRecoveryRequired {
		t.Fatalf("first scan order = %#v, want approval_required before routine_recovery_required", first)
	}
	second, err := InspectAttention(context.Background(), root, now)
	if err != nil {
		t.Fatal(err)
	}
	for index := range first {
		if first[index] != second[index] {
			t.Fatalf("repeated scan is not identical: first=%#v second=%#v", first, second)
		}
	}
}

func TestInspectAttentionGovernanceNoMutationsNoClaims(t *testing.T) {
	root, _, routineID := routinePlanFixture(t)
	store, err := routineStoreFor(root, routine.ScopeCompany, "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Get(context.Background(), routineID)
	if err != nil {
		t.Fatal(err)
	}
	ledgerDirectory := filepath.Join(root, ".workspace-os", "commands")
	ledgerBefore, _ := os.ReadDir(ledgerDirectory)

	if _, err := InspectAttention(context.Background(), root, time.Now()); err != nil {
		t.Fatal(err)
	}

	after, err := store.Get(context.Background(), routineID)
	if err != nil || after.Version != before.Version || after.Status != before.Status {
		t.Fatalf("Routine changed after InspectAttention: before=%#v after=%#v, err=%v", before, after, err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "プロジェクト"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("プロジェクト/ has %d entries after InspectAttention, want 0: %v, err=%v", len(entries), entries, err)
	}
	responsibilityAfter, err := InspectResponsibility(context.Background(), root, responsibility.ScopeCompany, "", "RESP-1")
	if err != nil {
		t.Fatal(err)
	}
	if responsibilityAfter.Version != 1 {
		t.Fatalf("Responsibility changed after InspectAttention: %#v", responsibilityAfter)
	}
	ledgerAfter, _ := os.ReadDir(ledgerDirectory)
	if len(ledgerAfter) != len(ledgerBefore) {
		t.Fatalf(".workspace-os/commands/ grew from %d to %d entries after a read-only InspectAttention call", len(ledgerBefore), len(ledgerAfter))
	}
}

func fixedDigestSuffix() string {
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"[:64]
}
