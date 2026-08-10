package vault

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/policy"
	promptbuilder "github.com/AkiraShimizu0/workcairn/go/internal/prompt"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
	"github.com/AkiraShimizu0/workcairn/go/internal/workflow"
)

type promptFixture struct {
	Expected worker.Prompt `json:"expected"`
}

func TestLoaderBuildsExecutionContextAndPreservesGoldenPrompt(t *testing.T) {
	root := t.TempDir()
	writeFakeVault(t, root)
	before := vaultSnapshot(t, root)
	loader, err := NewLoader(root)
	if err != nil {
		t.Fatal(err)
	}
	currentTime := time.Date(2026, time.August, 6, 16, 30, 0, 0, time.FixedZone("JST", 9*60*60))
	approval := &policy.ApprovalEvidence{Granted: true, Source: "test", Reference: "approval-001"}
	metadata := map[string]string{"correlation_id": "COR-001"}
	request, err := loader.LoadExecutionRequest(context.Background(), ExecutionInput{
		ProjectID:   "PROJECT-001",
		ProjectName: "ToDoアプリ",
		TaskID:      "TASK-001",
		Approval:    approval,
		CurrentTime: currentTime,
		ExecutionID: "EXEC-001",
		CommandID:   "CMD-001",
		Metadata:    metadata,
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.ProjectID != "PROJECT-001" || request.ProjectOverview != "シンプルなToDo Webアプリを開発する" ||
		request.Employee.EmployeeID != "PLAN-001" || request.Employee.Name != "田中 美咲" ||
		request.Employee.Model != "Claude Sonnet 5" || request.CurrentTime != currentTime {
		t.Fatalf("Execution context = %#v", request)
	}
	if len(request.Tasks) != 1 || request.Tasks[0].AssigneeID == nil || *request.Tasks[0].AssigneeID != "PLAN-001" ||
		len(request.Dependencies) != 1 || request.Dependencies[0].TaskID != "TASK-001" || len(request.Dependencies[0].DependsOn) != 0 ||
		!reflect.DeepEqual(request.ExistingEmployees, map[string]bool{"PLAN-001": true}) {
		t.Fatalf("Task context = %#v, %#v, %#v", request.Tasks, request.Dependencies, request.ExistingEmployees)
	}
	if request.Approval == approval || request.Metadata["correlation_id"] != "COR-001" {
		t.Fatal("Loader did not clone caller-owned context")
	}
	approval.Granted = false
	metadata["correlation_id"] = "changed"
	if !request.Approval.Granted || request.Metadata["correlation_id"] != "COR-001" {
		t.Fatal("Loaded context changed with caller-owned values")
	}

	built, err := promptbuilder.NewBuilder().Build(context.Background(), worker.PromptInput{
		Employee: request.Employee,
		Task: worker.TaskContext{
			TaskID:          request.TaskID,
			Title:           request.Tasks[0].Title,
			ProjectName:     request.ProjectName,
			ProjectOverview: request.ProjectOverview,
			AssigneeID:      request.Tasks[0].AssigneeID,
		},
		CurrentTime: request.CurrentTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := loadPromptFixture(t)
	if !reflect.DeepEqual(built, fixture.Expected) {
		t.Fatalf("Vault context prompt = %#v, want %#v", built, fixture.Expected)
	}
	if after := vaultSnapshot(t, root); !reflect.DeepEqual(after, before) {
		t.Fatal("read-only Loader changed the Vault fixture")
	}
}

func TestLoaderParsesDependenciesWithEscapedRationalePipe(t *testing.T) {
	content := "| Task ID | Proposed ID | Depends On | Rationale |\n" +
		"|---|---|---|---|\n" +
		"| TASK-001 | PROPOSED-001 | なし | A \\| B |\n" +
		"| TASK-002 | PROPOSED-002 | TASK-001 | test |\n"
	parsed, err := parseDependencies(content)
	if err != nil {
		t.Fatal(err)
	}
	want := []workflow.Dependency{
		{TaskID: "TASK-001", DependsOn: []string{}},
		{TaskID: "TASK-002", DependsOn: []string{"TASK-001"}},
	}
	if !reflect.DeepEqual(parsed, want) {
		t.Fatalf("dependencies = %#v, want %#v", parsed, want)
	}
}

func TestLoaderRejectsMalformedDependencyTableRow(t *testing.T) {
	_, err := parseDependencies("| TASK-001 | only-two-columns |\n")
	if !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoaderRejectsUnsafeOrIncompatibleVaultContext(t *testing.T) {
	currentTime := time.Date(2026, time.August, 6, 16, 30, 0, 0, time.UTC)
	validInput := ExecutionInput{
		ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", TaskID: "TASK-001", CurrentTime: currentTime,
	}

	t.Run("path traversal", func(t *testing.T) {
		root := t.TempDir()
		writeFakeVault(t, root)
		loader, err := NewLoader(root)
		if err != nil {
			t.Fatal(err)
		}
		input := validInput
		input.ProjectName = "../outside"
		if _, err := loader.LoadExecutionRequest(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("error = %v", err)
		}
	})

	for _, test := range []struct {
		name   string
		change func(*testing.T, string)
		kind   error
	}{
		{"missing dependencies", func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "プロジェクト", "ToDoアプリ", "Task Dependencies.md")); err != nil {
				t.Fatal(err)
			}
		}, ErrDocumentNotFound},
		{"legacy assignee column", func(t *testing.T, root string) {
			path := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Tasks.md")
			content := readTestFile(t, path)
			writeTestFile(t, path, replaceOnce(t, content, "担当社員ID", "担当"))
		}, ErrInvalidDocument},
		{"unassigned Task", func(t *testing.T, root string) {
			path := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Tasks.md")
			content := readTestFile(t, path)
			writeTestFile(t, path, replaceOnce(t, content, "PLAN-001", "未割当"))
		}, ErrAssigneeMissing},
		{"unknown assignee", func(t *testing.T, root string) {
			path := filepath.Join(root, "プロジェクト", "ToDoアプリ", "Tasks.md")
			content := readTestFile(t, path)
			writeTestFile(t, path, replaceOnce(t, content, "PLAN-001", "UNKNOWN-001"))
		}, ErrDocumentNotFound},
		{"duplicate employee ID", func(t *testing.T, root string) {
			writeTestFile(t, filepath.Join(root, "社員", "佐藤 蓮.md"), employeeMarkdown("PLAN-001", "開発部", "Engineer", "Claude Sonnet 5"))
		}, ErrDuplicateIdentity},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFakeVault(t, root)
			test.change(t, root)
			loader, err := NewLoader(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := loader.LoadExecutionRequest(context.Background(), validInput); !errors.Is(err, test.kind) {
				t.Fatalf("error = %v, want %v", err, test.kind)
			}
		})
	}
}

func TestLoaderHonorsContextCancellation(t *testing.T) {
	root := t.TempDir()
	writeFakeVault(t, root)
	loader, err := NewLoader(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = loader.LoadExecutionRequest(ctx, ExecutionInput{
		ProjectID: "PROJECT-001", ProjectName: "ToDoアプリ", TaskID: "TASK-001", CurrentTime: time.Now(),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func writeFakeVault(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "社員"), 0o755); err != nil {
		t.Fatal(err)
	}
	projectDirectory := filepath.Join(root, "プロジェクト", "ToDoアプリ")
	if err := os.MkdirAll(projectDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "社員", "田中 美咲.md"), employeeMarkdown("PLAN-001", "企画部", "Product Manager", "Claude Sonnet 5"))
	writeTestFile(t, filepath.Join(projectDirectory, "Project.md"), "---\ntype: project\nname: ToDoアプリ\n---\n\n# ToDoアプリ\n\n## 概要\n\nシンプルなToDo Webアプリを開発する\n")
	writeTestFile(t, filepath.Join(projectDirectory, "Tasks.md"), "---\ntype: project-tasks\nproject: ToDoアプリ\n---\n\n| ID | タスク | 状態 | 担当社員ID | 作成日時 |\n|---|---|---|---|---|\n| TASK-001 | 要件を整理する | 未着手 | PLAN-001 | 2026-08-06 16:00 |\n")
	writeTestFile(t, filepath.Join(projectDirectory, "Task Dependencies.md"), "---\ntype: task-dependencies\nproject: ToDoアプリ\n---\n\n| Task ID | Proposed ID | Depends On | Rationale |\n|---|---|---|---|\n| TASK-001 | PROPOSED-001 | なし | 初期Task |\n")
}

func employeeMarkdown(employeeID, department, role, model string) string {
	return "---\n" +
		"id: " + employeeID + "\n" +
		"department: " + department + "\n" +
		"role: " + role + "\n" +
		"model: " + model + "\n" +
		"status: 待機中\n---\n"
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func replaceOnce(t *testing.T, content, old, replacement string) string {
	t.Helper()
	if !strings.Contains(content, old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return strings.Replace(content, old, replacement, 1)
}

func vaultSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot[relative] = string(content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func loadPromptFixture(t *testing.T) promptFixture {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "fixtures", "prompt", "task_execution.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture promptFixture
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}
