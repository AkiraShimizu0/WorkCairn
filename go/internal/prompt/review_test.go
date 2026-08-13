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

	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

type reviewExecutionFixture struct {
	Name     string             `json:"name"`
	Input    review.PromptInput `json:"input"`
	Expected worker.Prompt      `json:"expected"`
}

func TestReviewBuilderMatchesGoldenFixture(t *testing.T) {
	fixture := loadReviewExecutionFixture(t)
	built, err := NewBuilder().BuildReview(context.Background(), fixture.Input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(built, fixture.Expected) {
		t.Fatalf("BuildReview() = %#v, want %#v", built, fixture.Expected)
	}
	if strings.HasSuffix(built.System, "\n") || strings.HasSuffix(built.User, "\n") {
		t.Fatal("review golden prompt must not gain a trailing newline")
	}
}

func TestReviewBuilderIsDeterministicAndKeepsSectionOrder(t *testing.T) {
	fixture := loadReviewExecutionFixture(t)
	first, err := NewBuilder().BuildReview(context.Background(), fixture.Input)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		next, err := NewBuilder().BuildReview(context.Background(), fixture.Input)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(next, first) {
			t.Fatalf("BuildReview() changed at iteration %d", index)
		}
	}

	previous := -1
	for _, heading := range []string{
		"## 会社情報", "## 社員情報", "## 現在情報",
		"## プロジェクト情報", "## タスク情報",
		"## レビューコンテキスト", "## レビュー方針",
	} {
		position := strings.Index(first.System, heading)
		if position <= previous {
			t.Fatalf("section %q is out of order", heading)
		}
		previous = position
	}
	if !strings.Contains(first.System, "Markdown code fence（```）、コメント、余分な空行やテキストを一切含めないでください") ||
		!strings.Contains(first.System, "開始マーカー REVIEW_RESULT_JSON_START を正確に1回だけ出力してください") ||
		!strings.Contains(first.System, "終了マーカー REVIEW_RESULT_JSON_END を正確に1回だけ出力してください") ||
		!strings.Contains(first.System, "JSONのtop-level fieldはverdictとissuesの2つだけにしてください") ||
		!strings.Contains(first.System, `"Approve" または "Request Changes"`) {
		t.Fatal("Review Prompt does not make the strict parser contract explicit")
	}
}

func TestReviewBuilderHandlesOptionalContextDeterministically(t *testing.T) {
	fixture := loadReviewExecutionFixture(t)
	input := fixture.Input
	input.Deliverable.Frontmatter = review.DeliverableFrontmatter{}

	built, err := NewBuilder().BuildReview(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"- project: 未設定",
		"- task_id: 未設定",
		"- assignee_id: 未設定",
		"- runner: 未設定",
		"- executed_at: 未設定",
	} {
		if !strings.Contains(built.System+"\n"+built.User, expected) {
			t.Fatalf("BuildReview() did not contain %q", expected)
		}
	}
}

func TestReviewBuilderRejectsMissingOrSelfReviewIdentity(t *testing.T) {
	fixture := loadReviewExecutionFixture(t)
	input := fixture.Input
	input.SourceEmployee = nil
	if _, err := NewBuilder().BuildReview(context.Background(), input); err == nil {
		t.Fatal("BuildReview() accepted missing source employee")
	}
	input = fixture.Input
	input.Reviewer.EmployeeID = *input.Task.AssigneeID
	if _, err := NewBuilder().BuildReview(context.Background(), input); err == nil {
		t.Fatal("BuildReview() accepted self review")
	}
}

func TestReviewBuilderPreservesUnicodeSpecialCharactersAndContentNewlines(t *testing.T) {
	fixture := loadReviewExecutionFixture(t)
	input := fixture.Input
	input.Deliverable.Content = "\n# 成果物 | #見出し\n\n日本語 🧪\nline two\n"
	input.Deliverable.Frontmatter.Runner = "Claude|Runner #1"
	input.SourceEmployee.Name = "田中｜美咲 🧪\n共同担当"

	built, err := NewBuilder().BuildReview(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"元担当社員氏名: 田中｜美咲 🧪\n共同担当",
		"- runner: Claude|Runner #1",
		"## レビュー対象成果物\n\n# 成果物 | #見出し\n\n日本語 🧪\nline two\n\n指定された観点",
	} {
		if !strings.Contains(built.System+"\n"+built.User, expected) {
			t.Fatalf("BuildReview() did not preserve %q", expected)
		}
	}
}

func TestReviewBuilderRejectsMissingDeliverableAndHonorsCancellation(t *testing.T) {
	fixture := loadReviewExecutionFixture(t)
	input := fixture.Input
	input.Deliverable.Content = " \n"
	if _, err := NewBuilder().BuildReview(context.Background(), input); err == nil {
		t.Fatal("BuildReview() error = nil for empty deliverable")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewBuilder().BuildReview(ctx, fixture.Input); !errors.Is(err, context.Canceled) {
		t.Fatalf("BuildReview() error = %v, want context.Canceled", err)
	}
}

func loadReviewExecutionFixture(t *testing.T) reviewExecutionFixture {
	t.Helper()
	path := filepath.Join("..", "..", "..", "fixtures", "prompt", "review_execution.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read review prompt fixture: %v", err)
	}
	var fixture reviewExecutionFixture
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatalf("decode review prompt fixture: %v", err)
	}
	return fixture
}
