# Review Flow

## 概要

通常製品Reviewは、`workcairn review-*`からGo ReviewServiceを呼び、タスクの作成担当者とは別のAI社員Contextで成果物を確認します。Go PromptBuilder、Runner Registry、Runner Adapterを再利用し、検証可能な構造化JSONと人間向けMarkdownを生成します。

## 事前検証

- タスクIDが`TASK-<number>`形式
- `Deliverables/<TASK-ID>.md`が存在
- レビュー担当社員がOrganizationに存在
- レビュー担当者と元担当者が別社員
- 元担当社員IDが存在
- 同じ版のレビューMarkdownとJSONが未作成
- 明示的承認がある

`workcairn review-plan`では、対象、担当者、モデル、保存予定path、blocking reasonを返し、RunnerやVaultを変更しません。

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

ADR-0010に従い、構造化JSONをimmutable canonical evidenceとして先にcommitし、Markdownをhuman-readable projectionとして後にcommitします。JSON成功後のMarkdown失敗はpartial failureであり、JSONを削除しません。既存レビューと衝突する場合は上書き、adopt、自動修復をせず拒否します。

## 判定後

### Approve

レビューを保存し、Reviewed Workflowは次のTaskへ進みます。

### Request Changes

Request ChangesではReviewed Workflowが既存Go Revision orchestrationを呼びます。ADR-0012に従いimmutable intentを先にcommitし、TaskService.Create、`revision.created`、Audit subscriberの順で確定します。

- 担当者は元タスクの`assignee_id`
- 元タスクID、元レビュー、判定、指摘一覧をRevisionsへ保存
- 同じレビューからの重複作成を既存metadata検査と原子的createで拒否
- intent後のTask作成失敗、Task後のEvent失敗はrollbackせずpartial failureとして返す

## 監査と失敗

Review artifactはTask状態とTasks.mdを変更しません。ADR-0011 Review orchestrationはcanonical JSON commit後だけ`review.completed`を発行し、Vault Audit subscriberが保存します。projection失敗でもReview factは成立し、Event配送失敗もartifactを保持したpartial publication failureです。Review Store／orchestration自身は自動再実行、reconciliation、artifact adoptionを行いません。ADR-0021により、process edgeで明示Command IDを指定した場合だけterminal resultの再送を副作用なしで返します。
