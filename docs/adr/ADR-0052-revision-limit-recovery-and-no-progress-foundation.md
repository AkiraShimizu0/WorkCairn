# ADR-0052: Revision Limit Recovery and the No-Progress Foundation

## Status

Accepted (Checkpoint F — Revision Limit Recovery implemented and production-wired; No-Progress Foundation v0 implemented additively, semantic/embedding-based judgement explicitly deferred — see Implementation Notes)

## Context

ADR-0051（Leverage Engine）が並列Task実行とRevision Guard（`autonomy.MaxRevisionCount`）を導入した結果、Review/Revisionサイクルが上限に達して停止するケース（`ErrRevisionLimitReached`）が、単一Task実行より明らかに増えました。並列Branchが複数同時に走る以上、そのうち1本だけがRevision上限に達して停止することは異常ではなく、通常起こり得る状態です。

ADR-0051時点の実装は、この停止を**構造的失敗**として扱っていました: `reviewedWorkflowFailureClassification`は`"revision_limit"` stageを`REVIEWED_WORKFLOW_FAILED`という汎用codeへfallbackさせており（child Envelopeの`REVISION_LIMIT_REACHED`分類が outer Command へ正しく伝播していなかった）、CEOに見える体験は「原因不明の失敗」でした。回復するための唯一の手段は、失敗したCommandと同じ`interaction.plan.approve_and_execute`／`workflow.reviewed.execute`を——新しい判断もガイダンスもなしに——素朴に再実行することしかなく、これは実質的に無限retryと変わらない危険な形でした。

本Checkpointの要求は明確です: **Revision上限に達したこと自体は失敗ではなく、そこからCEOがどれだけ少ない操作で有用な成果を安全に救い、必要な部分だけ続けられるかが成功条件である。** 同時に、将来「同じ指摘が繰り返される」「成果物がほぼ変化しない」といった停滞を早期検出するNo-Progress Detectorの土台を、既存Architectureを壊さない形で用意する必要があります。

### 既に決定済み・実装済みで、本ADRが変更しない事項

- **Revision Guard本体**（`autonomy.MaxRevisionCount`、`ReviewedWorkflowRunService.runBranch`の`revisionCount >= maxRevisionCount`判定、ADR-0051）は無変更です。本ADRはこのGuardが停止した**あとの**回復経路と、停止判断そのものを補助する追加Policyだけを扱います。
- **TaskServiceの単一所有権**（CONSTITUTION Article 6）は無変更です。Revision上限到達時点でTaskは既に`Completed`（実行は成功しており、Reviewの`Request Changes`判定が記録されているだけ）であり、`TaskService.Hold`は`InProgress`からのみ有効な遷移であるため使えません（`go/internal/task/transition.go`確認済み）。本ADRはTaskの状態を一切変更せず、既存のReview verdict（`Request Changes`）をそのまま「なぜ止まったか」の証跡として扱います。
- **Command Ledgerのclaim/replay/idempotency**（ADR-0021）と**FailureEnvelope伝播**（ADR-0041）は無変更で、そのまま新しいCommandにも適用されます。
- **ADR-0049の単一承認チェーン**（`runInteractionWorkflowChain`共有ヘルパー）は無変更です。Recovery Commandはこのヘルパーを**そのまま再利用**することで、新しいWorkflow再開ロジックを一切書いていません。

### 新たに決定が必要な事項（本ADRのDecision）

1. Revision上限到達後、CEOが安全に再開するための**Recovery Command**の形（新規operationか、既存operationの再利用か）。
2. Recovery実行の**lineage**（元Task・元Review・Recovery Command・新Revision/Taskの関係）をどこに、どう記録するか。
3. `REVISION_LIMIT_REACHED`をFailureEnvelope／Conversation Projection／Composerへ、どう非侵襲的に統合するか。
4. No-Progress Foundationの**接続点**（`policy.ProgressPolicy`）と、v0として実装する最小の非AI判定。
5. 並列Branchの一部だけがRecoveryされる場合の、他Branch保存とSynthesis再開条件。

