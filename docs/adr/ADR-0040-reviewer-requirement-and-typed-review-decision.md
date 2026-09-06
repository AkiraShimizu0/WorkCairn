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
| 判定理由の要約 | LLMがMarkdown本文として自由記述 | LLMが`summary`として1 fieldで簡潔に記述（当初の設計。PB-3bo.3／PB-3bo.5で`summary`はverdict別の固定outcome labelへ変更され、LLMによる自由記述の要約はfresh Reviewではもう収集されない。指摘の具体的内容は引き続き`issues`へLLMが記述する。詳細は各節参照） |
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

### PB-3bo.1 Correction: summaryをverdict別の固定constへ閉じる

Codex PB-3bo診断のP1: 上記の`summary`はGo側`ParseTypedDecision`（`parseDecision(content, requireSummary=true)`）が非空を厳格に要求する一方、Provider Structured Output Schema（`review.TypedDecisionJSONSchema`）側はSchema自体で非空を強制できていませんでした（Anthropic対応Schema subsetは`minLength`／`pattern`を持たないため、`summary`は単なる`{"type":"string"}`のままでした）。Providerが仮に空文字列のsummaryを構造的に正しいStructured Outputとして生成した場合、Schema上は合法であるにもかかわらずGo側が`missing_required_field/summary`として拒否し、verdict・issuesの判定内容自体は有効なReviewを丸ごと失敗させる、というSchemaとGo parserの意味論的契約の不一致がありました。

この不一致を、Schema側をGo側の既存の厳格さへ合わせる形で解消しました。`verdict`フィールドが本ADR時点で既にこのSchema自身で`const`（closed value）を使っており（`ceoplan`のstep `kind`フィールドでも同様に使用済み）、Anthropic対応Schema subsetで`const`が利用可能であることは実装・実際のRequest fixture・testで既に実証されていました。この事実を利用し、`anyOf`の各verdict別branchの`summary`フィールドを、verdictごとに異なる固定`const`文字列へ閉じました（`review.SummaryApprove`／`review.SummaryRequestChanges`）。

- Approve branch: `"レビューの結果、問題は見つかりませんでした。"`
- Request Changes branch: `"レビューの結果、修正が必要な指摘があります。詳細は下記の指摘事項を参照してください。"`

`const`は非空文字列であるため、Provider側のStructured Output生成自体がこの制約を機械的に強制し、空文字列のsummaryはSchema違反として構造的に発生し得なくなります。`summary`はverdictの固定ラベルとしての性質が明確になり、`renderReviewBody`の`## 概要`セクションには引き続き表示されます。Promptの出力例・ルール説明もこの2つの固定値と完全に一致するよう同期しました（`prompt/review.go`）。（PB-3bo.3補記: 本節はここで当初「詳細な判定理由・具体的な指摘はissues[]にすべて保持されるため、Review evidenceが失われることはない」と一般化して記述していましたが、これはRequest Changesの個別指摘についてのみ正しく、Approveについては不正確でした。正確な記述はPB-3bo.3節を参照してください。）

Go側（`ParseTypedDecision`／`Decision.Validate`）は変更していません。非空チェックは既存のまま維持し、summaryが特定のconst値と一致するかどうかを追加検証する新しいロジックは導入していません（Provider Schemaによる強制で十分であり、Go側で二重に強制することはscopeを広げるため見送りました）。空文字列・空白のみのsummaryは、これまでどおり`ParseFailureMissingRequiredField`として拒否されます。

本Correctionは`interaction.ProfileBoundedAcceptance`（ADR-0072）のPlan1回・Task1件・Review1回・最大3 attempts・Revision禁止契約には一切影響しません。PB-3ag／PB-3jで観測されたProvider failure根本原因の修正、実Providerでの成功確認、Public Beta GOへの近接のいずれも主張しません。Codex PB-3bo診断のP2（stop reasonの永続化）は本Correctionのscope外であり、別Checkpointで扱います。

変更ファイル: `go/internal/review/schema.go`（`SummaryApprove`／`SummaryRequestChanges`定数の新設、`summary`フィールドへの`const`追加）、`go/internal/prompt/review.go`（Promptのルール説明・出力例を同期）、`fixtures/provider/claude_review_typed_decision_request_v1.json`・`fixtures/prompt/review_execution.json`（固定fixtureの同期）、および対応するGo test（`go/internal/review/schema_test.go`、`go/internal/adapter/claude/runner_test.go`、`go/internal/prompt/review_test.go`）。`go/internal/review/result.go`（parser本体）・`go/internal/service/review_service.go`は変更していません。

