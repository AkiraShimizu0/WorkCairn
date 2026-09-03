# ADR-0058: Provider Output Completeness Policy — Truncated Output Is Never Silent Success

## Status

Accepted

## Context

A real Claude one-shot Synthesis Acceptance benchmark (`claude-sonnet-5`, scenario `public-beta-product-growth-ja-v1`) observed `StopReason=max_tokens` with `Content` non-empty. The generated Deliverable stopped mid-sentence, before its top priority's validation method and before later priorities were ever written.

Investigation of the existing call chain found two separate, previously-undocumented facts:

1. `internal/adapter/claude/runner.go`'s `defaultMaxTokens = 3000` is, in practice, the output-token ceiling for every Claude call in the system today — no caller (production CLI, production daemon, or the Synthesis Acceptance harness) ever sets `ClaudeProcessConfig.MaxTokens` to a non-zero value, so the same default applies everywhere. This ADR does not change that value (see Non-goals).
2. `worker.RunResult.Validate()`/`worker.ExecutionResult.Validate()` never inspected `StopReason`. As long as `Content` was non-empty, a response cut off by `max_tokens` passed validation identically to a genuinely complete one. `ExecutionService.Execute()` treated any `workerErr == nil` result as an ordinary success: it saved the (truncated) content as the canonical Deliverable and called `TaskService.Complete`. A real Task's output could therefore be silently committed as "done" while missing content the model never got to write.

This is a `Failure / Partial Completion Observability` gap, not a Synthesis-specific quality problem. `docs/CONSTITUTION.md` Article 8 ("失敗や部分成功を成功として隠さない") already forbids exactly this shape of silent success; this ADR closes a concrete, previously-undetected instance of it.

The central distinction this ADR insists on: **a Provider call succeeding (HTTP 200, valid response, no `claude.Error`) and a Task's output being complete are two different questions.** `max_tokens` is not a Provider call failure — the Runner did its job correctly and reported the fact accurately (`worker.StopReasonMaxTokens`, added additively in the prior Checkpoint). The judgment that this output is not acceptable as a finished Task deliverable belongs one layer up, in production orchestration — never in the Runner, and never expressed by inventing a new Task lifecycle state.

### 既に決定済み・実装済みで、本ADRが変更しない事項

- **`worker.StopReason`とそのClaude Adapter mapping**（前Checkpoint）は無変更です。本ADRはこの既存Provider-neutral型を読むだけで、新しい分類語彙を追加しません。
- **TaskServiceがTask状態変更・Task lifecycle Eventの唯一の所有者である**という原則（CONSTITUTION Article 6）は無変更です。本ADRの新しい判断も、既存の`TaskLifecycleService.Fail`/`.Hold`呼び出しを1回追加するだけで、これを経由しない状態変更は一切行いません。
- **`policy.ExecutionPolicy`（`HoldOnFailurePolicy`）とFail→Holdの既存recovery sequence**（`ExecutionService.recoverExecutionFailure`）は無変更です。本ADRの新しい失敗種別も、既存の全ての実行失敗と全く同じこのsequenceを通ります。
- **`failure.Envelope`とそのchild→outer伝播**（ADR-0041）は無変更です。本ADRは`executionFailureEnvelope`が既に汎用的に処理する`*execution.ExecutionError`の新しい`Kind`を1つ追加するだけで、伝播ロジック自体（Reviewed Workflow、Command Ledger、HTTP、UI）には一切触れません。
- **`defaultMaxTokens = 3000`**は変更しません（Non-goals参照）。

### 新たに決定が必要な事項（本ADRのDecision）

1. どのlayerが「truncated outputをTask成功としてacceptしない」と判断するか。
2. 既存のFail/Hold語彙をそのまま使うか、新しいTask stateが必要か。
3. truncated contentをcanonical Deliverableとして保存するか。
4. Provider-neutralな型付き表現の具体形（新しいErrorKind／Stage／Category）。
5. Synthesis Acceptance harness固有の観測（StopReason／OutputTruncated）を、production pathでも同様に確保する方法。