## Decision

### 1. Recovery Command — 新しいadditive operation、内部は既存Commandの再利用のみ

`interaction.workflow.recover_revision`という**新しい**Interaction層Command（`go/internal/process/interaction_recover_revision.go`）を追加しました。既存の`interaction.workflow.execute`や`interaction.plan.approve_and_execute`を「Recovery用に拡張」するのではなく別operationにしたのは、CEOの意思決定の性質が異なるためです——これは自動継続ではなく、**新しい人間の承認**（Recoveryしてよいという判断＋任意の追加指示）です。

内部実装は完全に既存機構の再利用です:

- Session状態の遷移は`interaction.Record.RecordRevisionRecoveryStarted`（新しいTurnKind `TurnRevisionRecoveryStarted`）が`StateWorkflowAttentionRequired → StateReadyToExecute`を担い、これは既存の`WorkflowStatusBlocked`/`WorkflowStatusLimitReached`が同じ遷移を使う「新しいCommandで再開する」パターンと同一です。
- 新しいRevision Taskの作成は、既存の`revision.execute`Command（`ExecuteRevision`）を**そのまま**、`commandledger.DeriveChildCommandID(outerCommandID, "revision.execute:"+taskID)`で導出したdeterministic child Command IDで呼び出すだけです。Revision自体のDomain/Storeロジックは一切変更していません。
- Workflow再開は、ADR-0049が導入した`runInteractionWorkflowChain`共有ヘルパーを**そのまま**呼び出します。これは`interaction.plan.approve_and_execute`と`interaction.workflow.execute`の両方が既に使っている同じ関数であり、Recovery用の新しい再開ロジックはゼロです。結果として、`workflow.EvaluateAllReadiness`（ADR-0051）が現在のTask Store状態から再計算するため、既にCompletedな他Branchは自動的に再実行対象から外れます。

### 2. Additional Guidance — 既存の唯一のPrompt入力チャネル（Task Title）への折り込み

CEOの追加指示（例:「この指摘は無視して、読みやすさを優先してください」）は、新しいPrompt注入機構を作らず、`revision.Intent.AdditionalGuidance`という additive フィールドとして`revision.PlanRevision`まで運ばれ、既存の唯一のWorker Prompt入力チャネルである**Revision TaskのTitle**（`prompt.builder.go`の`"作業指示: " + Task.Title`）へ折り込まれます。これにより新しいPromptビルダーや新しいContext経路が一切不要になりました。空文字列（指示なし）も有効な入力です——「同じ指摘のまま、もう一度試す」という選択自体が新しい人間の承認だからです。

### 3. Lineage — 既存の識別子だけで表現、新しい永続IDは追加しない

Recoveryの追跡可能性は、すべて既存の識別子の組み合わせだけで表現しています。新しいID体系は一切追加していません:

- **元Task ID**: `interaction.Turn.RecoveryTaskID`（既存の`task.Task.ID`文字列をそのまま格納）。
- **CEOの追加指示**: `interaction.Turn.RecoveryGuidance`。
- **Recovery Command自体**: `interaction.workflow.recover_revision`Commandの`CommandID`（ADR-0021のCommand Ledgerがそのまま追跡）。
- **新Revision/Task**: 既存の`revision.execute`子Commandの決定的ID（`DeriveChildCommandID`）と、その結果生成される新しい`task.Task.ID`。

`interaction.Record.Next()`は`stalledRevisionTaskID`という新しい純粋ヘルパーで「まだRevisionが作られていない、直近のRequest Changes Task」を機械的に特定し、`EligibleTaskIDs`として返します——CEOやUIが手動で対象Taskを推測する必要はありません。

### 4. FailureEnvelope統合 — 新しいCode 2つ、既存の伝播経路をそのまま利用

