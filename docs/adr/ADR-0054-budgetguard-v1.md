# ADR-0054: BudgetGuard v1 — Runtime / Provider Call / Token Accounting Foundation

## Status

Accepted (Checkpoint — Runtime Budget and Provider Call Budget implemented and wired into production via a concurrency-safe reservation tracker; Token accounting types exist and are populated but not yet gated on; Cost accounting explicitly out of scope — see Consequences)

## Context

Progress Intelligence v1 (ADR-0053) answers "is the output actually improving." It does not answer, and was never meant to answer, a different question: "has this Command/Workflow already used more Runtime, Provider calls, or (eventually) Cost than it is permitted to use." A branch can be genuinely converging — new Review findings every round, a Deliverable that keeps changing — and still never stop on its own, because nothing before this Checkpoint enforced a resource ceiling independent of whether the result was improving. WorkCairn's stated goal (少ない人間操作から多くの成果を出す) only holds safely if the inverse is also guaranteed: one request always terminates within finite time, finite Task count, and finite Provider usage, regardless of how well or poorly it is converging.

LoopGuard (ADR-0051: `MaxParallelTasks`, `MaxRevisionCount`, `MaxGeneratedTasks`, `MaxWorkflowTasks`, dependency-cycle detection) already bounds *structural* iteration and work volume. It does not bound wall-clock time, actual Provider invocations, or Token usage — a branch within its Revision Count limit can still run arbitrarily long or make arbitrarily many Provider calls per attempt if each individual attempt itself is slow or retried internally. BudgetGuard is the third, independent boundary this Checkpoint adds, deliberately scoped so it never duplicates LoopGuard's own structural counters and never judges convergence (Progress Intelligence's own job).

### 既に決定済み・実装済みで、本ADRが変更しない事項

- **LoopGuardの構造的カウンタ**（`MaxParallelTasks`/`MaxRevisionCount`/`MaxGeneratedTasks`/`MaxWorkflowTasks`、ADR-0051）は無変更です。BudgetGuardはこれらを一切再実装しません。
- **`policy.ProgressPolicy`/`CompoundProgressPolicy`**（ADR-0053）は無変更です。BudgetGuardは「改善しているか」を一切判断しません——「許可された資源envelopeを超えたか」だけを判断します。
- **TaskServiceが唯一の公式Task状態変更者である**という原則（CONSTITUTION Article 6）は無変更です。`policy.BudgetPolicy`もそれを実装する`budgetTracker`も、Task状態を直接変更しません。
- **Command Ledgerのclaim/replay機構**（ADR-0021）は無変更です。BudgetGuardの会計はこの機構に依存することで、replay時の二重加算を構造的に防いでいます（後述）。

### 新たに決定が必要な事項（本ADRのDecision）

1. Runtime Budgetのscope（1 Task／1 Workflow／outer Command）とその強制方法。
2. Provider Call Budgetの定義（何を「1回のProvider Call」と数えるか）とscope。
3. 並列Branchが同時にProvider Callを要求したときの、race-safeな予約方法。
4. Token使用量の会計方法と、「unknown」を「0」と誤認しないための型。
5. FailureEnvelopeへの分類方法（Code/Category設計）とRecovery UXとの関係。
6. Recovery時にBudgetをリセットするか、継承するか。
7. Cost Budgetを今回のスコープに含めない、という明示的な線引き。

## Decision

### 1. Runtime Budget scope — 1 Reviewed Workflow execution（Workflowレベル）

`autonomy.Contract.MaxRuntime`（既定30分、上限2時間、0は「未設定=Default」——既存の`MaxParallelTasks`等と同じ規約）は、outer CEO Command全体でも、個々のTaskでもなく、**1回のReviewed Workflow execution**（`ExecuteReviewedWorkflow`が呼び出す`RunParallel`一回分）を単位とします。これは「WorkCairnの理想はouter Command全体を有限にすることだが、v1は今日のArchitectureが自然にサポートするWorkflowレベルから始めてよい」という指示への直接の応答です——`RunParallel`はすでに全Branchが共有する一つの呼び出し境界であり、`context.WithTimeout`をここ一箇所に掛けるだけで、Provider呼び出し中の中断を含めて既存のcontext伝播がそのまま効きます。CEO Plan生成（`ceoplan`）自体のProvider呼び出しはこのRuntime Budgetの対象外です——将来、outer Command全体を対象にする拡張は、この境界をより外側（`ExecuteInteractionWorkflow`または更に外）へ移すだけで済むよう、`SetBudgetLimits`という追加的なsetterの形にしてあります。