## Decision

### 1. 判断layer: ExecutionService、Runner/WorkerServiceではない

`internal/service/execution_service.go`の`Execute()`は、既に「WorkerからContentを受け取る → Deliverableをsaveする → TaskService.Completeを呼ぶ」という一連の判断を行う唯一の場所であり、既に`recoverWorkerFailure`/`recoverExecutionFailure`という型付き失敗処理を持っています。本ADRはこの既存の判断点へ、`workerResult.StopReason == worker.StopReasonMaxTokens`という1つの新しい条件分岐を追加するだけです。

RunnerとWorkerServiceは無変更のままです——RunnerはProvider呼び出しとresponse mappingだけを担当し続け（`worker.StopReason`を正確に報告する既存責務のまま）、Task lifecycle・Approval・Retry・Recovery・Deliverable保存のいずれも知りません。

### 2. 既存Fail/Hold語彙をそのまま再利用、新しいTask stateは追加しない

`StopReason == max_tokens`のとき、`ExecutionService`は`recoverExecutionFailure(ctx, request, result, execution.StageWorker, "OUTPUT_INCOMPLETE", "provider_output_incomplete_max_tokens", execution.ErrorOutputIncomplete, ErrProviderOutputIncomplete)`を呼びます。これは他のあらゆる実行失敗と全く同じ経路です：

1. `TaskService.Fail(...)`——実行事実（この試行は完了しなかった）を記録。
2. 既存`policy.ExecutionPolicy`（`HoldOnFailurePolicy`）が評価され、既定では常にHoldを返す——これは他の失敗種別と同一の、既存のPolicy判断です。
3. `TaskService.Hold(...)`——Task最終状態は`on_hold`。

新しいTask stateは追加していません。Fail（実行事実）とHold（Policy判断）を混同しないというCONSTITUTION Article 6の原則も、既存の区別をそのまま踏襲しているだけで変更していません。既存の`RecoveryService`／explicit recovery機構（ADR-0020）が、この新しい失敗種別のTaskも他のHeld Taskと同様にそのまま扱えます——新しいRecovery経路は追加していません。

### 3. Deliverableは保存しない

`Execute()`は`workerResult.StopReason == worker.StopReasonMaxTokens`を検出した時点で`deliverables.Save(...)`を一切呼ばずに`recoverExecutionFailure`へ分岐します。truncated contentがcanonical Deliverableとして確定commitされることは構造的にありません（既存の`recoverWorkerFailure`パス——Providerが実際に失敗した場合——と同じく、Deliverable Save自体に到達しないため）。

生成された部分的な本文自体は失われません——`result.WorkerResult`（既存field）へ設定済みのまま返され、Command Ledgerの`Result` JSON経由で診断目的に観測可能です。これは既存の`execution.Result.WorkerResult`という診断用フィールドをそのまま再利用しているだけで、新しいpartial-content保存機構は追加していません。

### 4. Provider-neutralな型付き表現

`internal/execution/errors.go`へ`ErrorOutputIncomplete ErrorKind = "OUTPUT_INCOMPLETE"`を追加しました（新規Stageは追加せず、既存`StageWorker`を再利用）。`executionFailureEnvelope`（`internal/process/execution.go`、ADR-0041が確立した既存の唯一の分類地点）は、この新しいKindについてだけ`envelope.Category = string(result.StopReason)`（例：`"max_tokens"`）を設定します——生のProvider文字列やHTTP statusではなく、既存の型付き`worker.StopReason`から導出される値です。`envelope.Provider`は設定しません（`claude.Error`由来のProvider診断は存在しないため、これは正しくnilのままです）。

