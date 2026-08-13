package prompt

import (
	"context"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/review"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

var _ review.PromptBuilder = Builder{}

// BuildReview renders the versioned review prompt from structured context. It
// performs no filesystem, Vault, Provider, Runner, or Task work.
func (builder Builder) BuildReview(ctx context.Context, input review.PromptInput) (worker.Prompt, error) {
	if ctx == nil {
		return worker.Prompt{}, fmt.Errorf("build review prompt: nil context")
	}
	if err := ctx.Err(); err != nil {
		return worker.Prompt{}, err
	}
	if err := input.Validate(); err != nil {
		return worker.Prompt{}, fmt.Errorf("build review prompt: %w", err)
	}

	base, err := builder.Build(ctx, worker.PromptInput{
		Employee:    input.Reviewer,
		Task:        input.Task,
		CurrentTime: input.CurrentTime,
	})
	if err != nil {
		return worker.Prompt{}, fmt.Errorf("build review prompt: %w", err)
	}

	unknown := func(value string) string {
		if value == "" {
			return "不明"
		}
		return value
	}
	unassigned := func(value string) string {
		if value == "" {
			return "未割当"
		}
		return value
	}
	unset := func(value string) string {
		if value == "" {
			return "未設定"
		}
		return value
	}

	var source worker.EmployeeContext
	if input.SourceEmployee != nil {
		source = *input.SourceEmployee
	}
	assigneeID := ""
	if input.Task.AssigneeID != nil {
		assigneeID = *input.Task.AssigneeID
	}
	reviewContext := strings.Join([]string{
		"元タスクID: " + input.Task.TaskID,
		"元タスクタイトル: " + input.Task.Title,
		"元担当社員ID: " + unassigned(assigneeID),
		"元担当社員氏名: " + unknown(source.Name),
		"元担当社員部署: " + unknown(source.Department),
		"元担当社員役割: " + unknown(source.Role),
		"レビュー担当社員ID: " + unknown(input.Reviewer.EmployeeID),
		"レビュー担当社員氏名: " + unknown(input.Reviewer.Name),
		"レビュー担当社員部署: " + unknown(input.Reviewer.Department),
		"レビュー担当社員役割: " + unknown(input.Reviewer.Role),
	}, "\n")
	reviewRules := strings.Join([]string{
		"あなたは成果物を客観的に確認するReviewerです。",
		"作成者情報は、元担当社員情報と照合してください。",
		"成果物本文だけを根拠に、作成者不明または推測と判定しないでください。",
		"executed_atと本文の日付が矛盾する場合のみ、日付の不整合を指摘してください。",
		"Project.mdに存在する既知情報が成果物へ反映されているか確認してください。",
		"推測ではなく、与えられたコンテキストから確認できる矛盾だけを指摘してください。",
		"次の観点をすべて確認してください。",
		"- 要件漏れ",
		"- 不明点",
		"- 推測による記述",
		"- 一貫性",
		"- Markdown品質",
		"- TODO不足",
		"- MVPとして適切か",
		"指摘には理由と具体的な修正案を含めてください。",
		"人間向けMarkdownの後に、指定されたマーカーでJSONを1つだけ出力してください。",
		"マーカー間にはJSONオブジェクトだけを置き、Markdown code fenceや説明文を含めないでください。",
		"開始・終了マーカーはそれぞれ正確に1回だけ使用してください。",
		"JSONのverdictはApproveまたはRequest Changesのみ使用してください。",
		"Request Changesの場合はissuesを1件以上含めてください。",
		"categoryはdate|format|requirements|context|todo|otherのみ使用してください。",
		"severityはhigh|medium|lowのみ使用してください。",
		"REVIEW_RESULT_JSON_START",
		`{"verdict":"Approve または Request Changes","issues":[`,
		`{"category":"date|format|requirements|context|todo|other",`,
		`"severity":"high|medium|low","description":"指摘内容",`,
		`"suggested_action":"修正案"}]}`,
		"REVIEW_RESULT_JSON_END",
	}, "\n")

	frontmatter := input.Deliverable.Frontmatter
	frontmatterLines := strings.Join([]string{
		"- project: " + unset(frontmatter.Project),
		"- task_id: " + unset(frontmatter.TaskID),
		"- assignee_id: " + unset(frontmatter.AssigneeID),
		"- runner: " + unset(frontmatter.Runner),
		"- executed_at: " + unset(frontmatter.ExecutedAt),
	}, "\n")

	return worker.Prompt{
		System: base.System + "\n\n## レビューコンテキスト\n" + reviewContext +
			"\n\n## レビュー方針\n" + reviewRules,
		User: strings.Join([]string{
			"プロジェクト: " + input.Task.ProjectName,
			"タスクID: " + input.Task.TaskID,
			"レビュー対象: " + input.Task.Title,
			"",
			"## 成果物Front Matter",
			"",
			frontmatterLines,
			"",
			"## レビュー対象成果物",
			"",
			strings.TrimSpace(input.Deliverable.Content),
			"",
			"指定された観点でレビューしてください。",
		}, "\n"),
	}, nil
}
