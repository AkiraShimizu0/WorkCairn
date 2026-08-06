# Review Flow

## 概要

ReviewerWorkerは、タスクの作成担当者とは別のAI社員として成果物を確認します。Worker、PromptBuilder、ModelRouter、Runnerを再利用し、人間向けMarkdownと検証可能な構造化JSONを生成します。

## 事前検証

- タスクIDが`TASK-<number>`形式
- `Deliverables/<TASK-ID>.md`が存在
- レビュー担当社員がOrganizationに存在
- レビュー担当者と元担当者が別社員
- 元担当社員IDが存在
- 同じ版のレビューMarkdownとJSONが未作成
- 明示的承認がある

`dry_run=True`では、成果物、担当者、Runner、モデル、プロンプト、保存予定パスを返し、RunnerやVaultを変更しません。

## Reviewer Prompt

PromptBuilderは通常の会社・社員・日時・プロジェクト・タスク情報に加え、次をレビューコンテキストへ含めます。

- 元タスクIDとタイトル
- 元担当社員のID、氏名、部署、役割
- レビュー担当社員のID、氏名、部署、役割
- 成果物Front Matter
- Project.mdの概要
- レビュー対象の成果物本文

Reviewerには、本文だけを根拠に作成者不明と判断せず、確認可能な矛盾だけを指摘するよう求めます。

## レビュー観点

- 要件漏れ
- 不明点
- 推測による記述
- 一貫性
- Markdown品質
- TODO不足
- MVPとして適切か

## 構造化結果

Runnerは人間向けMarkdownの後へ、次のマーカーでJSONを出力します。

```text
REVIEW_RESULT_JSON_START
{
  "verdict": "Approve または Request Changes",
  "issues": [
    {
      "category": "date|format|requirements|context|todo|other",
      "severity": "high|medium|low",
      "description": "指摘内容",
      "suggested_action": "修正案"
    }
  ]
}
REVIEW_RESULT_JSON_END
```

保存前にJSONを抽出・検証します。

- verdictは`Approve`または`Request Changes`のみ
- Request Changesではissuesが1件以上必要
- categoryとseverityは許可値のみ
- 不正JSONはレビュー保存前に拒否
- 人間向けMarkdownからJSONマーカー部分を除去

## 保存先とバージョン

- 人間向けレビュー: `Reviews/TASK-XXX.review.md`
- 構造化結果: `Reviews/TASK-XXX.review.json`
- バージョン付き: `TASK-XXX.review.v2.md`と`TASK-XXX.review.v2.json`

既存レビューと衝突する場合は上書きせず拒否します。バージョン付きレビューによりlegacyレビューを保持できます。

## 判定後

### Approve

レビューを保存し、WorkflowEngineは次の未着手タスクIDを返します。

### Request Changes

RevisionTaskServiceがJSONのissuesから修正タスクを1件作成します。

- 担当者は元タスクの`assignee_id`
- 元タスクID、元レビュー、判定、指摘一覧をRevisionsへ保存
- 同じレビューからの重複作成をロックとメタデータで拒否
- Tasks.mdへの追加とRevisionメタデータ作成を失敗時にロールバック

## 監査と失敗

レビュー実行はAudit LogへRunner、モデル、入出力トークン数、実行時間、状態を記録します。失敗時は不完全なレビューを残さず、元成果物とTasks.mdを変更しません。自動再実行は行いません。