この新しいEnvelopeは、既存の`reviewedWorkflowOuterEnvelope`（`case "task_execute": child = last.Execution.Failure`）、Command Ledgerの`Details`伝播、HTTP／UI投影のいずれも変更なしにそのまま流れます——ADR-0041が意図したとおり、新しい分類地点を1つ追加しただけで、伝播ロジック自体には一切触れていません。

### 5. StopReasonの正常値は変更なしで成功経路へ

`StopReasonCompleted`（正常終了）、`StopReasonStopSequence`（Anthropic上の正当な正常終了）、`StopReasonUnknown`（未報告または未分類——truncatedと推測しない）は、いずれも既存の成功経路（Deliverable保存、TaskService.Complete）をそのまま通ります。`max_tokens`という1つの値だけが新しい分岐を持ちます。

### 6. Synthesis Acceptance harnessの観測を production 失敗経路でも維持

`synthesisacceptance.Run()`は、`StopReason`/`TokenUsage`/`Duration`のTASK-004抽出を`ExecuteReviewedWorkflow`の成功・失敗を問わず早い時点で行うよう並び替えました——本Checkpoint以前は、この抽出が`workflowErr != nil`の早期returnより後にあったため、truncated attemptの場合に`StopReason`/`OutputTruncated`が観測不能になる回帰リスクがありました。`workflowFailureCategory`は新しい`FailureOutputIncomplete`カテゴリを、既存の`FailureProvider`判定より先にチェックします——Provider呼び出し自体は失敗していないため、これを`PROVIDER_FAILURE`として誤分類しないためです。

### 7. ~~Reviewパスは対象外（既存機構で十分）~~

~~Review自体もProvider呼び出しであり、理論上は`max_tokens`で打ち切られ得ますが、Reviewは既存のStructured Output契約（JSON Schema）を要求しており、truncateされたJSONは既存の`FailureStructuredOutputInvalid`分類へ既に自然に失敗します。本ADRはReview側に新しいチェックを追加しません。~~

（superseded。PB-3ah.3のCheckpointで、この節の前提どおりにはならないことが判明しました——truncateされたJSONは`json.Decoder`のtop-level EOF判定次第では構造的に有効なJSONとして誤って受理されうる、あるいは無効なJSONとして`FailureStructuredOutputInvalid`に分類されても、それは「Provider呼び出しの失敗」ではなく「出力が完結する前に打ち切られた」という別の事実であり、CEO PlanなどのStructured Output caller、およびTask実行のProvider-output pathと一貫しない分類になります（Task実行はStructured Output callerではありませんが、`StopReasonMaxTokens`を先行検出して`OUTPUT_INCOMPLETE`へ分類する既存の同じ判断layerを持ちます）。Reviewも他のStructured Output callerと同じ`StopReasonMaxTokens`の先行検出を持つよう変更しました。詳細は下記「Addendum: Review Structured Output Max-Tokens Detection」を参照してください。）

## Non-goals

- `defaultMaxTokens = 3000`の変更。
- Prompt compression、Prompt v3。
- Evaluator（`internal/synthesisacceptance/evaluator.go`）の変更。
- Automatic retry、別Provider fallback。
- Monetary token budgeting、Workflow全体のaggregate token budget。
- UI redesign（既存のcode/stage日本語copy lookup tableへの新規分岐追加は将来のUI Checkpointの対象とし、本Checkpointでは行いません）。

## Runtime MaxTokens Policy Seam（今回は評価のみ、実装せず）

調査の結果、`ClaudeProcessConfig.MaxTokens` → `claude.Config.MaxTokens`というoverride plumbingは既に存在します（`internal/process/execution.go`／`review.go`／`ceo_plan.go`が`provider.MaxTokens`をそのまま`claude.Config`へ渡す既存経路）。ADR-0045（bounded Provider request timeout policy）が確立した「Runtime composition edgeが所有する単一のdefault + 明示CLI override」というpatternを、将来MaxTokensへ適用する場合も、この既存plumbingへ値を渡すだけで済み、新しいConfig型やspeculativeな汎用config systemは不要です。

