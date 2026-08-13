# ADR-0041: Typed FailureEnvelopeをchild→outer→Ledger→HTTP→UIへそのまま伝播する

## Status

Accepted

## Context

ADR-0021のCommand Ledgerは各Commandの終端outcomeを`{code, stage}`という平坦なpairで永続化します。この単純な契約自体は妥当ですが、その`code`/`stage`を実際に「決定する」ロジックが複数の境界に分散していました。

- `reviewedWorkflowFailureClassification`（`process/reviewed_workflow.go`）は、outer Reviewed Workflow Commandの失敗分類を決めるために、child Task/Reviewの`Result`構造体（`result.Tasks[last].Execution.ProviderFailure`、`.Review.ProviderFailure`、`.Review.FailureCode`/`.FailureStage`）を直接読み、優先順位付きのif分岐で再構成していました。これはchildがすでに自分のLedger entryへ確定・記録した分類を、outerが独自に「推測し直す」アンチパターンです。
- `workflowFailure`（`process/interaction_workflow.go`）は、`*RecordedCommandError`が渡ってきた場合はそれを使いますが、そうでない場合は上記の再構成ロジックへfall backしており、同じデータから同じ分類を3層目でも計算していました。
- `execution.ProviderFailure`、`review.ProviderFailure`、`process.ProviderFailure`という構造的に同一の型が3箇所独立に定義され、変換helper（`reviewOrchestrationProviderFailure`）だけがそれらをつなぐ役目を負っていました。
- `providerFailureCode`（`process/interaction.go`）はInteraction Plan用に書かれた関数でしたが、ReviewとReviewed Workflowからも共用されており、`default`が`"INTERACTION_PLAN_FAILED"`固定でした。この結果、`claude.FailureRefusal`（Provider拒否）や`claude.FailureStructuredOutputInvalid`（Structured Output契約違反）というカテゴリがReview経由で発生した場合、無関係な`INTERACTION_PLAN_FAILED`という診断がCEOへ返っていました。実際に動いていた誤分類バグです。
- HTTP層（`httpapi/handler.go`の`mapCommandError`）は既に正しく設計されており、`errors.As(err, &recorded)`のcaseは`recorded.Code`/`.Stage`をそのまま転送するだけで再分類していませんでした。問題はさらに一段上のWeb UI（`app.js`の`commandProviderFailure`）にあり、`result.provider_failure`→`result.workflow.tasks[].execution.provider_failure`→`.review.provider_failure`という複数shapeを順に試すtree-walkingで診断情報を再構成していました。

`docs/CONSTITUTION.md` Article 8は「失敗や部分成功を成功として隠さない」「Stage、ErrorKind…を型付きResult/Errorで返す」ことを求めています。上記の分散した再分類・再構成は、これに反するものではないものの、最初に確定した分類が層をまたぐたびに変質・欠落しうる構造でした。

## Decision

### FailureEnvelopeの所有者

`go/internal/failure`（新設、Provider/Domain非依存）が`Envelope`型を定義します。Envelopeは「最初にfailureを確定できるdurable Process boundary」だけが生成します。今回のvertical sliceでは：

- **Review execution**（`process/review.go`の`reviewFailureEnvelope`）
- **Task execution**（`process/execution.go`の`executionFailureEnvelope`）

の2箇所だけがEnvelopeを新規生成します。Revision、CEO Plan生成/適用、External Actionは今回のスコープ外であり、既存の`(code, stage)`分類helperをそのまま維持しています。

### child→outer伝播

Reviewed Workflow（`reviewedWorkflowOuterEnvelope`）とInteraction Workflow（`workflowFailure`の書き換え）は、child（Task execution／Review）がすでに確定したEnvelopeを**そのまま転送**します。旧`reviewedWorkflowFailureClassification`が行っていた「child Resultのfieldをtree-walkして再構成する」ロジックは削除し、「どちらのchild種別が失敗したか（`stage`から自明）に応じて、その`.Failure`フィールドを1回コピーするだけ」の選択ロジックに置き換えました。Provider category→code変換の再適用は一切行いません。

outerが自分自身のLedger entryへ書き込む際は、childのcopyに対して`Partial`/`RecoveryRequired`だけを自分自身のcommit状態で上書きします（`Code`/`Stage`/`Category`/`Provider`/`Parse`/`Evidence`は不変）。これはコピーに対する上書きであり、child自身が保持するEnvelopeへのポインタを変更しないよう、必ず値コピーしてから書き換えます。`Partial`はLedger entryごとに正当に異なりうる値（「このcommandの副作用がどこまでcommit済みか」）であり、「何が失敗したか」という分類とは別軸であるため、これは再分類には当たりません。

### Command Ledger互換性