### PB-3bo.3 Correction: Go境界でも固定summaryを強制し、PB-3bo.1の記述を訂正する

Codex PB-3bo.2診断のP1: PB-3bo.1はProvider Structured Output Schema側だけを`const`で閉じ、Go側（`review.ParseTypedDecision`）は「非空であること」だけを検証し続けていました。この状態では、実Providerの`const`強制を経由しない経路（Structured Outputsを使わない旧経路、テスト、あるいは将来の別Provider実装）が、任意の非空summaryや反対verdict用summaryをGoにそのまま受理させる余地が残っていました。`parseDecision(..., requireSummary=true)`（`ParseTypedDecision`が使う、fresh Reviewの経路）に、canonical verdictへの対応関係チェックを追加しました。

- `Approve` → `candidate.Summary`は`review.SummaryApprove`と（トリムなしで）完全一致しなければならない
- `Request Changes` → `candidate.Summary`は`review.SummaryRequestChanges`と（トリムなしで）完全一致しなければならない

この一致判定はtrimされていない生の値に対して行うため、固定値へ前後空白を加えた値（例: `" レビューの結果、問題は見つかりませんでした。"`）もProvider Schemaの`const`強制と同じ意味で拒否されます。空文字列・空白のみのsummaryは、既存の`ParseFailureMissingRequiredField`判定がこの新しいチェックより先に走るため、従来どおりのReasonで拒否され続けます。不一致の場合は新しい閉じたReason `review.ParseFailureInvalidSummary`（`"invalid_summary"`）を`Field: "summary"`とともに返します。既存のsanitized ParseError／FailureEnvelope伝播経路（`reviewFailureEnvelope`等）はReason／Fieldだけを転送する既存の汎用実装のままであり、このために新しい配線は追加していません——不正なsummary本文自体は、Result・Command Ledger・Audit Log・Eventのいずれにも一切保存・表示されません。`DecodeDecision`（`requireSummary=false`、historical record再読込専用）はこのチェックを一切実行せず、summaryキー欠如を含む既存のcanonical Review JSONを引き続きそのままdecodeできます。parserの緩和・空値の補完・retry・fallback・追加Provider callはいずれも実装していません。

**PB-3bo.1の記述の訂正（Codex PB-3bo.2診断のP2）**: PB-3bo.1節は「詳細な判定理由・具体的な指摘はissues[]にすべて保持されるため、この変更でReview evidenceが失われることはない」と一般化して記述していましたが、これは不正確でした。正確には次のとおりです。

- **Request Changes**については、指摘の具体的内容（何が問題か、なぜか、どう直すか）は`issues[]`（category／severity／description／suggested_action）に引き続き保持されます。この部分は失われません。
- **Approve**については、PB-3bo.1以前は`summary`に「なぜ問題なしと判断したか」という自由記述の肯定的理由をLLMが記述できましたが、fresh Reviewではこれはもう収集されません。`summary`はverdict確定と同時に決まる固定outcome labelであり、Approve判定固有の理由・根拠を運ぶfieldではなくなりました。これは本Correctionが実際にもたらす、範囲の限定された情報量の削減です。
- **JSON Contract自体**（field名`summary`、JSON型`string`、`Decision{Verdict, Issues, Summary}`構造体、`json:"summary,omitempty"`タグ、canonical JSON commit順序、`DecodeDecision`のhistorical/pre-migration decode互換）はいずれも不変です。変わったのは、fresh Reviewにおいて`summary`が取りうる値の意味論（自由記述の要約 → verdictに1対1で対応する固定outcome label）だけです。
- 上の「Canonical Review JSON Contract・Task lifecycle・CAS・Command Ledger・Audit/Event・Recovery semanticsはいずれも無変更です」というConsequencesの記述は、field／type／永続化経路のレベルでは引き続き正しいですが、「Canonical Review JSON Contractが全面的に無変更」と読める書き方は誤解を招くため、このCorrectionで上記のとおり限定します。

本CorrectionもPB-3ag／PB-3jで観測されたProvider failure根本原因の修正、実Providerでの成功確認、Public Beta GOへの近接のいずれも主張しません。Codex PB-3bo診断のP2（stop reasonの永続化）は引き続きscope外です。