強制方法は二重です: (a) `budgetCtx, cancel := context.WithTimeout(ctx, maxRuntime)`をRunParallelの先頭で一度だけ作り、Batch Planner呼び出しと全Branch Goroutineへ伝播——実際のProvider HTTP呼び出し中でも既存のcontext cancellationがそのまま中断させます。(b) `policy.FixedBudgetPolicy`が`budgetTracker`のfake-clock対応スナップショットを毎Provider呼び出し前に読み、`ElapsedRuntime >= MaxRuntime`なら`BudgetEscalate`を返します。(a)は物理的なcontext断，(b)は論理的・テスト可能な事前チェックで、本番では同じ`clock`（既定`time.Now`）を共有するため一致します。

**本Checkpointで見つけた実装バグとその修正**: `runBranch`自身のループ先頭（Revision継続でRunParallelに戻らず内側で回り続ける箇所）にも既存の`ctx.Err()`チェックがありましたが、これは`context.DeadlineExceeded`かどうかを区別せず常に`"cancelled"` stageを返していました——RunParallel自身のトップレベルチェックは区別していたにもかかわらずです。これはRuntime Budgetが実際に発火する最も現実的な場面（複数Revisionラウンドを回している最中）でBUDGET_EXCEEDEDが観測されず、汎用的なキャンセルに分類される、という実バグでした。本Checkpointで`runBranch`側も同じ区別をするよう修正し、専用の回帰テスト（`TestRunParallelRuntimeBudgetDeadlineExceededInsideBranchClassifiesAsBudgetNotCancelled`）で、修正前は実際にfailすることを確認した上で追加しています。

### 2. Provider Call Budget — Task実行のExecute呼び出しとReview呼び出しの合計、Workflow実行scope内

`autonomy.Contract.MaxProviderCalls`（既定60、上限300）は、1 Reviewed Workflow execution内で実際に行われた「Task実行のExecute Provider呼び出し」と「Review Provider呼び出し」の合計です。Revision自体（`revision.Execute`）はProvider呼び出しではない（決定的なTask作成のみ）ため数えません。CEO Plan生成・Recovery Command自体のProvider呼び出しも、Runtime Budgetと同じ理由でscope外です。既定値60は、3 branch + review + revision + synthesisという典型的なWorkflow（既存fixtureの並列テスト構成）が通常のhappy pathで消費する呼び出し数（8〜16回程度）に対して十分な余裕を持ちながら、暴走ループを現実的な時間で止められる値として選びました。

### 3. 並列予約 — `budgetTracker`という別個の状態を持つ機構、`policy.BudgetPolicy`とは意図的に分離

`policy.BudgetPolicy`（`Evaluate(ctx, BudgetSignal) (BudgetDecision, error)`）はProgressPolicyと同型のpureな決定境界です——状態を持たず、Task状態を変更せず、Storeへ書き込まず、Eventを発行せず、Providerを呼びません。しかし並列Branchが同時に「残り1回」を見て両方とも進んでしまうrace（Checklist項目16-17で明示的に指摘された懸念）は、pureな関数だけでは原理的に防げません——読み取りと予約の間に別のgoroutineが割り込めるからです。

そこで`internal/service`の`budgetTracker`（非公開）が、`policy.BudgetPolicy`とは別の、意図的にstatefulな機構として存在します: `reserveProviderCall(max int) bool`は単一mutexの下でcheck-and-incrementをatomicに行う、実際にrace-safeな唯一のgateです。`reserveProviderCallBudget`は「まずPolicyを読む（速い、typedな分類の早期判定）→ 常にtrackerの予約を行う（Policyの有無に関わらず、これが権威的な最終判定）」という二段構成で、Policy読み取りは早期に人間可読な分類（Runtime/Provider-call）を与えるための最適化であり、実際の「絶対に超えない」保証はtrackerのatomicな予約だけが担います。`TestBudgetTrackerReserveProviderCallParallelNeverExceedsLimit`（200 goroutine、limit=5、`-race`下で20回repeat）でこの保証を直接検証しています。

