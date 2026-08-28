package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/project"
)

func TestProjectAndTaskCreationPlanBeforeApprovedExecution(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeOrganizationProcessFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	at := time.Date(2026, 8, 6, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60))
	projectInput := ProjectBootstrapInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", Description: "シンプルなToDoアプリ", CurrentTime: at}
	before := organizationProcessSnapshot(t, root)
	plan, err := PlanProjectBootstrap(context.Background(), projectInput)
	if err != nil || !plan.Executable || !plan.ApprovalRequired {
		t.Fatalf("plan = %#v, %v", plan, err)
	}
	if _, err := ExecuteProjectBootstrap(context.Background(), projectInput, false); err != ErrProjectApprovalRequired {
		t.Fatalf("unapproved error = %v", err)
	}
	if after := organizationProcessSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("Project plan/unapproved execution changed Vault")
	}
	if _, err := ExecuteProjectBootstrap(context.Background(), projectInput, true); err != nil {
		t.Fatal(err)
	}
	assignee := "PLAN-001"
	taskInput := TaskCreationInput{VaultRoot: root, ProjectName: "ToDoアプリ", Title: "要件を整理する", AssigneeID: &assignee, CurrentTime: at}
	taskPlan, err := PlanTaskCreation(context.Background(), taskInput)
	if err != nil || !taskPlan.Executable || taskPlan.TaskID != "TASK-001" {
		t.Fatalf("Task plan = %#v, %v", taskPlan, err)
	}
	result, err := ExecuteTaskCreation(context.Background(), taskInput, true)
	if err != nil || result.Task.ID != "TASK-001" || !result.EventPublished {
		t.Fatalf("Task creation = %#v, %v", result, err)
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: root, ProjectName: "ToDoアプリ"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Inspect(context.Background(), "TASK-001")
	if err != nil || stored.Version != 1 || stored.AssigneeID == nil || *stored.AssigneeID != assignee {
		t.Fatalf("stored Task = %#v, %v", stored, err)
	}
	if _, err := os.Stat(filepath.Join(root, "プロジェクト", "ToDoアプリ", "Audit Log.md")); err != nil {
		t.Fatalf("Task Audit missing: %v", err)
	}
}

func TestPlanProjectBootstrapResolvesDeterministicSuffixOnNameCollisionWithoutTouchingExisting(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	first := ProjectBootstrapInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "りんご説明文作成プロジェクト", Description: "1回目", CurrentTime: at}
	plan, err := PlanProjectBootstrap(context.Background(), first)
	if err != nil || plan.ProjectName != "りんご説明文作成プロジェクト" || plan.RequestedProjectNameTaken || !plan.Executable {
		t.Fatalf("first plan = %#v, %v", plan, err)
	}
	if _, err := ExecuteProjectBootstrap(context.Background(), first, true); err != nil {
		t.Fatal(err)
	}
	firstProjectMD, readErr := os.ReadFile(filepath.Join(root, "プロジェクト", "りんご説明文作成プロジェクト", "Project.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}

	// Same display name, new Command/Project ID (a second CEO Plan Apply for
	// a similar natural-language request) must not adopt, merge into, or
	// overwrite the first Project directory.
	second := ProjectBootstrapInput{VaultRoot: root, ProjectID: "PROJECT-002", ProjectName: "りんご説明文作成プロジェクト", Description: "2回目", CurrentTime: at.Add(time.Hour)}
	secondPlan, err := PlanProjectBootstrap(context.Background(), second)
	if err != nil || secondPlan.ProjectName != "りんご説明文作成プロジェクト (2)" || !secondPlan.RequestedProjectNameTaken ||
		secondPlan.RequestedProjectName != "りんご説明文作成プロジェクト" || !secondPlan.Executable || len(secondPlan.BlockingReasons) != 0 {
		t.Fatalf("second plan = %#v, %v", secondPlan, err)
	}
	secondRecord, err := ExecuteProjectBootstrap(context.Background(), second, true)
	if err != nil || secondRecord.ProjectName != "りんご説明文作成プロジェクト (2)" || secondRecord.ProjectID != "PROJECT-002" {
		t.Fatalf("second Project record = %#v, %v", secondRecord, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "プロジェクト", "りんご説明文作成プロジェクト (2)", "Project.md")); statErr != nil {
		t.Fatalf("second Project directory missing: %v", statErr)
	}
	afterFirstProjectMD, readErr := os.ReadFile(filepath.Join(root, "プロジェクト", "りんご説明文作成プロジェクト", "Project.md"))
	if readErr != nil || string(afterFirstProjectMD) != string(firstProjectMD) {
		t.Fatalf("first Project.md changed after second apply: before=%q after=%q, %v", firstProjectMD, afterFirstProjectMD, readErr)
	}

	// A third request resolves to the next free suffix, not the one already
	// taken by the second.
	third := ProjectBootstrapInput{VaultRoot: root, ProjectID: "PROJECT-003", ProjectName: "りんご説明文作成プロジェクト", Description: "3回目", CurrentTime: at.Add(2 * time.Hour)}
	thirdPlan, err := PlanProjectBootstrap(context.Background(), third)
	if err != nil || thirdPlan.ProjectName != "りんご説明文作成プロジェクト (3)" {
		t.Fatalf("third plan = %#v, %v", thirdPlan, err)
	}
}

func TestPlanProjectBootstrapReportsBlockingWhenSuffixSearchIsExhausted(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "プロジェクト", "満員プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	for suffix := 2; suffix <= maxProjectNameSuffix; suffix++ {
		if err := os.Mkdir(filepath.Join(root, "プロジェクト", fmt.Sprintf("満員プロジェクト (%d)", suffix)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	plan, err := PlanProjectBootstrap(context.Background(), ProjectBootstrapInput{
		VaultRoot: root, ProjectID: "PROJECT-EXHAUSTED", ProjectName: "満員プロジェクト", CurrentTime: at,
	})
	if err != nil || plan.Executable || !reflect.DeepEqual(plan.BlockingReasons, []string{"project_already_exists"}) ||
		plan.ProjectName != "満員プロジェクト" || !plan.RequestedProjectNameTaken {
		t.Fatalf("exhausted plan = %#v, %v", plan, err)
	}
}

func TestTaskCreationRejectsUnknownAssigneeWithoutWrite(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "社員"), 0o755); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	_, err := ExecuteProjectBootstrap(context.Background(), ProjectBootstrapInput{VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "P", CurrentTime: at}, true)
	if err != nil {
		t.Fatal(err)
	}
	unknown := "DEV-999"
	before := organizationProcessSnapshot(t, root)
	plan, err := PlanTaskCreation(context.Background(), TaskCreationInput{VaultRoot: root, ProjectName: "P", Title: "Task", AssigneeID: &unknown, CurrentTime: at})
	if err != nil || plan.Executable || !reflect.DeepEqual(plan.BlockingReasons, []string{"assignee_not_found"}) {
		t.Fatalf("plan = %#v, %v", plan, err)
	}
	if after := organizationProcessSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("blocked Task plan changed Vault")
	}
}