`reviewedWorkflowOuterEnvelope`（ADR-0041で導入済みの、child Envelopeを転記するだけの関数）に、構造的失敗ではない2つの新しいstageケースを追加しました:

```go
case stage == "revision_limit":
    envelope = failure.New("REVISION_LIMIT_REACHED", stage)
    envelope.Evidence = &failure.CommittedEvidence{Deliverable: true, TaskState: true, ReviewCanonical: true}
case stage == "no_progress":
    envelope = failure.New("NO_PROGRESS_DETECTED", stage)
    envelope.Evidence = &failure.CommittedEvidence{Deliverable: true, TaskState: true, ReviewCanonical: true}
```

`Evidence`フィールドは「最後の実行とReviewは両方とも正常にcommit済みである」という、Recovery UIが安全にDeliverable/Reviewを表示してよいという既存の事実を明示するだけで、新しい推測を一切加えません。新しいFailureEnvelopeスキーマ拡張やContract移行は不要でした——ADR-0041が既に用意した`Code`/`Stage`/`Evidence`フィールドで十分に表現できたためです。

Conversation Projection（ADR-0047）とCommand Ledgerへの追加変更はゼロです。`commandLedgerFailureDetails`は既に保存済みEnvelopeを優先して転記する実装だったため、この2つの新しいCodeは**既存コードパスのまま**エンドツーエンドで伝播します。UI側の「品質確認で修正上限に達しました」という文言は`interactionErrorGuidance()`の追加ケースであり、これは提示層のcopyであって、新しい偽のcanonical Conversationメッセージではありません（既存のFailure Turnがcanonicalなまま）。

### 5. No-Progress Foundation — `policy.ProgressPolicy`という新しい決定境界

`go/internal/policy/progress.go`に、既存の`policy.ExecutionPolicy`/`HoldOnFailurePolicy`と同じ設計原則（純粋関数、Provider非依存、State変更なし）に従う新しいインターフェースを追加しました:

```go
type ProgressPolicy interface {
    Evaluate(ctx context.Context, signal ProgressSignal) (ProgressDecision, error)
}
```

- **State変更なし**: `ProgressPolicy`はTask/Session/Revisionのいずれの状態も直接変更しません。呼び出し元（`ReviewedWorkflowRunService.runBranch`）が`ProgressEscalate`/`ProgressCancel`を受け取った場合にのみ、既存のRevision Guardと**同じ形**（`stageError("no_progress", ...)`）でBranchを停止させます——新しい停止経路ではなく、既存のRevision Guard停止経路の兄弟です。
- **Providerに依存しない**: `ProgressSignal`は呼び出し元が既に持っている型付き情報（`TaskLineageID`、`RevisionCount`、正規化済み`NormalizedFeedback`、`ConsecutiveSameFeedbackCount`）だけを受け取ります。生のDeliverable本文やProvider応答は一切渡しません。
- **Optional・後方互換**: `ReviewedWorkflowRunService.SetProgressPolicy`という追加setterで注入し、未設定（nil）なら今までどおりRevision Guardの回数上限だけが効きます。既存の全呼び出し元は変更不要です。

### 6. No-Progress v0 — 非AI・文字列一致ベースの`RepeatedFeedbackProgressPolicy`

v0実装は、意図的に単純な非AI判定です: 同じTask lineage内でReview所見（`normalizedReviewFeedback`——Issue の Category/Severity/Description/SuggestedAction と Summary を正規化・ソートした文字列）が`RepeatThreshold`回（デフォルト2、`autonomy.DefaultMaxRevisionCount`と同じ保守的な値）連続して完全一致した場合にのみ`ProgressEscalate`を返します。埋め込みベクトルやAI判定は使わず、意図的に「AIにもっと考えさせる」方向を避け、「無駄なRevision・Provider呼び出し・時間・Costを早く止める」という安全機構としての役割に限定しています。