`commandledger.Failure`へ`Details *failure.Envelope`をadditiveに追加しました。既存の`Code`/`Stage`は無変更です。`Record.Validate()`は`Details`が存在する場合だけ`Details.Code == Code`、`Details.Stage == Stage`、`Details.Validate()`を検査します。`SchemaVersion`は据え置きです。`details`キーを持たない旧record（本Phase以前にcommitされたすべてのrecord）は、`Details == nil`としてそのままdecode・replayできます。

`finishDurableCommand`（既存シグネチャ、既存動作を完全維持）に加え、`finishDurableCommandWithEnvelope`を新設しました。Review／Task execution／Reviewed Workflow／Interaction Workflowのouter finish呼び出しだけがこちらへ切り替わり、それ以外の約20箇所の既存呼び出しは無変更です。

### HTTP／UI投影

`httpapi.CommandError`へ`Details *failure.Envelope`をadditiveに追加しました。`mapCommandError`の`errors.As(err, &recorded)` caseへ`Details: recorded.Envelope`を1行追加しただけで、他の分岐・HTTP status選択ロジックには触れていません。

Web UI（`app.js`）は`errorDiagnostics(details, result)`という新helperを追加し、優先順位を`response.error.details` → `ledger.failure.details` → 旧`commandProviderFailure()`のtree-walk fallback、としました。`commandProviderFailure()`自体は削除せず、Details不在時（未移行のCommand、旧Ledger record）のためのlegacy fallbackとして残しています。`code`/`stage`文字列で日本語copyを選ぶ既存のlookup tableは無変更です——これはEnvelope導入で変わるべき対象ではなく、自然言語へのprojectionそのものだからです。

### Recoveryとの関係

`Envelope.RecoveryRequired`は、各Commandの既存finish処理がすでに計算していた`partial`値をそのままコピーしただけです（`handler.go`の既存`RecoveryRequired: recorded.Partial`ルールと同一）。新しい「Recoveryが必要かどうか」の判断ロジックは一切追加していません。自動retry・rollback・repairも追加していません。

### 副次的に修正したbug

`failure.ClassifyProviderCategory`は`providerFailureCode`が担っていたProvider category→code変換を一元化し、`claude.FailureRefusal`→`PROVIDER_REFUSED`（新設）、`claude.FailureStructuredOutputInvalid`→`PROVIDER_RESPONSE_INVALID`を明示的にcoverし、default（未知category）をCommand非依存の`PROVIDER_FAILURE`にしました。`process.providerFailureCode`はInteraction Plan向けの薄いwrapperとして残し、`PROVIDER_FAILURE`の場合だけ既存の`INTERACTION_PLAN_FAILED`へ変換します。これにより、Interaction自身の呼び出しでも上記2カテゴリの誤分類が同時に解消されています。

### Rejected Alternatives

- **全Commandを一度に移行する**: 依頼で明示的に禁止されており、変更範囲とリスクが不必要に拡大するため見送りました。
- **既存legacy field（`ProviderFailure`／`FailureCode`／`FailureStage`／`ParseFailureReason`）を即時削除する**: 依頼により今回は維持し、Envelopeから導出するprojectionへ位置づけを変えるに留めました。削除は次のPhaseの削除候補です。
- **UIやouter wrapperが独自に`recovery_required`を判定する**: Constitution Article 8および依頼の明示的禁止に反するため、既存のPartial由来の値をそのまま relay するだけに留めました。
- **`execution.ProviderFailure`／`review.ProviderFailure`／`process.ProviderFailure`を単一型へ統合する**: JSON Contractへの影響範囲が広がるため見送り、`Envelope.Provider`という新しい単一の入り口を追加する形にしました。

## Consequences

- Review／Task executionのfailure分類は、それぞれ1箇所（`reviewFailureEnvelope`／`executionFailureEnvelope`）だけで決定され、outer層・Ledger・HTTP・UIはそれを転送するだけになりました。
- `reviewedWorkflowFailureClassification`・`workflowFailure`のtree-walkingロジックは削除され、再分類は構造的に発生し得なくなりました。
- 既存Ledger record・既存Result JSON・既存HTTP Contractはすべて後方互換です。`details`を持たないrecordは今までどおり読めます。
- 実機で発生していた「Provider拒否・Structured Output契約違反がReview経由でINTERACTION_PLAN_FAILEDと誤診断される」bugが解消されました。
- `reviewOrchestrationProviderFailure`はlegacy fieldのためにまだ必要であり、削除候補として記録しつつ今回は維持しています。
- Revision、CEO Plan、External Action、Interaction Action（`interaction_action.go`の同型アンチパターン）は今回のスコープ外のまま残っており、次のPhaseの対象です。