変更ファイル: `go/internal/review/result.go`（`ParseFailureInvalidSummary`定数、`summaryForVerdict`、`parseDecision`への一致判定追加）、`go/internal/review/result_test.go`（positive/negative/legacy-compatibility test群）、`go/internal/process/review_test.go`（FailureEnvelope伝播・非露出のend-to-endテスト2件追加、既存fixtureのsummary同期）、`go/internal/process/reviewed_workflow_test.go`・`go/internal/process/interaction_recover_revision_test.go`・`go/internal/process/conversation_test.go`（共有mock helperおよびgoldenのsummary同期）、`go/internal/service/review_service_test.go`・`go/internal/httpapi/handler_test.go`・`go/internal/httpapi/interaction_bounded_http_test.go`・`go/cmd/workcairn/main_test.go`（fake Runner／production-path fixtureのsummary同期）、`go/internal/synthesisacceptance/scenario_v1.json`（Synthesis Acceptance harnessのReview baseline同期）、`fixtures/provider/browser_acceptance_v1.json`（実daemon binaryを使うBrowser Gateが同じ厳格なGo parserを通るため、23箇所のsummaryを同期）、`tests/browser/conversation.spec.mjs`（同期後のsummary文字列に合わせたUI-copy assertion更新）。

### PB-3bo.5 Correction: fresh Reviewの有効な組合せをApprove+空issuesとRequest Changes+非空issuesの2つだけへ閉じる

Codex PB-3bo.4診断のP0/P1: PB-3bo.3までの時点で、`summary`はverdict別に閉じられていましたが、`issues`とverdictの組合せ自体は閉じられていませんでした。具体的には、fresh Reviewが`{"verdict":"Approve","issues":[{...}],"summary":"<Approveの固定summary>"}`という、verdictはApproveでissuesが非空という組合せをGo側が受理してしまう余地が残っていました（`Decision.Validate()`はRequest Changesがissuesを要求する方向しか検証しておらず、逆方向のApprove+issuesは制約されていませんでした）。

`parseDecision(..., requireSummary=true)`（fresh Reviewの経路のみ、`DecodeDecision`は対象外）へ、verdictがApproveかつ`candidate.Issues`が1件以上であれば拒否する判定を追加しました。この判定はsummary判定より前、かつissuesの個々のfield（category／severity／description／suggested_action）を読む前に行われるため、拒否されるApprove応答のissue本文が一切検証されず、したがって一切露出しません。新しい閉じたReason `review.ParseFailureIssuesForbidden`（`"approve_issues_forbidden"`）を`Field: "issues"`とともに返します。既存のsanitized ParseError／FailureEnvelope伝播経路は変更しておらず、この新しいReasonもReason／Fieldだけを転送する既存の汎用実装にそのまま乗ります。

これにより、fresh Reviewが到達しうる有効な組合せは次の2つだけに閉じられました。

- `Approve` ＋ `issues=[]` ＋ `SummaryApprove`
- `Request Changes` ＋ 1件以上のissues ＋ `SummaryRequestChanges`

**Schema側の対応する強制について**: `TypedDecisionJSONSchema`のRequest Changes branchは既存の`minItems: 1`（Anthropic対応Schema subsetで実証済み）でissues非空を強制していますが、Approve branchのissuesを「空配列のみ」へSchema側で機械強制する対応するキーワード（`maxItems`など）は、本Checkpoint時点でリポジトリ内に実証されたfixture・実装例が一切ありませんでした（`minItems`は`review/schema.go`自身が使用実績を持ちますが、`maxItems`はリポジトリ全体を検索しても使用例が存在しません）。要件が「Provider対応が実証済みの場合だけ追加し、未実証keywordは追加しない」と明示的に限定しているため、Approve branchへのSchema側の空配列強制は追加していません。Go側の`ParseTypedDecision`によるこのCheckpointの強制が、この組合せの唯一の機械的な保証です。

Go側（`Decision.Validate()`）は変更していません。この判定はfresh Review boundaryにのみ追加し、`Validate()`（`DecodeDecision`と共有）には追加していません——`DecodeDecision`（`requireSummary=false`、historical record再読込専用）はこの判定を一切実行せず、Approveが非空issuesを持つ既存のcanonical Review JSON（もし過去に存在していれば）や、PB-3bo.3以前の自由記述summaryを持つ記録を、引き続きそのままdecodeできます。parserの緩和・空値の補完・retry・fallback・追加Provider callはいずれも実装していません。

**PB-3bo.1／PB-3bo.3の記述への追記**: 上記PB-3bo.3節の「Request Changesについては、指摘の具体的内容は`issues[]`に引き続き保持される」という記述は本Correctionでも引き続き正しく、追加の訂正は不要です。ADR全体を通じて、Approveのsummaryは固定outcome labelであり自由記述の肯定的理由をfresh Reviewでは収集しないこと、Request Changesの具体的指摘は`issues`へ保持されることの2点が、このCheckpoint時点での正確な記述です。