### 7. 並列Branch — 他Branch保存とSynthesis再開は、既存機構がそのまま提供

A/B/Cの3 Branchが並列実行され、BだけがRevision上限に達するケース（Checkpoint要求の核心シナリオ）は、**新しいコードを一切書かずに**既存機構の組み合わせだけで正しく動作することを確認しました（`TestInteractionRecoverRevisionParallelBranchLimitThenRecoverySucceedsWithoutReExecutingOtherBranches`で証明）:

- `RunParallel`は各Branchの結果を`waitGroup.Wait()`後に`result.Tasks`へ追加してからerrorを検査するため、Bのエラーが原因でA/Cの結果が失われることはありません（ADR-0051で既に成立していた性質）。
- Synthesis（全Branch依存のTask）は、依存Task（B）がまだ`Completed`でない限り`workflow.EvaluateAllReadiness`が readyと判定しないため、誤って早期実行されることはありません。
- Recovery Command実行後、`planReviewedWorkflowBatch`が現在のTask Store状態から**再計算**するため、A/Cは既に`Completed`——再実行対象に含まれず、Bの新しいRevision Taskだけが新しいラウンドとしてdispatchされます。その後Synthesisが自然にreadyになります。

## Consequences

### 実装済み（Decisionどおり）

- Recovery Command（`interaction.workflow.recover_revision`）、Turn Kind（`TurnRevisionRecoveryStarted`）、`Next()`の`stalledRevisionTaskID`ヘルパー。
- `revision.Intent.AdditionalGuidance`のTitleへの折り込み、Vault Markdown投影への追記セクション。
- `REVISION_LIMIT_REACHED`/`NO_PROGRESS_DETECTED`のFailureEnvelope統合（`reviewedWorkflowOuterEnvelope`）。
- `policy.ProgressPolicy`インターフェースと`RepeatedFeedbackProgressPolicy`（No-Progress v0）、`ReviewedWorkflowRunService`への optional wiring。
- Web UIのRevision Limit Recovery専用画面（既存の`taskEvidenceBlock`/`deliverableViewerNode`を再利用、composerが「追加の指示（任意）」を受け付ける専用mode）。
- Go単体・統合テスト一式（Revision Guard off-by-one、Recovery idempotency/lineage、並列Branch Recovery、ProgressPolicy）とBrowser E2Eテスト1本。

### 意図的に据え置いた事項（将来Checkpointの対象）

- **意味的No-Progress判定**（Deliverable内容の意味的類似度、埋め込みベースの比較）は行いません。v0の文字列完全一致は「production-safeな最小境界」であり、今回のDecisionとして意図的に単純さを優先しました。
- **Cost/Tool Call量に基づくProgress判定**は未実装です。`ProgressSignal`に将来フィールドを追加する余地は残していますが、今回は追加していません。
- **Deliverable内容の比較**（「Revisionしたが、成果物がほとんど変化していない」の検出）は設計ノートに留め、実装していません（既存のDeliverableハッシュ/digestに類する追加フィールドは、Auditへ生成物本文やその指紋を外部Secret的に晒さない範囲で将来検討します）。
- **BudgetGuard/Scheduler統合**は対象外です。
- **深い再帰的Task分解**は対象外です（ADR-0051と同じ既存のスコープ制限）。
- Recovery CommandはADR-0049の「Case B: クラッシュ後の未完了outer Command再開」に相当する完全な対称性を、今回は実装していません（既知のスコープ制限として記録し、将来必要になれば別途対応します）。

### 移行・互換性への影響

すべての変更はadditiveです。`MaxRevisionCount`未設定のContract、`ProgressPolicy`未設定のService、Recovery機構を経由しない既存の直接/CLI/HTTP呼び出し元は、挙動が一切変わりません。既存Command（`revision.execute`、`workflow.reviewed.execute`、`interaction.plan.approve_and_execute`）のシグネチャ・Ledger schemaは変更していません。