本Checkpointでは、defaultを3000のまま維持し、CLIでの値変更機能は追加しません（speculative configを作らないという明示指示に従う）。将来Checkpointの候補として`docs/ROADMAP.md`へ記録するに留めます。

## Consequences

- **`max_tokens`発生時のproduction behavior**: Task実行はDeliverableを確定保存せず、TaskServiceがFail→Hold（既定Policyにより）を記録し、`OUTPUT_INCOMPLETE`という型付きFailureEnvelope（`Category=max_tokens`）が生成されます。同じCommand IDでのreplayはCommand Ledgerのclaim/replay機構によりProviderを再呼び出ししません（ADR-0021、既存機構のまま）。
- **Deliverable保存との関係**: truncated contentがcanonical Deliverable Storeへ書き込まれることはありません。生成された部分的本文は`execution.Result.WorkerResult.Content`（Command Ledgerの`Result` JSON経由）を通じてのみ診断目的に残ります。
- **Review／Recoveryとの関係**: Reviewはこの新しい失敗経路には到達しません（Task自体がExecute段階で停止するため）。既存のExplicit Recovery（ADR-0020）／RecoveryServiceが、この新しい失敗種別のHeld Taskも他のHeld Taskと同様にそのまま扱います。
- **Compatibility**: JSON Contract v1への破壊的変更はありません。`ErrorOutputIncomplete`／`OUTPUT_INCOMPLETE`はどちらもadditiveな新しい列挙値で、既存の`code`/`stage`文字列lookupには影響しません。`execution.Result`・`failure.Envelope`のフィールド形状は無変更です。
- **Observability**: `StopReason`／`OutputTruncated`相当の情報は、production Command Ledgerの`Result`／`Failure.Details`を通じて型付きで追跡可能になりました。Synthesis Acceptance harness固有ではなく、`ExecuteTask`を経由するあらゆるproduction呼び出しに適用されます。
- **Future token policyとの関係**: 本ADRはtoken *count*ベースのbudget（BudgetGuardが引き続き扱わない領域）には触れません。`defaultMaxTokens`自体の値決定、Prompt compression、Cross-Evidence evaluatorの再検討は、いずれも別途のfuture Checkpointの対象として`docs/ROADMAP.md`へ分離して記録します。

## Addendum: Review Structured Output Max-Tokens Detection (PB-3ah.3 Checkpoint follow-up)

Section 7 above ("Reviewパスは対象外") is superseded. Review is now a Structured Output caller that detects `worker.StopReasonMaxTokens` before parsing, matching CEO Plan generation and this ADR's own governing distinction (a Provider call succeeding is not the same question as an output being complete). This addendum documents the active contract; it does not reopen the Task-execution Decision above.

