// Package prompt provides deterministic, provider-neutral prompt construction.
package prompt

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

var jst = time.FixedZone("JST", 9*60*60)

// Builder renders the versioned task-execution prompt from structured context.
// It performs no filesystem, Vault, Provider, or Runner operations.
type Builder struct{}

var _ worker.PromptBuilder = Builder{}

// NewBuilder returns a stateless task-execution prompt builder.
func NewBuilder() Builder { return Builder{} }

// Build converts structured Worker context into the stable task prompt.
func (builder Builder) Build(ctx context.Context, input worker.PromptInput) (worker.Prompt, error) {
	return builder.build(ctx, input, "成果物はMarkdownで出力してください。")
}

func (Builder) build(ctx context.Context, input worker.PromptInput, outputInstruction string) (worker.Prompt, error) {
	if ctx == nil {
		return worker.Prompt{}, fmt.Errorf("build prompt: nil context")
	}
	if err := ctx.Err(); err != nil {
		return worker.Prompt{}, err
	}
	if err := input.Employee.Validate(); err != nil {
		return worker.Prompt{}, fmt.Errorf("build prompt: %w", err)
	}
	if err := input.Task.Validate(); err != nil {
		return worker.Prompt{}, fmt.Errorf("build prompt: %w", err)
	}
	if input.CurrentTime.IsZero() {
		return worker.Prompt{}, fmt.Errorf("build prompt: %w: current datetime is required", worker.ErrInvalidRequest)
	}

	project := "プロジェクト名: " + input.Task.ProjectName
	if input.Task.ProjectOverview != "" {
		project += "\nプロジェクト概要: " + input.Task.ProjectOverview
	}
	assigneeID := "未割当"
	if input.Task.AssigneeID != nil && *input.Task.AssigneeID != "" {
		assigneeID = *input.Task.AssigneeID
	}

	sections := []string{
		"## 会社情報\n" + strings.Join([]string{
			"あなたはWorkspace社のAI社員です。",
			"CEOの依頼ではなく担当タスクを遂行してください。",
			outputInstruction,
			"不明点は推測せずTODOとして残してください。",
			"推測で事実を書かないでください。",
		}, "\n"),
		"## 社員情報\n" + strings.Join([]string{
			"氏名: " + input.Employee.Name,
			"部署: " + input.Employee.Department,
			"役割: " + input.Employee.Role,
			"使用モデル: " + input.Employee.Model,
		}, "\n"),
		"## 現在情報\n現在日時（JST）: " + input.CurrentTime.In(jst).Format("2006-01-02 15:04:05 JST"),
		"## プロジェクト情報\n" + project,
		"## タスク情報\n" + strings.Join([]string{
			"タスクID: " + input.Task.TaskID,
			"タイトル: " + input.Task.Title,
			"担当社員ID: " + assigneeID,
		}, "\n"),
	}

	return worker.Prompt{
		System: strings.Join(sections, "\n\n"),
		User: strings.Join([]string{
			"プロジェクト: " + input.Task.ProjectName,
			"タスクID: " + input.Task.TaskID,
			"担当タスク: " + input.Task.Title,
			"",
			"この担当タスクの成果物を作成してください。",
		}, "\n"),
	}, nil
}
