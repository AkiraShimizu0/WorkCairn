package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/project"
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