本CorrectionもPB-3ag／PB-3jで観測されたProvider failure根本原因の修正、実Providerでの成功確認、Public Beta GOへの近接のいずれも主張しません。Codex PB-3bo診断のP2（stop reasonの永続化）、retry、fallback、Revision、Provider routing、Keychain、bounded profile契約（ADR-0072）はいずれも変更していません。

変更ファイル: `go/internal/review/result.go`（`ParseFailureIssuesForbidden`定数、`parseDecision`へのApprove+issues拒否判定追加、`Decision.Summary`／`ParseError.Field`のdocコメント更新）、`go/internal/review/result_test.go`（positive/negative/legacy-compatibility test追加、`TestParseTypedDecisionParseErrorFieldOnlyForMissingRequiredField`を`TestParseTypedDecisionParseErrorFieldScopedToFieldSpecificReasons`へ改名しケース追加）、`go/internal/process/review.go`（`ReviewExecutionResult.ParseFailureField`のdocコメント更新）、`go/internal/review/orchestration.go`（`OrchestrationResult.ParseFailureField`のdocコメント更新）、`go/internal/process/review_test.go`（FailureEnvelope伝播・非露出のend-to-endテスト2件追加）、`go/internal/prompt/review.go`（ルール3へApprove+issues=[]必須の明記）、`fixtures/prompt/review_execution.json`（golden promptの同期）。`go/internal/review/schema.go`は変更していません（Schema側実証キーワード不在のため、上記のとおり意図的に見送り）。既存fixture（`fixtures/provider/browser_acceptance_v1.json`、`fixtures/provider/claude_review_typed_decision_request_v1.json`、`go/internal/synthesisacceptance/scenario_v1.json`）は全Review responseをtruth tableに照らして確認済みで、Approve+非空issuesの組合せは1件も存在しなかったため変更していません。

## Consequences

- Reviewer Requirementの決定はGoの単一Policy（`taskMakerIDs` + `organization.ResolveReviewerAssignment`）に集約され、CEO Plan Task由来の推測経路は構造的に存在しなくなりました。
- Revisionで再割当されるTaskや`ExecuteTaskCreation`で直接作成されたTaskの担当者も、常にlive Task Storeから正しくMaker除外されるようになりました（latent bugの修正）。
- Direct/CLI/HTTP pathの自己レビューは、個別Review実行時ではなくWorkflow Plan時点（実行開始前）で検出されるようになりました。
- Review Provider契約はverdict/issues/summaryの3 fieldのみに縮小され、マーカー・Markdownレイアウトに起因する契約境界失敗（`marker_missing`等）は構造的に発生し得なくなりました。
- 人間向けMarkdownは常にGoが決定的に生成するため、レイアウトの一貫性・再現性が保証されます。
- Canonical Review JSON Contractは、field名・JSON型・commit順序・永続化経路（Task lifecycle・CAS・Command Ledger・Audit/Event・Recovery semantics）のレベルでは無変更です。ただし`summary`が取りうる値の意味論はPB-3bo.1／PB-3bo.3で変わっており（自由記述の要約 → verdict別の固定outcome label）、「JSON Contractが全面的に無変更」という意味では読めません（詳細はPB-3bo.3節）。
- Structured Outputsは「小さく安定した契約を受け取る」ための手段として、CEO PlanとReviewの両方で同じ設計原則（Domain-owned schema、Provider翻訳はAdapter境界のみ）を共有するようになりました。
- **JSON Contractへの影響（PB-3bo.1／PB-3bo.3／PB-3bo.5）**: `summary`／`issues` fieldはいずれもtop-level keyとして引き続き存在し、型（`string`／array）も無変更です。値の意味論だけが変わりました：`summary`は「自由記述の短い要約」から「verdictに1対1で対応する固定文字列」へ、`issues`とverdictの組合せは「Request Changesは1件以上」という片方向の制約だけから「Approveは必ず空、Request Changesは必ず1件以上」という双方向の閉じた制約へ。既存の`Decision{Verdict, Issues, Summary}`構造体・`json:"summary,omitempty"`タグ・canonical JSON commit順序・`DecodeDecision`のpre-migration互換（summaryキー欠如、Approve+非空issues、自由記述summaryのいずれも許容）はいずれも無変更です。Task ID／Reviewer ID／Review ID／artifact pathの契約にも影響しません。
