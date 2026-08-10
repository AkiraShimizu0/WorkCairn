package vault

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/task"
)

func TestTaskMetadataMigrationPlanIsReadOnlyAndApplyRequiresApproval(t *testing.T) {
	root, tasksPath := legacyVaultFromFixture(t)
	store := newTestTaskStore(t, root)
	before := vaultSnapshot(t, root)

	plan, err := store.PlanTaskMetadataMigration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != 1 || !strings.HasPrefix(plan.SourceRevision, "sha256:") ||
		len(plan.Tasks) != 1 || plan.Tasks[0].TaskID != "TASK-001" ||
		plan.Tasks[0].Status != task.StatusUnstarted || plan.Tasks[0].InitialVersion != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	if after := vaultSnapshot(t, root); !equalStringMaps(after, before) {
		t.Fatalf("plan changed temporary Vault: before=%#v after=%#v", before, after)
	}
	if err := store.ApplyTaskMetadataMigration(context.Background(), plan, false); !errors.Is(err, ErrMigrationApproval) {
		t.Fatalf("unapproved Apply() error = %v", err)
	}
	if after := vaultSnapshot(t, root); !equalStringMaps(after, before) {
		t.Fatal("unapproved Apply changed temporary Vault")
	}

	if err := store.ApplyTaskMetadataMigration(context.Background(), plan, true); err != nil {
		t.Fatal(err)
	}
	content := readTestFile(t, tasksPath)
	if strings.Count(content, taskMetadataMarker) != 1 {
		t.Fatalf("migrated Tasks.md =\n%s", content)
	}
	got, err := store.Get(context.Background(), "TASK-001")
	if err != nil || got.Version != 1 || got.Status != task.StatusUnstarted {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
	if _, err := store.PlanTaskMetadataMigration(context.Background()); !errors.Is(err, ErrMigrationNotNeeded) {
		t.Fatalf("second Plan() error = %v", err)
	}
}

func TestTaskMetadataMigrationRejectsStalePlanWithoutReplacing(t *testing.T) {
	root, tasksPath := legacyVaultFromFixture(t)
	store := newTestTaskStore(t, root)
	plan, err := store.PlanTaskMetadataMigration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(readTestFile(t, tasksPath), "要件を整理する", "別の要件を整理する", 1)
	writeTestFile(t, tasksPath, changed)
	before := readTestFile(t, tasksPath)

	err = store.ApplyTaskMetadataMigration(context.Background(), plan, true)
	if !errors.Is(err, ErrMigrationStale) {
		t.Fatalf("Apply() error = %v", err)
	}
	if after := readTestFile(t, tasksPath); after != before || strings.Contains(after, taskMetadataMarker) {
		t.Fatal("stale migration changed Tasks.md")
	}
}

func TestTaskMetadataMigrationRejectsOnHoldTaskWithoutGuessingReason(t *testing.T) {
	root, tasksPath := legacyVaultFromFixture(t)
	content := strings.Replace(readTestFile(t, tasksPath), "未着手", "保留", 1)
	writeTestFile(t, tasksPath, content)
	store := newTestTaskStore(t, root)
	before := vaultSnapshot(t, root)

	_, err := store.PlanTaskMetadataMigration(context.Background())
	if !errors.Is(err, ErrMigrationUnsafe) {
		t.Fatalf("Plan() error = %v", err)
	}
	if after := vaultSnapshot(t, root); !equalStringMaps(after, before) {
		t.Fatal("unsafe migration plan changed temporary Vault")
	}
}

func TestTaskMetadataMigrationRejectsInvalidExistingMetadata(t *testing.T) {
	root, tasksPath := managedVaultFromFixture(t)
	content := strings.Replace(readTestFile(t, tasksPath), `"schema_version": 1`, `"schema_version":`, 1)
	writeTestFile(t, tasksPath, content)
	store := newTestTaskStore(t, root)

	_, err := store.PlanTaskMetadataMigration(context.Background())
	if !errors.Is(err, ErrMetadataInvalid) {
		t.Fatalf("Plan() error = %v", err)
	}
}

func legacyVaultFromFixture(t *testing.T) (string, string) {
	t.Helper()
	root, tasksPath := emptyVaultLayout(t)
	content := sharedManagedFixture(t)
	markerIndex := strings.Index(content, taskMetadataMarker)
	if markerIndex == -1 {
		t.Fatal("shared fixture has no managed marker")
	}
	writeTestFile(t, tasksPath, content[:markerIndex])
	return root, tasksPath
}

func equalStringMaps(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
