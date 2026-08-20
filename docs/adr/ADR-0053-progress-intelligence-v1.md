# ADR-0053: Progress Intelligence v1 — Deliverable Change, Structured Review, and Resource Signals

## Status

Accepted (Checkpoint — Structured Review Signature, Deliverable fingerprint, and CompoundProgressPolicy implemented and wired into production; semantic/embedding-based judgement, Cost accounting, and ErrorKind-based signals explicitly deferred — see Consequences)

## Context

ADR-0052's No-Progress Foundation v0 (`policy.RepeatedFeedbackProgressPolicy`) stops a Revision branch when the exact same normalized Review feedback *string* repeats. This has a real false-positive/false-negative gap that this Checkpoint's own request named directly: a Reviewer merely rewording the same finding ("要件が不足しています" vs. "要件について追記が必要です") looks like *different* feedback to a literal string comparison, so v0 never fires on it — while genuinely unrelated content changes are invisible to v0 entirely, since it never looks at the Deliverable at all. WorkCairn's actual goal is not "did the Review text stay byte-identical" but "is the AI actually converging, or spending Revisions/Provider calls/time without the outcome improving."

This ADR is deliberately scoped by what this Checkpoint's own instructions ruled out up front: no embedding or semantic-similarity judgement, no second LLM loop to judge the first loop's progress, and deterministic/cheap/explainable signals first. The success condition is the same one ADR-0052 already established for Recovery: WorkCairn should be able to stop itself early and cheaply when it is not converging, using facts it already has — not a smarter model watching a model.

### 既に決定済み・実装済みで、本ADRが変更しない事項

- **`policy.ProgressPolicy`インターフェースと`ProgressDecision`語彙**（ADR-0052）は無変更です。本ADRは`ProgressSignal`をadditiveに拡張し、この語彙を満たす新しい`Evaluate`実装を1つ追加するだけです。
- **ProgressPolicyはTask状態を直接変更しない**という原則（ADR-0052、CONSTITUTION Article 6）は無変更です。本ADRの新しい型・実装も例外なくこの原則に従います。
- **Revision Limit Recovery**（Command、lineage、UI）は無変更です。No-Progressの停止は、既存のRecovery機構（`interaction.workflow.recover_revision`）をそのまま再利用します——新しいRecovery画面や別の回復経路は一切追加していません。
- **`FailureEnvelope`の`NO_PROGRESS_DETECTED`分類**（ADR-0052、`reviewedWorkflowOuterEnvelope`）は無変更です。本ADRは停止の**判断根拠**（どのPolicyが、どんな信号で判断したか）だけを変更し、停止が下流へどう伝播するかは変更しません。
- **`worker.TokenUsage`/`Duration`が既に`execution.Result`/`review.ExecutionResult`に存在すること**（`internal/worker/result.go`、`internal/execution/result.go`、`internal/review/result.go`）は既存の実装です。本ADRはこれらの既存フィールドを読むだけで、新しいUsage計測機構を追加しません。

### 新たに決定が必要な事項（本ADRのDecision）

1. Review所見を、自由文の一致ではなく**構造的**に比較する方法。
2. Deliverable本文が実際に変化したかを、**semanticではなく決定的**に判定する方法、およびその安全な扱い方（内部値であり続けること）。
3. 上記2つと既存のRevision Countを**複合条件**として組み合わせる新しいProgressPolicy実装。
4. Resource Signal（Provider call数、経過時間）を`ProgressSignal`へ観測用として追加する範囲——ゲーティングには使わない。
5. Cost/ErrorKindベースの信号を今回のスコープに含めない、という明示的な線引き。

## Decision

### 1. Structured Review Signature — 自由文ではなく、既存のtyped enumフィールドから構築

`policy.ReviewSignature`（`go/internal/policy/progress.go`）は、`review.Decision`の**Verdict**と、`review.Issue`の**Category**・**Severity**（すでに閉じたenum: `date`/`format`/`requirements`/`context`/`todo`/`other`、`high`/`medium`/`low`）だけから構築します。`Description`／`SuggestedAction`／`Summary`という自由文フィールドは一切読みません——これがChecklist項目5の「Review全文をそのままsignatureにしない」を満たす直接の設計選択です。`review.Decision`／`review.Issue`のContractは無変更のまま、既存のtyped enumフィールドを再利用するだけなので、大規模なReview Contract再設計（Checklist項目7が明示的に避けるよう求めた選択肢）は不要でした。

```go
type ReviewSignature struct {
    Verdict         review.Verdict
    IssueCategories []string // sorted, deduplicated
    IssueSeverities []string // sorted, deduplicated
    IssueCount      int
}
func NewReviewSignature(decision review.Decision) ReviewSignature
func (signature ReviewSignature) Equal(other ReviewSignature) bool
```