`budgetTracker`はTask状態ではありません——1回の`RunParallel`呼び出しにscopeされたephemeralなprocess-local会計であり、呼び出しが返る時点で破棄されます。v1のこの保証は**1プロセス内・1 Commandの1回の実行**に対してのみ有効です。durable/複数プロセスにまたがる将来のRuntimeが必要になった場合、Ledgerベースの予約機構への移行が必要になりますが、`budgetTracker`の`reserve → invoke → record`という構造は、その移行時に呼び出し側を書き換えずに済むよう意図的に整えてあります。

### 4. Token accounting — 既存の`worker.TokenUsage`のnilable設計をそのまま利用、unknown≠0を維持

`worker.TokenUsage{InputTokens *int, OutputTokens *int}`は既存の型（`internal/worker/runner.go`）で、Provider未報告時に`nil`を返す設計が既にありました。これは、Checklist項目9/35が要求した「usage不明を0扱いしない」という制約を、新しい型を作らずに既に満たしていました。`policy.TokenUsageSignal{InputTokens, OutputTokens int, Known bool}`はこの`*int`のnilを`Known bool`へ一度だけ変換する境界で、`budgetTracker.recordUsage`が「一度でも`nil`を観測したら`tokensUnknown`は永続的にtrueのまま」というfail-closed方針（一部の呼び出しだけ既知でも、集計全体を「不明」として扱う）で実装しています。v1では`FixedBudgetPolicy`はToken使用量を読み取り・検証するだけで、Token Budgetとしてゲートしません——Provider間でUsageの信頼性が揃っているという確証がまだ無いためで、実装自体は将来Token Budgetを追加する際に型を変えずに済む形にしてあります。

### 5. FailureEnvelope — 単一Code `BUDGET_EXCEEDED` + `Category`での区別

複数のCodeを増やす案（`RUNTIME_BUDGET_EXCEEDED`/`PROVIDER_CALL_BUDGET_EXCEEDED`/`TOKEN_BUDGET_EXCEEDED`）ではなく、既存のFailureEnvelope（ADR-0041）の`Category`フィールドを使い、単一Code `BUDGET_EXCEEDED` + `Category: "runtime"` または `"provider_call"` という設計を採用しました。これは「Codeを増やしすぎるより、既存Envelopeに自然ならtyped reasonの方を選ぶ」という明示的な指示に沿っています。`reviewedWorkflowOuterEnvelope`のbudgetケースは、Revision Limit/No-Progressとは異なり、Budgetの停止はBranchの試行中どの時点でも起こり得るため（Execute前、Execute後Review前、Round間）、`CommittedEvidence`を無条件にtrueとせず、実際の最後のTaskの状態から計算します。

### 6. Recovery UX — 既存機構の転用を検討したが、v1では意図的に配線しない

既存の`interaction.workflow.recover_revision`（Revision Limit Recovery/No-Progress Recovery、ADR-0052）は、`stalledRevisionTaskID`が「Review verdict = Request Changes かつ RevisionCommandID未設定」というTaskを探して初めて有効化されます。実装過程で、`runBranch`はRequest ChangesとRevisionCommandIDの設定を**同じ同期的なステップ**で行う（間にBudget予約チェックが無い）ことを確認しました——つまりBudget停止は、この「stalled」形を**構造的に決して残せません**。Budget停止が実際に残す状態は、(a) Review未到達（Verdict空）か、(b) 既にRevision Taskが作成済みだが未実行、のいずれかで、どちらも既存のstalled-task検出とは一致しません。

無意味に一致しない条件でRecovery操作を提示するのは、CEOを誤誘導するdead UIになるため、v1では`interaction.go`の`Next()`にBUDGET_EXCEEDEDを追加しませんでした。Budget停止後のCEOに提示されるのは(A)完了済みの成果を見る、だけです——(B)「必要な部分だけ続ける」の正しい実装は、既存のExecuteRevisionを再利用するのではなく、既に作成済みのUnstarted Revision Taskをそのまま`workflow.EvaluateAllReadiness`が拾う形のWorkflow継続（`limit_reached`継続と同じ形）であるべきで、これは`ExecuteInteractionRecoverRevision`とは異なる形のCommandを必要とします。v1はこの新しいCommandを実装せず、明示的な将来課題として残します。

### 7. Recovery時のBudgetリセット/継承 — v1では実質的に該当なし（将来の設計課題として明記）

