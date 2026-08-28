package prompt

import (
	"context"
	"fmt"
	"strings"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/worker"
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

	base, err := builder.build(ctx, worker.PromptInput{
		Employee:    input.Reviewer,
		Task:        input.Task,
		CurrentTime: input.CurrentTime,
	}, "レビュー結果は、後述するJSON形式だけで出力してください。")
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
		"",
		"## 出力形式（例外なし）",
		"応答全体をJSONオブジェクト1つだけにしてください。前後にMarkdown、code fence（```）、説明文、コメントを一切含めないでください。",
		"人間向けのレビュー本文（Markdown）はWorkCairnが機械的に生成するため、あなたは作成しません。",
		"1. JSONのtop-level fieldはverdict・issues・summaryの3つだけにしてください。それ以外のfieldを追加しないでください。",
		`2. verdictの値は、次の2つの文字列のどちらか一方をそのまま使用してください（言い換え・翻訳・組み合わせは禁止）: "Approve" または "Request Changes"`,
		"3. issuesは常にJSON配列にしてください（指摘がない場合は空配列[]、verdictがRequest Changesの場合は1件以上）。",
		"4. issuesの各要素は、category・severity・description・suggested_actionの4 fieldだけを持つオブジェクトにしてください。",
		"5. categoryの値は date, format, requirements, context, todo, other のいずれか1つの文字列にしてください。",
		"6. severityの値は high, medium, low のいずれか1つの文字列にしてください。",
		"7. descriptionとsuggested_actionは空文字列にせず、具体的な内容を記述してください。",
		"8. summaryは、今回の判定理由を短く要約した文字列にしてください。空文字列は禁止です。",
		"",
		"## 出力例（構造の見本です。この値をそのままコピーせず、あなた自身の判断内容に差し替えてください）",
		`{"verdict":"Request Changes","issues":[{"category":"requirements","severity":"medium","description":"要件Xへの言及がありません。","suggested_action":"要件Xについて追記してください。"}],"summary":"要件Xの記載漏れのため修正を依頼します。"}`,
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