`IssueCount`を分離フィールドとして持つ理由は、「同じcategoryの指摘が1件」と「同じcategoryの指摘が4件」を意図的に区別するためです——さもないと、指摘件数が増えていても構造的signatureが同じままになり、悪化を進歩と誤認しかねません。

### 2. Signature normalization — sort/dedupeで、Go map iteration順に依存しない

`NewReviewSignature`は`IssueCategories`/`IssueSeverities`を`map[string]struct{}`で重複排除したのち`sort.Strings`で確定順序へ変換します。`ReviewSignature.Equal`はこの既にソート済みの`[]string`を`slices.Equal`で比較するだけなので、Issueの記述順序（AがBの前か、BがAの前か）や、Goのmap iterationが持つ非決定性のどちらにも一切依存しません。

### 3. Deliverable Fingerprint — opaqueな内部比較値、semanticではなくcontent-blind

`policy.DeliverableFingerprint`（`type DeliverableFingerprint string`）は、Deliverable本文を軽量・content-blindに正規化（改行コード統一、行末空白trim、前後空白trim——Markdown構造は一切解釈しない）したのち、SHA-256でhash化した値です。`NewDeliverableFingerprint(content string) DeliverableFingerprint`は`go/internal/policy`パッケージに置かれ、`review`／`deliverable`パッケージへの依存を一切持たない純粋関数です。

安全性の設計判断（Checklist項目10に対応）:
- この値は**どこにも永続化しません**——`runBranch`内のローカル変数としてのみ存在し、Ledger／Audit／Eventのいずれにも書き込みません。
- **成果物全文をコピーしません**——fingerprintはcontentから一方向に導出されたSHA-256だけで、元のcontentを復元できません。
- **UIへ表示しません**——Web UIはこの値を一切参照せず、既存の`taskEvidenceBlock`/`deliverableViewerNode`（canonical Deliverable本文をそのまま表示する既存経路）だけがCEOに見える面です。
- **JSON Contractへ露出しません**——`ProgressSignal`自体がHTTP応答／Command Ledger／Eventのいずれにも登場しません（下記6節）。

### 4. exact change判定 — unchanged/changedの二値のみ、v1では類似度スコアを持たない

`runBranch`は、各Task実行の`execution.Result.WorkerResult.Content`（既存フィールド、`internal/service/execution_service.go`が既にセットしている値——新しいDeliverable取得経路は追加していません）からfingerprintを計算し、同じBranch内の**直前の**attemptのfingerprintとだけ比較します。1回目のattempt（比較対象が存在しない）は保守的に`DeliverableChanged: true`とし、「変化なし」を証拠なしに主張しません。`WorkerResult`が存在しない場合（Fakeや異常系）も同様に`true`を返します——測定できない場合に誤って「進歩なし」と判定しないための、意図的なfail-safeです。

### 5. CompoundProgressPolicy — 3つの独立信号が一致したときだけ停止

```go
type CompoundProgressPolicy struct {
    ReviewRepeatThreshold         int // default 2
    DeliverableUnchangedThreshold int // default 2
    RevisionCountThreshold        int // default 2
}
```

3条件すべて（Review Progress: `ConsecutiveSameReviewCount >= ReviewRepeatThreshold`、Deliverable Progress: `ConsecutiveUnchangedDeliverableCount >= DeliverableUnchangedThreshold`、Execution Progress: `RevisionCount >= RevisionCountThreshold`）が**同時に**成立したときだけ`ProgressEscalate`を返します。単一の信号だけで停止しない、という設計はChecklist項目19（false positive回避）への直接の応答です——同じQA指摘でも成果物が大きく改善している途中の可能性、逆に成果物が一時的に変化しなくても指摘内容が変わっている可能性、どちらも単独では停止条件になりません。全threshold既定値2は`autonomy.DefaultMaxRevisionCount`と同じ保守的な値で、既定のMaxRevisionCount運用では最短でも3回目のattempt（3回目のRequest Changes）でしか発火せず、Revision Guardの上限に達するのと同じか、それより一歩早いだけに留まります。

`v0`の`RepeatedFeedbackProgressPolicy`は削除していません——直接/operator呼び出し元が引き続き利用できます。production wiring（`process/reviewed_workflow.go`）だけを`CompoundProgressPolicy{}`へ切り替えました。

### 6. Resource Signal — 観測用のみ、ゲーティングには使わない