上記6の決定により、v1ではBudget停止に対するRecovery Commandがそもそも存在しないため、「Recovery時にBudgetをリセットするか継承するか」という問いは今回発火しません。将来、(6)の継続Commandを実装する際にこの問いが再浮上します——ユーザーの当初の想定どおり、Option A（新しいBudget scopeでリセット、CEOがRecoveryを繰り返すと事実上ceilingを回避できてしまう）が単純だが弱い代替であり、Option B（root lineageのBudgetを継承、durable accounting必要）がより意図に忠実です。v1実装時点で(6)のCommand自体が無いため、この選択は次のCheckpointへ持ち越します。

### 8. Cost Budget — 実装しない

`ProviderCallCount × 仮の単価`のような計算はどこにも実装していません。実際の価格表（モデル別、Provider別、cache token、tool billing）が存在するまで、Cost Budgetは実装不可能です——`policy.BudgetSignal.TokenUsage`はToken数のみを保持し、金額換算は一切行いません。

## Consequences

### 実装済み（Decisionどおり）

- `policy.BudgetPolicy`/`FixedBudgetPolicy`/`BudgetDecision`/`BudgetSignal`/`TokenUsageSignal`/`BudgetExceededReason`（`go/internal/policy/budget.go`）。
- `autonomy.Contract.MaxProviderCalls`/`MaxRuntime`（既定値・上限・`Effective*()`、既存規約と同じ0=未設定）。
- `internal/service`の`budgetTracker`（atomic予約、Token集計、fake-clock対応snapshot）と`ReviewedWorkflowRunService.SetBudgetPolicy`/`SetBudgetLimits`/`SetClock`（すべてoptional、後方互換）。
- `RunParallel`のRuntime Budget強制（`context.WithTimeout`＋既存cancellation伝播）と`runBranch`のProvider Call Budget予約（Execute/Review呼び出し直前、reserve→invoke→record）。
- Policy駆動の停止でもRuntime/Provider-callの区別を保つ`budgetPolicyExceededError`（本Checkpointで見つけた分類漏れの修正）。
- `reviewedWorkflowOuterEnvelope`の`"budget"`ケース（単一Code、Category区別、非一括Evidence計算）。
- 単体テスト一式（Policy、tracker予約の並列race安全性、Runtime停止のfake-clock/実context両方、Provider Call Budgetの単独/Policy併用両方、部分的失敗の保存）と、production Command chainを実HTTP mockで通す統合テスト（`TestExecuteReviewedWorkflowProviderCallBudgetExceededPartialResultReplaySafe`：2 branch成功・1 branch Budget停止・Synthesis未実行・replayでProvider呼び出しゼロ）。

### 意図的に据え置いた事項（将来Checkpointの対象）

- **Budget停止に対するRecovery Command**は実装していません（上記6）——v1のCEOは完了済み成果の閲覧のみ可能です。
- **Recovery時のBudget reset/inherit**の決定は上記Command実装まで持ち越します（上記7）。
- **Cost Budget/価格table**は実装していません（上記8）。
- **outer CEO Command全体を対象にしたRuntime/Provider Call Budget**（CEO Plan生成・Recovery自体を含む）は実装していません——scopeはReviewed Workflow execution一回分のままです。
- **durable/Ledgerベースの予約**は実装していません——`budgetTracker`はprocess-local・1呼び出しscopeのみです。
- **Metrics集計**（provider-calls-per-command等）は実装していません——BudgetGuard自体の会計と observability は分離されたままです。

### LoopGuard / Progress Intelligence / BudgetGuard の三分割（Architecture.mdへ反映）

- **LoopGuard**（ADR-0051）: 構造的な作業量・反復回数の上限（Task数、並列数、Revision回数、依存グラフの循環検出）。
- **Progress Intelligence**（ADR-0053）: 出力が実際に改善しているかどうかの判断。
- **BudgetGuard**（本ADR）: 許可された資源（Runtime、Provider呼び出し数、将来的にはCost）の上限。

3つは互いに独立したService層のPolicyであり続けます——TaskServiceだけが引き続き唯一の公式なTask状態変更者です。

### 移行・互換性への影響

すべての変更はadditiveです。`autonomy.Contract`に新フィールドが増えましたが、0値は既存の「未設定=Default」規約に従うため、既存呼び出し元は挙動が変わりません。`SetBudgetPolicy`/`SetBudgetLimits`/`SetClock`は全てoptionalなsetterで、`RunParallel`自体のシグネチャは変更していません。`FailureEnvelope`への`BUDGET_EXCEEDED`追加はJSON Contract上additiveであり、既存Codeの意味は変更していません。
