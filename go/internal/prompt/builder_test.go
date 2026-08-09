package prompt

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

	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
)

type taskExecutionFixture struct {
	Name     string             `json:"name"`
	Input    worker.PromptInput `json:"input"`
	Expected worker.Prompt      `json:"expected"`
}

func TestBuilderMatchesGoldenFixture(t *testing.T) {
	fixture := loadTaskExecutionFixture(t)
	built, err := NewBuilder().Build(context.Background(), fixture.Input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(built, fixture.Expected) {
		t.Fatalf("Build() = %#v, want %#v", built, fixture.Expected)
	}
	if strings.HasSuffix(built.System, "\n") || strings.HasSuffix(built.User, "\n") {
		t.Fatal("golden prompt must not gain a trailing newline")
	}
}

func TestBuilderIsDeterministicAndKeepsSectionOrder(t *testing.T) {
	fixture := loadTaskExecutionFixture(t)
	builder := NewBuilder()
	first, err := builder.Build(context.Background(), fixture.Input)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		next, err := builder.Build(context.Background(), fixture.Input)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(next, first) {
			t.Fatalf("Build() changed at iteration %d", index)
		}
	}

	previous := -1
	for _, heading := range []string{"## 会社情報", "## 社員情報", "## 現在情報", "## プロジェクト情報", "## タスク情報"} {
		position := strings.Index(first.System, heading)
		if position <= previous {
			t.Fatalf("section %q is out of order in %q", heading, first.System)
		}
		previous = position
	}
	if strings.Count(first.System, "\n\n## ") != 4 {
		t.Fatalf("section blank lines changed: %q", first.System)
	}
}

func TestBuilderHandlesOptionalProjectOverviewAndAssignee(t *testing.T) {
	fixture := loadTaskExecutionFixture(t)
	builder := NewBuilder()

	withoutOptional := fixture.Input
	withoutOptional.Task.ProjectOverview = ""
	withoutOptional.Task.AssigneeID = nil
	built, err := builder.Build(context.Background(), withoutOptional)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(built.System, "プロジェクト概要:") {
		t.Fatalf("empty overview was rendered: %q", built.System)
	}
	if !strings.Contains(built.System, "担当社員ID: 未割当") {
		t.Fatalf("nil assignee was not rendered as unassigned: %q", built.System)
	}

	emptyAssignee := ""
	withoutOptional.Task.AssigneeID = &emptyAssignee
	built, err = builder.Build(context.Background(), withoutOptional)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(built.System, "担当社員ID: 未割当") {
		t.Fatalf("empty assignee was not rendered as unassigned: %q", built.System)
	}
}

func TestBuilderPreservesUnicodeSpecialCharactersAndNewlines(t *testing.T) {
	fixture := loadTaskExecutionFixture(t)
	input := fixture.Input
	input.Employee.Name = "田中 美咲｜🚀\n共同担当 #1"
	input.Employee.Department = "企画|R&D"
	input.Task.ProjectName = "新規 #Project｜α"
	input.Task.ProjectOverview = "1行目 | 要件\n2行目 # TODO\n絵文字: 🧪"
	input.Task.Title = "仕様を整理する | #重要\n補足行"
	assigneeID := "PLAN-001|副担当"
	input.Task.AssigneeID = &assigneeID

	built, err := NewBuilder().Build(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"氏名: 田中 美咲｜🚀\n共同担当 #1",
		"部署: 企画|R&D",
		"プロジェクト名: 新規 #Project｜α",
		"プロジェクト概要: 1行目 | 要件\n2行目 # TODO\n絵文字: 🧪",
		"タイトル: 仕様を整理する | #重要\n補足行",
		"担当社員ID: PLAN-001|副担当",
	} {
		if !strings.Contains(built.System, expected) {
			t.Fatalf("Build() did not preserve %q in %q", expected, built.System)
		}
	}
	if !strings.Contains(built.User, "担当タスク: 仕様を整理する | #重要\n補足行") {
		t.Fatalf("user prompt did not preserve task text: %q", built.User)
	}
}

func TestBuilderConvertsInjectedTimeToJST(t *testing.T) {
	fixture := loadTaskExecutionFixture(t)
	input := fixture.Input
	input.CurrentTime = time.Date(2026, time.August, 6, 7, 30, 0, 0, time.UTC)
	built, err := NewBuilder().Build(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(built.System, "現在日時（JST）: 2026-08-06 16:30:00 JST") {
		t.Fatalf("JST time = %q", built.System)
	}
}

func TestBuilderRejectsMissingRequiredContext(t *testing.T) {
	fixture := loadTaskExecutionFixture(t)
	for _, test := range []struct {
		name   string
		change func(*worker.PromptInput)
	}{
		{"employee name", func(input *worker.PromptInput) { input.Employee.Name = "" }},
		{"task ID", func(input *worker.PromptInput) { input.Task.TaskID = "" }},
		{"project name", func(input *worker.PromptInput) { input.Task.ProjectName = "" }},
		{"current time", func(input *worker.PromptInput) { input.CurrentTime = time.Time{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := fixture.Input
			test.change(&input)
			if _, err := NewBuilder().Build(context.Background(), input); err == nil {
				t.Fatal("Build() error = nil")
			}
		})
	}
}

func TestBuilderHonorsContextCancellation(t *testing.T) {
	fixture := loadTaskExecutionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewBuilder().Build(ctx, fixture.Input); !errors.Is(err, context.Canceled) {
		t.Fatalf("Build() error = %v, want context.Canceled", err)
	}
}

func loadTaskExecutionFixture(t *testing.T) taskExecutionFixture {
	t.Helper()
	path := filepath.Join("..", "..", "..", "fixtures", "prompt", "task_execution.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read prompt fixture: %v", err)
	}
	var fixture taskExecutionFixture
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatalf("decode prompt fixture: %v", err)
	}
	return fixture
}