- **Detection point**: `ReviewService.Execute()` (`internal/service/review_service.go`) checks `runResult.StopReason == worker.StopReasonMaxTokens` immediately after the Runner call succeeds and its result passes `Validate()`, and strictly before `review.ParseTypedDecision` is ever called. A truncated Structured Output response's (necessarily malformed-or-incomplete) `Content` never reaches the Review parser.
- **Classification**: `OUTPUT_INCOMPLETE`/`review_output_incomplete` — the same `OUTPUT_INCOMPLETE` code this ADR already established for Task execution and CEO Plan generation, so a caller filtering Command Ledger records for incomplete-output failures finds every Structured Output caller under one code. Never `REVIEW_RESULT_INVALID` (an ordinary Review parse failure) and never `FailureStructuredOutputInvalid`/`PROVIDER_RESPONSE_INVALID` (the Adapter-level category this superseded section 7 assumed truncation would fall into). `internal/process/review.go`'s `reviewFailureEnvelope` is Review's single classification point (mirroring `executionFailureEnvelope`'s role for Task execution) and forwards this classification unchanged to Reviewed Workflow and Interaction Workflow, the same ADR-0041 "forward, never reclassify" propagation this ADR's own Decision 4 already relies on.
- **No Review or Revision artifact**: detecting `StopReasonMaxTokens` returns before any Review artifact is constructed or saved, and before any Revision is created — structurally the same "never reach the save step" shape Decision 3 above already established for Task execution's Deliverable.
- **No automatic retry or fallback**: exactly one Provider call. A truncated Review, like a truncated Task execution, surfaces as a typed failure for a human or the existing Recovery path (ADR-0020) to act on — this ADR does not add retry or fallback for any caller, Review included.
- **Malformed content is never treated as an ordinary Review parse failure**: this is the specific gap section 7 did not anticipate. `json.Decoder`'s own top-level EOF semantics do not reliably guarantee a truncated Structured Output response fails JSON parsing in every shape, and even when it does fail, "failed to parse" and "was never finished being generated" are different facts that belong in different typed categories — conflating them was exactly the kind of silent-success-adjacent misclassification Article 8 (see Context above) already forbids in the Task-execution case this ADR was written for.
- **Provenance**: this addendum's motivating incident (PB-3ag) never had its own `stop_reason` recorded in evidence. This change closes a real cross-caller inconsistency (Review was the one Structured Output caller without the check every other caller already had) and defends against a *possible* max_tokens path; it does not newly assert that PB-3ag's actual cause was confirmed to be `max_tokens`.
- **Caller inventory, stated explicitly**: the two production Structured Output callers are CEO Plan generation and Review -- both request a JSON Schema-constrained response and both check `StopReasonMaxTokens` before parsing it. Task execution is not a Structured Output caller (its output is plain content, not a schema-constrained document), but it independently detects the same `StopReasonMaxTokens` and classifies it as `OUTPUT_INCOMPLETE` (Decision 1-4 above) -- a separate, pre-existing instance of this ADR's governing distinction, not a Structured Output check. `OUTPUT_INCOMPLETE` itself is never a Structured Output invalid reason: it carries no Provider diagnostic at all (`ProviderFailure`/`Envelope.Provider` stay nil for every caller that reaches it), and it is not, and must never become, a seventh value alongside the six closed `claude.StructuredOutputInvalidReason` values (`unexpected_content_block`, `text_block_count_invalid`, `empty_text`, `invalid_json`, `multiple_json_documents`, `trailing_json_content`) -- those describe a *malformed* response the Provider finished sending; `OUTPUT_INCOMPLETE` describes a response the Provider never finished sending, a structurally different fact this ADR keeps in a separate, Provider-diagnostic-free classification for every caller, Structured Output or not.

## Rejected Alternatives

- **`max_tokens`を`ErrorWorkerFailed`（既存のWorker/Provider失敗種別）として分類する**: Provider呼び出しは実際には成功しており、これを「Workerが失敗した」と表現するのは事実と異なるため却下。
- **新しいTask state（例: `incomplete`）を追加する**: 既存のFail/Hold語彙で十分に表現でき、「新しいTask stateを安易に追加しない」という明示指示にも反するため却下。
- **truncated contentを"draft"としてDeliverable Storeへ保存する**: Deliverable Storeに"canonical/non-canonical"の区別機構が存在せず、追加すれば新しい永続化概念になり、下流のDependency Evidence Collector（ADR-0056）が誤ってcanonicalとして読む危険があるため却下。既存の`WorkerResult.Content`による診断表示で十分。
- **Acceptance harnessだけでtruncationを検出し、production pathは変更しない**: 本Checkpointの明示目的（productionでも黙って成功にしない）に反するため却下。
- **`defaultMaxTokens`を今回同時に引き上げる**: 明示的にNon-goalsとして除外。この変更の効果を測定する前に対症療法的にceilingだけ動かすのは、根本問題（truncationが可視化されていなかったこと）を覆い隠す危険があるため。
