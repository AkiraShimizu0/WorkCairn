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

ADR-0040に従い、Runnerはマーカーや人間向けMarkdownを含まない、flatなJSONオブジェクトだけを出力します（Anthropic Structured Outputsの制約に合わせ、`pattern`／`minLength`等の非対応キーワードは使いません）。PB-3bo.1で、`summary`はverdictごとに異なる固定`const`へ閉じました。`const`はAnthropic対応Schema subsetで利用可能（`verdict`自身が既に使用）なため、Provider Structured Output自体がこの値を機械的に強制し、空文字列や自由記述のsummaryは構造的に発生しなくなりました。PB-3bo.3で、Go側（`ParseTypedDecision`）もverdict別のこの固定値との完全一致を検証するようになり、Structured Outputsを経由しない経路でも同じ契約が強制されます。

Request Changesの個別の指摘内容は引き続き`issues`（category／severity／description／suggested_action）に記述されます。一方、`summary`はverdict確定と同時に決まる固定outcome labelであり、Approveについて以前LLMが記述できた「なぜ問題なしと判断したか」という自由記述の肯定的理由は、fresh Reviewではもう収集されません（詳細はADR-0040「PB-3bo.3 Correction」節）。

```text
{
  "verdict": "Approve",
  "issues": [],
  "summary": "レビューの結果、問題は見つかりませんでした。"
}
```

```text
{
  "verdict": "Request Changes",
  "issues": [
    {
      "category": "date|format|requirements|context|todo|other",
      "severity": "high|medium|low",
      "description": "指摘内容",
      "suggested_action": "修正案"
    }
  ],
  "summary": "レビューの結果、修正が必要な指摘があります。詳細は下記の指摘事項を参照してください。"
}
```

保存前にJSONを検証します。

- verdictは`Approve`または`Request Changes`のみ
- Approveではissuesが必ず空配列（PB-3bo.5。1件以上あれば`approve_issues_forbidden`として拒否）
- Request Changesではissuesが1件以上必要
- categoryとseverityは許可値のみ
- summaryは空文字列・空白のみ不可、かつverdictごとの固定文字列（`review.SummaryApprove`／`review.SummaryRequestChanges`）と完全一致しない限り不可（Go側`ParseTypedDecision`がPB-3bo.3以降、両方とも直接検証します。前後空白を加えた値・反対verdict用の値・任意の非空値はいずれも`invalid_summary`として拒否されます）
- 不正JSON・未知fieldはレビュー保存前に拒否

人間向けMarkdown（`Reviews/TASK-XXX.review.md`）はLLMが書きません。GoがcanonicalなDecision（verdict／issues／summary）から決定的に生成します。

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
