# ADR-0040: Reviewer RequirementをGo単一箇所で解決し、ReviewをTyped Decisionへ移行する

## Status

Accepted

## Context

ADR-0039でCEO Plan生成からLLM Canonical Output責務を撤去した後も、「誰がReviewするか」の決定は複数箇所に分散したままでした。

- `process/interaction_workflow.go`の`interactionReviewerIntent`は、CEO Planの`ProposedTasks`を走査し、`RequiredRole == "QA Engineer"`のTaskからMaker集合と「明示的Reviewer」を抽出していました。ADR-0039以降、`kind: "review"`のIntent stepは`proposed_tasks`へ変換されないため、「明示的Reviewer」枝は恒久的に到達不能でした。さらに、Maker集合をCEO Plan Approve時点の静的スナップショット（`ProposedTasks`）からしか導出していなかったため、`ExecuteTaskCreation`で直接作成されたTask（CEO Planを経由しないTask、Revisionで再割当されるTaskも同様）の担当者は、その人がMakerであってもReviewer候補から正しく除外されないというlatent bugがありました。
- 一方、直接/CLI/HTTPの`workflow-reviewed-plan|execute`（`PlanReviewedWorkflow`）は、呼び出し元が指定した`ReviewerID`をほぼ無検証で信頼していました。実在確認（`LoadEmployeeContext`）はありましたが、Maker除外チェックは一切なく、自己レビューは`review.PromptInput.Validate()`という個別Task実行時の防御でしか捕捉されませんでした。これは複数Task Workflowの途中まで実行が進んでから初めて失敗しうるという意味で、「Workflow実行が始まる前に安全停止する」という要件を満たしていませんでした。
- Review Provider契約は、人間向けMarkdown＋`REVIEW_RESULT_JSON_START`/`END`マーカー＋内部JSONという複合形式でした。実機Acceptanceで`marker_missing`等の契約境界失敗が繰り返し発生していました。調査の結果、canonical `Reviews/<taskID>.review.json`はすでに`Decision{Verdict, Issues}`のみを永続化しており、マーカーはRunnerの生応答テキストにしか存在しないこと、`review.ParseOutput`（マーカー解析）の本番呼び出し箇所が`review_service.go`の1箇所のみであり、Recovery（`recovery_snapshot.go`）・Revision（`revision.go`）はいずれも既にcommit済みのcanonical JSONを`review.DecodeDecision`経由で読むだけでマーカーに一切依存していないことが判明しました。

## Decision

### Reviewer Requirementの所有者と解決箇所

Reviewer Requirement（誰がReviewするかというPolicy）はGo（`process`パッケージ）が単独で所有します。既存の`organization.ResolveReviewerAssignment`（Maker除外＋Autonomy allow-list交差＋一意Role解決）をそのまま再利用し、新しいResolverは実装しません。

```text
process.taskMakerIDs(tasks []task.Task) ([]string, error)   -- 新設・純粋関数
  「今アクティブなTaskの担当者」という唯一の定義
  非完了Taskの担当者を重複排除して返す。担当者未設定はvault.ErrAssigneeMissing

resolveInteractionWorkflowReviewer  -- 候補選定（1回だけ）
  live Task Storeから taskMakerIDs でMaker集合を取得（CEO Planは一切読まない）
  organization.ResolveReviewerAssignment(ProposedEmployeeID: nil) で一意解決
  0件/複数件は ErrInteractionWorkflowReviewerRequired として型付き安全拒否

PlanReviewedWorkflow  -- 検証（全entry pathが必ず通過するゲート）
  live Task Storeから taskMakerIDs でMaker集合を取得
  input.ReviewerID がMaker集合に含まれれば ErrReviewedWorkflowReviewerIsMaker
  （実在確認は既存の loader.LoadEmployeeContext のまま）
```

`interactionReviewerIntent`（CEO Plan Taskスキャン）は完全に削除しました。`resolveInteractionWorkflowReviewer`はもはや`ceoplan.Plan`を一切受け取りません。

Interaction path（自動導出）とdirect/CLI/HTTP path（明示的Reviewer ID）は異なる信頼モデルを持ちます。