`ProgressSignal`へ`ProviderCallCount int`と`ElapsedDuration time.Duration`を追加しました。両方とも`runBranch`が既存の`execution.Result.Duration`/`review.ExecutionResult.Duration`（新しい計測は一切行わない、既存フィールドの単純な加算）から計算します。`CompoundProgressPolicy`はこの2つのフィールドを**decisionに使いません**——`signal.validate()`での非負検証だけが唯一の消費箇所です。これは意図的な設計です: Tokenの正確なコスト換算（modelごとの価格、cache token、tool billing）はまだ存在せず、ダミーのCost推定をこのラウンドで作らないという明示的な指示（Checklist項目14）に従いました。将来のBudgetGuardがCostを計算する基盤を持つまで、この2フィールドはPolicyの意思決定に使われない、単なる可観測性のための値のままです。

Token usage（`worker.TokenUsage`、`execution.Result.Usage`/`review.ExecutionResult.Usage`）は既に正確に取得可能でしたが、`ProgressSignal`へは追加していません——今回のCompoundProgressPolicyが実際に消費する最小集合（Review/Deliverable/Revision Count）に絞り、「将来使うかもしれない」フィールドの先取りをしない、というChecklist項目15の指示に従いました。

### 7. 評価タイミングと順序 — 既存の順序を維持

Review Request Changes → (v0/v1問わず) ProgressPolicy評価 → No Progressならbranch停止 → そうでなければRevision Count確認 → 上限ならRevision Limit → Revision実行、という順序は**ADR-0052から無変更**です（`runBranch`の既存コード構造そのまま）。無駄なRevision Taskを作成する前にNo Progress判定が行われる、という性質もそのまま維持されています。

## Consequences

### 実装済み（Decisionどおり）

- `policy.ReviewSignature`/`NewReviewSignature`/`Equal`（構造的Review比較）。
- `policy.DeliverableFingerprint`/`NewDeliverableFingerprint`（content-blind正規化＋SHA-256、非永続）。
- `policy.CompoundProgressPolicy`（3信号AND、既定threshold 2、Task状態を直接変更しない）。
- `ProgressSignal`のadditive拡張（`ReviewSignature`、`ConsecutiveSameReviewCount`、`DeliverableChanged`、`ConsecutiveUnchangedDeliverableCount`、`ProviderCallCount`、`ElapsedDuration`）。
- `runBranch`のwiring（Deliverable fingerprint比較、Review signature比較、Resource Signal集計）——既存の`previousFeedback`/`consecutiveSameFeedback`（v0互換）はそのまま並存。
- Production wiring切り替え（`process/reviewed_workflow.go`の`SetProgressPolicy`が`CompoundProgressPolicy{}`を使用）。
- 単体テスト一式（ReviewSignature正規化・順序非依存・重複除去、DeliverableFingerprintの改行/空白正規化・実質変更検出、CompoundProgressPolicyのAND条件・false positive回避・custom threshold）と、並列Branch統合テスト（A成功／B No-Progress／C成功→Recovery→Synthesis再開）、Browser E2Eテスト1本。

### 意図的に据え置いた事項（将来Checkpointの対象）

- **意味的類似度・embeddingベースの比較**は実装していません（今回のスコープから明示的に除外）。
- **Cost estimate**は実装していません——ダミーのCost計算を作らず、正確なCost accounting基盤が整うまでBudgetGuard側の責務として残します。
- **Token usageの`ProgressSignal`への追加**は見送りました（既に取得可能ですが、今回のCompoundProgressPolicyの意思決定には使わないため）。
- **ErrorKind／FailureEnvelope Codeの反復をProgress Intelligenceへ使う設計**は設計ノートに留めました（下記）——実装していません。
- **Deliverable内容の意味的差分**（「30%改善した」等のスコア）は実装していません——v1は`changed`/`unchanged`の二値のみです。

### ErrorKind反復についての設計ノート（未実装）

`failure.Envelope.Code`（例: `PROVIDER_AUTHENTICATION_REQUIRED`のようなProvider障害系Code）は、既にstable/typedな文字列として存在します。将来、同じCodeが同一lineageで連続する場合を第4のsignal（Execution Progress内の一区分、または独立したsignal）として`ProgressSignal`へ追加することは、今回追加した`ReviewSignature`/`DeliverableFingerprint`と同じ設計原則（typed値の比較、raw Provider textは使わない）で自然に拡張できます。今回は、Revision/Reviewの停滞という当面のスコープに集中するため実装していません。

### 移行・互換性への影響

すべての変更はadditiveです。`ProgressSignal`に新しいフィールドが増えましたが、`RepeatedFeedbackProgressPolicy`（v0）は新フィールドを一切読まないため無変更のまま動作します。`ProgressPolicy`未設定のService、直接/CLI/HTTP呼び出し元は挙動が変わりません。既存Command（`workflow.reviewed.execute`等）のシグネチャ・Ledger schemaは変更していません。