func TestProjectAndTaskWriterCommandReplayAndConflict(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "プロジェクト"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeOrganizationProcessFile(t, filepath.Join(root, "社員", "田中 美咲.md"), "---\nid: PLAN-001\ndepartment: 企画部\nrole: Product Manager\nmodel: Claude Sonnet 5\nstatus: 待機中\n---\n")
	at := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	projectInput := ProjectBootstrapInput{
		VaultRoot: root, ProjectID: "PROJECT-001", ProjectName: "案件", Description: "説明", CurrentTime: at,
		CommandID: "CMD-PROJECT-001",
	}
	firstProject, err := ExecuteProjectBootstrap(context.Background(), projectInput, true)
	if err != nil {
		t.Fatal(err)
	}
	projectSnapshot := organizationProcessSnapshot(t, root)
	replayedProject, err := ExecuteProjectBootstrap(context.Background(), projectInput, true)
	if err != nil || !reflect.DeepEqual(firstProject, replayedProject) || !reflect.DeepEqual(projectSnapshot, organizationProcessSnapshot(t, root)) {
		t.Fatalf("Project replay = %#v, %v", replayedProject, err)
	}
	projectInput.Description = "別の説明"
	if _, err := ExecuteProjectBootstrap(context.Background(), projectInput, true); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("Project conflict error = %v", err)
	}

	assignee := "PLAN-001"
	taskInput := TaskCreationInput{
		VaultRoot: root, ProjectName: "案件", Title: "要件整理", AssigneeID: &assignee, CurrentTime: at,
		CommandID: "CMD-TASK-CREATE-001",
	}
	firstTask, err := ExecuteTaskCreation(context.Background(), taskInput, true)
	if err != nil {
		t.Fatal(err)
	}
	taskSnapshot := organizationProcessSnapshot(t, root)
	replayedTask, err := ExecuteTaskCreation(context.Background(), taskInput, true)
	if err != nil || !reflect.DeepEqual(firstTask, replayedTask) || !reflect.DeepEqual(taskSnapshot, organizationProcessSnapshot(t, root)) {
		t.Fatalf("Task replay = %#v, %v", replayedTask, err)
	}
	taskInput.Title = "別Task"
	if _, err := ExecuteTaskCreation(context.Background(), taskInput, true); !errors.Is(err, commandledger.ErrRequestConflict) {
		t.Fatalf("Task conflict error = %v", err)
	}

	dependencyInput := ProjectDependenciesInput{
		VaultRoot: root, ProjectName: "案件", CurrentTime: at, CommandID: "CMD-DEPENDENCIES-001",
		Rows: []project.TaskDependency{{TaskID: firstTask.Task.ID, ProposalID: "PROPOSED-001", DependsOn: []string{}, Rationale: "first task"}},
	}
	firstDependencies, err := ExecuteProjectDependencies(context.Background(), dependencyInput, true)
	if err != nil {
		t.Fatal(err)
	}
	dependencySnapshot := organizationProcessSnapshot(t, root)
	replayedDependencies, err := ExecuteProjectDependencies(context.Background(), dependencyInput, true)
	if err != nil || !reflect.DeepEqual(firstDependencies, replayedDependencies) || !reflect.DeepEqual(dependencySnapshot, organizationProcessSnapshot(t, root)) {
		t.Fatalf("Dependency replay = %#v, %v", replayedDependencies, err)
	}
}