- Interaction pathはCEOからの非技術的な依頼を扱うため、Reviewer IDを人間やLLMが指定することは一切なく、常にGoのPolicyが一意候補を選定します（`ProposedEmployeeID: nil`）。
- Direct/CLI/HTTP pathはADR-0024で「Reviewer employee IDは必須のtyped input、name/roleからの推測は行わない」と明示的に設計されており、これは意図的に維持します。`organization.ResolveReviewerAssignment`の`ProposedEmployeeID`分岐（`assignment.go:143-154`）は、指定された候補がその時点でRole保有者の中で一意でなければ`AssignmentAmbiguous`を返す実装になっており、これをdirect pathへそのまま適用すると「同じRoleの社員が複数いる状況で特定の1人を明示的に指名する」という正当な運用が壊れます。そのため`PlanReviewedWorkflow`は`ResolveReviewerAssignment`を呼ばず、`taskMakerIDs`による単純なMaker集合メンバーシップ判定のみを追加しました。Role検証・Autonomy allow-list検証はdirect pathには意図的に追加していません（Rejected Alternativesを参照）。

`ExecuteInteractionWorkflow`が`PlanInteractionWorkflow`を再実行し`currentPlan.ReviewerID == input.ReviewerID`を要求する既存の仕組み（`interaction_workflow.go:210-216`）は変更していません。これは`WorkflowPlanDigest`と同系統の「Approve時点から乖離していないか」を確認するCAS的な安全策であり、実行に使われる値は常にこの再計算結果（Goのdeterministic Policy）から来ます。呼び出し元が新しい候補を選び直せる余地はなく、「LLM/UIがEmployee IDを選ぶ」ことには当たりません。この仕組み自体は今回のスコープ外として維持しました。

### Typed Review Decision契約

LLMが返すReview結果は、マーカー・人間向けMarkdownを含まない最小のflat JSONへ変更しました。

```json
{"verdict": "Approve" | "Request Changes", "issues": [...], "summary": "..."}
```

- `review.Decision`に`Summary string \`json:"summary,omitempty"\``を追加。`Decision.Validate()`では非空を要求しません。これは移行前にcommit済みのcanonical Review JSON（summaryキーが存在しない）が`DecodeDecision`（Revision/Recovery用）で読めなくなることを防ぐためです。新規生成されるReviewは常に非空のsummaryを持ちますが、それは次段の`ParseTypedDecision`が保証します。
- `review.ParseTypedDecision(content string) (Decision, error)`を新設。`ceoplan.ParseIntent`と同じ厳格さ（`DisallowUnknownFields`、単一JSONオブジェクト、末尾データ禁止）で、summaryの非空も要求します。共有の`parseDecision(content []byte, requireSummary bool)`にverdict/issues検証ロジックを残し、`DecodeDecision`（`requireSummary=false`）と重複させていません。
- `review.OutputJSONSchema()`/`StructuredOutputContentField`（マーカー付きMarkdownを1つのstring fieldへラップしていた前Checkpointの機構）を削除し、`review.TypedDecisionJSONSchema()`に置き換えました。schema自体の出力がそのままRunner Contentになるため、`ContentField`は不要です（`ceoplan.IntentJSONSchema()`と同じ使い方）。Provider固有の`output_config.format`変換は引き続き`adapter/claude`にのみ存在し、この変更でも一切触れていません。
- `ExecutionResult`から`HumanMarkdown` fieldを削除しました。

### 責務分担

| 責務 | 旧 | 新 |
|---|---|---|
| verdict / issues | LLM | LLM（不変） |
| 判定理由の要約 | LLMがMarkdown本文として自由記述 | LLMが`summary`として1 field で簡潔に記述 |
| マーカー配置・出力順序 | LLM | 廃止（構造自体が不要に） |
| 人間向けMarkdownのレイアウト | LLM | Go（`renderReviewBody`が`Decision`から決定的に生成） |
| Task ID / Reviewer ID / Review ID / artifact path / canonical metadata | Go（既存のまま） | Go（既存のまま、変更なし） |

### 人間向けMarkdown投影

`adapter/vault/review_store.go`の`renderReviewProjection`は、front matter（8フィールド、`---`区切り）とファイル命名（`Reviews/<taskID>[.<version>].review.{json,md}`）をADR-0010どおり無変更のまま維持しています。本文のみ、`document.Execution.HumanMarkdown`（LLM自由記述）から`renderReviewBody(decision)`（`## 概要` → Summary、`## 指摘事項` → Issuesの決定的箇条書き、`## 判定` → Verdict、いずれもGo側で組み立て）へ置き換えました。Canonical JSONのcommit（`json.MarshalIndent(document.Execution.Decision, ...)`）はcommit順序・commit内容ともに無変更で、`Summary`フィールドの追加は既存フィールドに影響しない加法的変更です。

### マーカープロトコルの廃棄

`ResultJSONStart`/`ResultJSONEnd`/`ParseOutput`/`ParseFailureMarkerMissing`/`ParseFailureMarkerDuplicate`/`ParseFailureHumanMarkdownMissing`をすべて削除しました。事前調査で本番呼び出し箇所が`review_service.go`の1箇所のみと判明しており、Recovery/Revisionはcommit済みcanonical JSONしか読まないため依存していません。

**到達不能性の証明方法**: Goは静的コンパイル言語であるため、該当シンボルを削除した上でリポジトリ全体の`go build ./...`/`go vet ./...`が成功することは、ランタイムテストより強い証拠になります。他に参照が残っていればコンパイルエラーとして即座に検出されるためです。この方法を採用し、実際に全パッケージのビルド・vet・テストが成功することを確認しました（該当箇所を参照していたテストは新しいflat JSON契約へ機械的に更新済み）。

### 失敗伝播

`process/review.go`の`WorkerErrorInvalidReviewResult` → `REVIEW_RESULT_INVALID`/`review_result_parser`という分類、および`errors.As(err, &review.ParseError{})`による`ParseFailureReason`抽出は、具体的なReason文字列に一切依存しない汎用実装のままでした。マーカー系Reasonの削除・`unknown_field`/`object_required`の追加は、この伝播層に変更を要しませんでした。`reviewedWorkflowFailureClassification`（`reviewed_workflow.go`）も同様に子Reviewの`FailureCode`/`FailureStage`をそのまま転送する既存実装のままです。新しいReviewer-is-Maker preflight失敗は、既存の`PlanReviewedWorkflow`エラー全般を`REVIEWED_WORKFLOW_PREFLIGHT_FAILED`/`preflight`に分類する仕組み（`ExecuteReviewedWorkflow`の既存preflight呼び出し）にそのまま乗るため、httpapi/CLI層の変更は不要でした。

### Rejected Alternatives

- **Direct/CLI/HTTP pathにもRole・Autonomy allow-list検証を追加する**: ADR-0024の「明示的typed input、name/roleから推測しない」という設計と衝突し、正当な運用（同一Role複数名から特定の1人を明示指名）を壊すため見送りました。Maker除外＋実在確認のみを追加する現在の設計を採用しています。
- **Reviewer解決を新しいResolver/パッケージとして再実装する**: `organization.ResolveReviewerAssignment`が既にMaker除外＋Autonomy allow-list交差＋一意Role解決を実装しており、再実装は重複です。既存資産を再利用しました。
- **`ExecuteInteractionWorkflow`の再Plan＋比較ロジックを、単純な保存済みID読み取りへ置き換える**: `WorkflowPlanDigest`と同系統の、Approve時点からの乖離検出という既存の安全設計を壊すリスクがあり、今回のスコープの本質（CEO-Plan-Task由来のReviewer導出の撤去、direct pathの無検証Maker受け入れの是正）とは別の問題であるため見送りました。
- **動的なClarification経由でのReviewer曖昧性解決**: ADR-0039のCEO Plan Assignment曖昧性と同じ理由（既存Clarification loopとの新規結線が必要になりscopeを大きく広げるため）で見送り、型付き安全拒否を採用しました。

## Consequences

- Reviewer Requirementの決定はGoの単一Policy（`taskMakerIDs` + `organization.ResolveReviewerAssignment`）に集約され、CEO Plan Task由来の推測経路は構造的に存在しなくなりました。
- Revisionで再割当されるTaskや`ExecuteTaskCreation`で直接作成されたTaskの担当者も、常にlive Task Storeから正しくMaker除外されるようになりました（latent bugの修正）。
- Direct/CLI/HTTP pathの自己レビューは、個別Review実行時ではなくWorkflow Plan時点（実行開始前）で検出されるようになりました。
- Review Provider契約はverdict/issues/summaryの3 fieldのみに縮小され、マーカー・Markdownレイアウトに起因する契約境界失敗（`marker_missing`等）は構造的に発生し得なくなりました。
- 人間向けMarkdownは常にGoが決定的に生成するため、レイアウトの一貫性・再現性が保証されます。
- Canonical Review JSON Contract・Task lifecycle・CAS・Command Ledger・Audit/Event・Recovery semanticsはいずれも無変更です。
- Structured Outputsは「小さく安定した契約を受け取る」ための手段として、CEO PlanとReviewの両方で同じ設計原則（Domain-owned schema、Provider翻訳はAdapter境界のみ）を共有するようになりました。
