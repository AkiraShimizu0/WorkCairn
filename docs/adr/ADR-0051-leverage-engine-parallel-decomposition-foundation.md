# ADR-0051: Leverage Engine — Parallel Reviewed Workflow and Decomposition Bounds Foundation

## Status

Accepted (Checkpoint E — production wiring; see Implementation Notes below for exactly what "Accepted" covers and what remains explicitly out of scope)

## Context

WorkCairnの新しいNorth Starは「人間の少ない指示から、多くの有用な成果を、安全・有限・追跡可能な形で生み出すこと」です。CEOが「このサービスをもっと売れるようにして」のような1回の依頼から、市場調査・競合調査・戦略立案・LP案・SNS案・QAのような独立した複数Taskを安全に並列実行し、最後に統合する — この能力（以下Leverage Engine）を既存Architectureへ最小の形で接続することが本ADRの目的です。

調査の結果、Leverage Engineが必要とする能力の大部分は、**既に実装済みか、既存境界へ自然に拡張できる形で存在しています**。新規に決定すべき事項は当初想定より小さく、以下に限られます。

### 既に決定済み・実装済みで、本ADRが変更しない事項

- **Decomposition Planner**: `go/internal/ceoplan`が既にProvider／Vault非依存のPlanner（`BuildPrompt` → Runner → `ParseIntent`/`ParseRunnerOutput` → `NormalizeIntent`/`NormalizeCandidate`）として存在します（ADR-0019、ADR-0039）。1回のCEO依頼から複数`ProposedTask`（`proposal_id`／`title`／`required_role`／`assignee_id`／`dependency_ids`／`rationale`、`go/internal/ceoplan/plan.go:59-66`）を生成する経路は既に本番稼働しています。
- **Dependency model**: `go/internal/workflow.Dependency`／`ValidateDependencies`（DFSベースの循環検出、`go/internal/workflow/readiness.go:52-118`）と`go/internal/project.TaskDependency`／`ValidateTaskDependencies`が既に依存グラフの構築・循環拒否・存在検証を提供しています。`ceoplan.validateDependencyGraph`（`plan.go:270-308`）も適用前の同等検証を独立に行います。`task.Task`自体は依存フィールドを持たず（意図的な最小設計、`go/internal/task/task.go:17-25`）、依存はTask ID文字列参照で結ばれる別Artifact（`Task Dependencies.md`）として持たれます。
- **Single approval chain**: ADR-0049の`interaction.plan.approve_and_execute`が、CEOの1回の承認を`ceo_plan.apply`と`workflow.reviewed.execute`という2つのdeterministic child Commandへの承認として扱う仕組みを既に提供しています（`go/internal/process/interaction_approve_and_execute.go:50-161`）。
- **Concurrent-safe child Command claim**: `commandledger.DeriveChildCommandID(parent, discriminator)`（SHA-256ベース、`go/internal/commandledger/ledger.go:181-189`）と`CommandLedgerService.Begin`のatomic-create-if-absent CAS（`go/internal/service/command_ledger_service.go:29-52`）は、異なるCommand IDを持つ複数childが**同時にclaimしても互いに競合しない**ことを既に保証しています。並列実行を妨げているのは、この基盤の欠如ではなく、単に「一度に1 childしかdispatchしないループ」（後述）だけです。
- **Autonomy Contract**: `workcairn-autonomy.v1`（ADR-0035）は既に「実行Task上限」をtyped valueとして固定する仕組みを持っています。

### 新たに決定が必要な事項（本ADRのDecision）

1. 依存関係のないTaskを**同時に複数dispatch**する実行モード（ADR-0023／0024が明示的に対象外とした「Task並列実行」）。
2. 複数Task成果物を統合する**Synthesis**の境界。
3. LoopGuard／Budget Safetyを表現する**typed値の集合**と、その**所有者**。
4. Task lineageを**どこに記録するか**（Task Domainを肥大化させない形）。

## Decision

### 1. Parallel readiness — 新しい純粋関数、既存Domainの拡張のみ

`go/internal/workflow/readiness.go`へ、既存`EvaluateReadiness`（最初のunstarted Taskだけを返す）の兄弟関数として、依存関係を満たした**すべての**unstarted Taskを返す`EvaluateAllReadiness(tasks []Task, dependencies []Dependency, existingEmployees map[string]bool) ([]ReadinessResult, error)`を追加します。既存の`ValidateDependencies`（循環検出・存在検証）をそのまま再利用し、Domain・Contract・Kernelへの新規依存を一切追加しません。既存`EvaluateReadiness`／`EvaluateTaskReadiness`は無変更で維持し、Sequential Workflow（ADR-0023）とReviewed Workflow（ADR-0024）の既存呼び出し元は影響を受けません。

### 2. Parallel dispatch — 既存Reviewed Workflowループの並列化、Kernel／TaskServiceは無変更

`ReviewedWorkflowRunService.Run`（`go/internal/service/reviewed_workflow_run_service.go:84-176`）の1ラウンドを「1 Task選択」から「`EvaluateAllReadiness`が返す全ready Taskの束」へ拡張します。各TaskはBounded goroutine poolで同時に処理し、既存の`ExecuteTask → ExecuteReview → (Request Changesなら)ExecuteRevision → 再Review`という1 Task分の分岐（ADR-0024）を**そのまま**各goroutine内で呼び出します。並行数の上限（後述`MaxParallelTasks`）を超えないbounded concurrencyは、`go/internal/httpapi/async_command.go`が`maxConcurrentAsyncCommands`で既に使っているsemaphoreパターンを踏襲します。

各TaskのCommand IDは既存どおり`DeriveChildCommandID(parentCommandID, "task.execute:"+taskID)`等で独立に導出され（ADR-0024と同一discriminator形式）、各TaskのState mutationは既存の`ExecutionService.Execute` → `TaskService.Start/Complete/Fail/Hold`だけを経由します。並列化によって新しいTask状態変更経路は一切追加されません。同一Taskの二重dispatchは既存のVersion/CAS（`task.Store.Update`のexpected version）がそのまま防ぎます。

1ラウンド（束）が全て終端（成功またはRequest Changes分のRevision完了）に達してから、次ラウンドの`EvaluateAllReadiness`を呼びます。これはADR-0023の「各Task完了後に再plan」原則を、1 Task単位から1 Round単位へ拡張したものであり、破壊的変更ではありません。

外側Commandは既存`workflow.reviewed.execute`のpayloadへadditive optional field（`parallel bool`、既定false=現行のSequential挙動）として表現し、新operationは追加しません。ADR-0049の`interaction.plan.approve_and_execute`は、内部で呼ぶWorkflow実行モードをGoが決定するため、CEOから見える承認手順（1回）は変わりません。

### 3. Result Synthesis — 新しい実行primitiveではなく、依存関係を持つ1つのTask

Synthesisは特別なKernel機構やSynthesis Serviceとして実装しません。Synthesisは「並列branch全TaskのTask IDをdependency_idsに持つ、通常のTask」です。既存の`EvaluateReadiness`／`EvaluateAllReadiness`の依存解決規則がそのまま「全branch完了後にだけSynthesisがreadyになる」ことを保証します。必要な変更は`ceoplan.NormalizeIntent`（現在は直列chainだけを構築、`intent_normalizer.go:133-138`）を、fan-out（複数rootが並列dependencyなし）→fan-in（1つの統合TaskがN branchすべてに依存）というDAG形状も構築できるよう拡張することだけです。TaskService、Kernel、新しいDomainは一切必要ありません。

### 4. Specialist Routing — 既存Runner Registryを変更しない、将来の差し込み点だけ確認

現在のRunner解決は`Employee.Model → Runner Registry.Resolve`という静的な1段階だけです（`go/internal/runner/registry.go:62-75`）。Task種別に応じた動的Runner選択は存在しません。ROADMAP（`docs/ROADMAP.md`の`ADR-0036の workcairn-auto を起点に...`の項）が既にこの拡張を「複数Provider導入時だけ」の将来課題として記録しています。本ADRはこれを追認するだけで、今回のvertical sliceには含めません。挿入点は`WorkerService.Execute`が`runners.Resolve(...)`を呼ぶ直前（`go/internal/service/worker_service.go:122`）で確定しています。

### 5. LoopGuard / Budget Safety — Autonomy Contractへのadditive拡張、単一の所有者

LoopGuardとBudget Safetyの両方を、既存`workcairn-autonomy.v1`（ADR-0035）へ**additive**フィールドとして追加します。新しいContract typeは作りません。

```
AutonomyContract (既存 workcairn-autonomy.v1 の追加フィールド)
  MaxTasks               (既存 — 実行Task上限、通常＋Revision Taskの合計)
  MaxGeneratedTasks       int  // decomposition時にceoplanが生成できるTask数上限
  MaxChildTasksPerTask    int  // 将来の再帰分解が1 Taskから生成できる子Task数上限（今回未実装、境界だけ確保）
  MaxTaskDepth            int  // 再帰分解の深さ上限（今回未実装、境界だけ確保）
  MaxParallelTasks        int  // 1ラウンドで同時dispatchできるTask数上限
  MaxRevisionCount        int  // 同一Taskに対するRequest Changes往復回数上限
  MaxTokens / MaxCost / MaxRuntime / MaxToolCalls  *int  // 将来のBudget Guard用、今回未実装
```

これらは既存`MaxTasks`と同じ設計原則に従います。**CEOが値を送信するのではなく、Goが現在のOrganization／Workflow文脈から決定的に構築**し、plan digestへ含め、承認後はWorkflow evidenceへ保存し、execute requestが同じcontractを含まなければ拒否します（ADR-0035の既存原則、変更なし）。

**所有者は単一**: これらの値の**検証**（上限超過の判定）は`policy`パッケージの新しいpure decision関数（例: `policy.EvaluateLoopGuard(contract, currentState) LoopGuardDecision`）が担いますが、**Task stateの変更は一切行いません**。上限超過時、`ReviewedWorkflowRunService`は既存の`TaskService.Hold`（HOLDの場合）または既存の`blocked`/`limit_reached`終端状態（ADR-0023が既に持つ語彙、ESCALATEに相当）を返すだけです。DEGRADE（範囲を縮小して続行）は今回の対象外とし、将来のPolicy拡張として記録します。LoopGuard／BudgetGuard自身がTaskService以外からTask stateを書き換えることは、CONSTITUTION Article 6に反するため今後も禁止します。

### 6. Dependency cycle対策 — 新規実装なし

decomposition時（`ceoplan.validateDependencyGraph`）と実行readiness時（`workflow.ValidateDependencies`、今回`EvaluateAllReadiness`もこれを再利用）の**2段階**で、既存のDFSベース循環検出がそのまま機能します。並列dispatch前にラウンド全体の依存グラフを一括検証し、1つでも問題があれば**そのラウンド全体を副作用前に拒否**します（Command Ledgerの「claim before effects」原則、ADR-0021と同型）。

### 7. Revision / Retry loop対策

`MaxRevisionCount`（上記5）を、`ReviewedWorkflowRunService.Run`内の既存Request Changes分岐（`forcedTaskID`を設定する箇所、`reviewed_workflow_run_service.go:139-149`）へ、同一source Task IDに対するRevision回数カウンタとして追加するだけの、既存ループへの小さな追加です。新しいLoop構造は不要です。

No Progress Detection（同じQA指摘の繰り返し、Deliverable未改善、Cost増加のみ）は今回実装しません。接続点として、`policy.ExecutionPolicy`と対になる新しいoptional port `policy.ProgressPolicy`（`Evaluate(ctx, ProgressInput) (ProgressDecision, error)`）を、`ReviewedWorkflowRunService`が各Review verdict直後に呼べる場所として確保します（未実装、interfaceの置き場所だけを本ADRで確定）。

Retryについては、既存ADR群（0021、0049）が繰り返し確立している**「自動retryを行わない」原則をLeverage Engineでも維持**します。同一Command IDの再送は既存のreplay機構（結果を返すだけ、副作用なし）であり、新しい試行は必ず人間／operatorが新しいCommand IDで明示的に行います。有限retryを自動化する新機構は設計しません。

### 8. Lineage / Causality — 新しいTaskフィールドではなく、既存Eventフィールドの初回活用

`task.Task`と`event.Event`を調査した結果、**`event.Event`は既に`CorrelationID`／`CausationID`フィールドを持っています**（`go/internal/event/event.go:21-22`）が、現在どのPublish箇所からも一切設定されていません（全リポジトリ検索で確認）。

Leverage Engineの最小lineageは、この2つの既存フィールドを初めて意味のある値で埋めることで実現します。**Task Domain（`task.Task`）自体は変更しません** — これはRevision（`SourceTaskID → RevisionTaskID`、`go/internal/revision/revision.go:21-32`）が既に確立した「lineageはTask構造体ではなく、別の関連レコードやCommand識別子で表現する」という既存の設計原則と一致します。

- `CorrelationID` = そのTask束を生んだRoot outer Command ID（例: `interaction.plan.approve_and_execute`のCommand ID）。並列dispatch・Synthesis含む、1回のCEO承認から生じた全Task／Eventが同じCorrelationIDを共有します。
- `CausationID` = 直接の親Command ID（`DeriveChildCommandID`の`parentCommandID`引数として、各claim関数に既に渡されている値）。

どちらも**新しいContract変更を必要としません**（フィールドは既に存在し、`omitempty`で後方互換）。必要なのは`TaskService.newTaskEvent`等のEvent構築箇所（`go/internal/service/task_service.go:218-231`）が、呼び出し元から渡されたCorrelationID／CausationIDを`event.New`へ伝播する配線だけです。`Generation`（分解の深さ）は、再帰分解（`MaxChildTasksPerTask`／`MaxTaskDepth`が実際に使われる段階）まで導入を延期します — 今回のvertical sliceはroot分解＋並列実行だけであり、常に0になるフィールドを先取りして追加しません。

### 9. TaskServiceの単一所有権維持

並列dispatchはExecutionServiceを複数goroutineから呼び出しますが、各呼び出しは独立した1 Taskに対する既存の`readiness → ApprovalPolicy → TaskService.Start → WorkerService.Execute → DeliverableStore.Save → TaskService.Complete/Fail → ExecutionPolicy → TaskService.Hold`（ADR-0007）をそのまま辿ります。TaskServiceの`mutate`メソッド（`task_service.go:165-198`）はTask単位でCAS保護されており、並列化によって複数goroutineが同一Task IDを同時に変更しようとしても、片方は`ErrVersionConflict`で安全に拒否されます。TaskServiceの外側に新しい状態変更経路は作りません。

## Implementation Notes (Checkpoint D)

このCheckpointで、上記Decisionの一部を実装しました。実装できた部分・意図的に据え置いた部分・設計から逸脱した部分を分けて記録します（Status変更の判断根拠）。

### 実装済み（Decisionどおり）

- **1. Parallel readiness**: `workflow.EvaluateAllReadiness`（`go/internal/workflow/readiness.go`）を、既存`ValidateDependencies`をそのまま再利用する形で追加。既存`EvaluateReadiness`／`EvaluateTaskReadiness`は無変更。決定的順序（入力`tasks`スライスの並び順をそのまま維持、mapを走査に使わない）を含め、11件のテストで検証済み。
- **2. Parallel dispatch（実行部分）**: `ReviewedWorkflowRunService.RunParallel`（`go/internal/service/reviewed_workflow_run_service.go`）を、既存`Run`と共存する形で追加。1ラウンド分のready Task束をbuffered channel semaphore + `sync.WaitGroup`でbounded並列実行し、束全体が終端に達してから次ラウンドを`planReviewedWorkflowBatch`経由で再planします。各TaskのState変更は既存`TaskService`だけを経由し、新しいTask状態変更経路は追加していません。同一Taskの二重dispatchは既存Version/CASがそのまま防ぐことを、並行dispatchテストで確認済みです（`TestRunParallelDuplicateDispatchIsSafeUnderCAS`）。
- **9. TaskServiceの単一所有権**: 並列dispatch下でもgoroutineはStore／Eventへ直接書き込まず、`ExecutionService → TaskService`だけを経由することを確認済みです。
- **8. Lineage**: `event.Event`の既存`CorrelationID`／`CausationID`を、新しい`event.Correlation`＋`context.WithValue`ベースの配線（`go/internal/event/correlation.go`）で初めて意味のある値へ設定しました。`CorrelationID`はラウンド呼び出しの`parentCommandID`、`CausationID`は各段階の子Command ID（`task.execute:<id>`等）です。`task.Task`自体は無変更です。並列2branchで共有CorrelationID／別CausationIDになることをテストで確認済みです（`TestRunParallelStampsSharedCorrelationAndPerBranchCausation`）。
- **3. Synthesisの依存表現**: Synthesisは新しいServiceを持たず、既存Dependency machineryで表現される通常のTaskとして扱われることを、fakeベースの`TestRunParallelSynthesisTaskWaitsForAllBranches`と実Vault・実HTTPを使う`TestExecuteReviewedWorkflowParallelRunsIndependentTasksThenSynthesis`の両方で確認しました（後者は3独立branch → 1 Synthesis Taskの依存グラフを`ExecuteProjectDependencies`で直接構成）。
- **5. `MaxParallelTasks`のみ**: Autonomy Contract（`autonomy.Contract`）へ`MaxParallelTasks`をadditive fieldとして追加しました。`0`は「未設定（既存永続Contractとの後方互換）」として有効とし、`EffectiveMaxParallelTasks()`が既定値3へ解決します。既定値3はPublic BetaがMac 1台につきProvider接続1本という制約から選びました。負値・`MaxParallelTasksCeiling`（10）超は`Validate()`が拒否します。

### 意図的に据え置いた事項（LoopGuard／Budgetの残り）

Decision section 5が列挙した`MaxGeneratedTasks`／`MaxChildTasksPerTask`／`MaxTaskDepth`／`MaxRevisionCount`／`MaxTokens`／`MaxCost`／`MaxRuntime`／`MaxToolCalls`は、**今回のCheckpointでは追加していません**。これらはまだ使う側（再帰分解、Revision往復回数の実際の強制、Budget計測）が存在せず、今追加すると常に未使用のfieldになるため、CONSTITUTION／CLAUDE.mdの「投機的な将来汎用性を先取りしない」原則に従い、実際に使われる段階まで延期します。今回runtimeで強制するLoopGuardは`MaxParallelTasks`と、既存の`MaxTasks`（Round消化数の上限としてそのまま再利用）、および既存`ValidateDependencies`による循環拒否の3点だけです。

Decision section 5が想定した`policy.EvaluateLoopGuard`のような専用policy pure関数も、今回は作っていません。`MaxParallelTasks`1 fieldだけを強制する現段階では、`RunParallel`内でのローカルなclamping（`maxParallelTasks <= 0` → 1、`maxParallelTasks > maxTasks` → `maxTasks`）で十分であり、専用Policy portを今追加するのは時期尚早と判断しました。LoopGuardの次元が増えた段階（次のCheckpoint）で、Autonomy Policy／Execution Limits／Budget Limits／Loop Limitsを分離可能な形に切り出すことを推奨します。

Decision section 7の`MaxRevisionCount`・Revisionループ対策、No Progress Detector用`policy.ProgressPolicy`の接続点確保も、今回は着手していません（ADR原文が既に「今回実装しません」としていた事項で、逸脱ではなく元々の対象外です）。

### 設計から逸脱した事項

- **外側Commandの表現方法**: Decision section 2は「既存`workflow.reviewed.execute`のpayloadへadditive optional field（`parallel bool`）として表現し、新operationは追加しない」としていましたが、実装では代わりに**新しい兄弟関数`process.ExecuteReviewedWorkflowParallel`と新operation文字列`workflow.reviewed.execute.parallel`**を追加しました。理由: `parallel bool`を既存operationへ追加すると、既存のHTTP／Interaction allow-listを経由するあらゆる既存呼び出し経路が、並列実行安全性を個別に監査されないまま並列modeへ到達しうる状態になります。新しい独立operationにすることで、この段階のParallel Dispatch機能は**どのHTTP／Interaction allow-listにも配線されておらず、直接のGo呼び出し（テスト、将来の明示的配線）以外から到達不可能**です。これは元のDecisionが意図した「CEOから見える承認手順は変わらない」という安全側の目的そのものを、より強く達成する選択です。次のCheckpointで実配線する際は、この新operationをallow-listへ追加するか、既存operationへの統合へ設計変更するかを別途決定する必要があります。
- **Decomposition側のfan-out/fan-in生成**: Decision section 3が「必要」としていた`ceoplan.NormalizeIntent`のfan-out/fan-in DAG生成拡張は、今回のCheckpointの対象外でした（今回のスコープはCEO Plan生成側ではなく実行側のみ、との明示指示）。そのため、今回証明できたのは「Dependency machineryとEvaluateAllReadinessが正しくSynthesisをgateする」ことだけであり、「1回のCEO依頼からLLMが実際にfan-out/fan-in形状を提案する」経路は未実装のまま残っています。これがDecision原文と実装の間で最も大きい範囲差です。

## Implementation Notes (Checkpoint E — Production Wiring)

Checkpoint Dが「実行側の基盤」を作った一方、意図的に据え置いた3点――(a) 外側Commandの表現、(b) `ceoplan`のfan-out/fan-in生成、(c) `MaxGeneratedTasks`/`MaxRevisionCount`――をこのCheckpointで実装し、CEOの実際のPublic Beta経路（`interaction.plan.approve_and_execute`）へ配線しました。

### 3.a 外側Commandの表現 ― Checkpoint Dの逸脱をB案（既存operationへの統合）で解消

Checkpoint Dは安全側の判断として新operation `workflow.reviewed.execute.parallel` を追加しましたが、これは元のDecision section 2（「新operationは追加しない」）からの逸脱として記録されていました。このCheckpointで**その新operationと`ExecuteReviewedWorkflowParallel`を削除し**、既存の唯一のoperation `workflow.reviewed.execute`（＝`process.ExecuteReviewedWorkflow`）自身が、内部で`ReviewedWorkflowRunService.RunParallel`を常に駆動するよう変更しました。

具体的には、1ラウンドの`workflow.EvaluateAllReadiness`が返すready Task数が1件なら実質的に逐次実行と同じ挙動になり、複数件ならGoが自動的にbounded並列実行します。**どちらになるかは毎ラウンド、依存グラフの形だけから自動的に決まり、呼び出し元がparallel／sequential／concurrencyを指定する経路は存在しません**（`ExecuteReviewedWorkflowInput`にそのようなフィールドを追加していません）。これはDecision section 2が意図した「`parallel bool`のような呼び出し元選択肢」よりも強い形で、「CEOやUIにparallelという概念を一切見せない」というこのCheckpointの最重要目標を満たします。

既存の逐次`ReviewedWorkflowRunService.Run`メソッド自体は削除していません（無変更のまま残置）。呼び出し元がゼロになった状態ですが、既存の単体テスト群がこの関数自身の挙動（`planReviewedWorkflowStep`を経由する1 Task単位のround-robin、Revision継続時の`forcedTaskID`等）を独立して検証し続ける価値があり、将来の診断／比較用途に備えて残す判断をしました。

### 3.b `ceoplan`のfan-out/fan-in生成 ― `parallel_with_previous`

`ceoplan.IntentStep`へ`ParallelWithPrevious bool`（JSON: `parallel_with_previous`、既定false）を追加しました。LLMが供給する意味論的signalは「直前のstepと同時に着手できるか」という1個のbooleanだけです。Go（`NormalizeIntent`）が、この一連のbooleanから**構造的に**依存グラフを組み立てます（現在の「開いているグループ」のmembersとupstream依存を追跡し、trueならグループに参加、falseならグループ全メンバーへ依存する新Taskを作り、新しいグループを開く）。ADR-0039が既に確立した「LLMは依存関係IDやグラフ構造を一切出力しない」原則をfan-out/fan-inへそのまま拡張したものです。

すべてのstepが`false`の場合、Checkpoint D以前の線形chain構築とbyte-for-byte同一の結果になることを回帰テストで固定しています（`TestNormalizeIntentAllFalseParallelWithPreviousReproducesLinearChain`）。`IntentJSONSchema`／`BuildPrompt`双方をこのfieldに合わせて更新し、Providerフィクスチャ（`fixtures/provider/claude_ceo_intent_request_v1.json`）も同期しました。

新たな依存グラフ検証は追加していません。既存の`validateDependencyGraph`（自己依存・存在しない依存・重複依存・循環の拒否）を`NormalizeCandidate`経由でそのまま再利用します。「Proposal ID重複」は、IDが常にGoの連番生成（`PROPOSED-%03d`）であり構造的に発生し得ないため、専用チェックを追加していません。「Synthesis依存不足」も、この構築方式では常に構造的に整合したグラフしか生成できないため、独立した失敗モードとして存在しません。

### 3.c `MaxGeneratedTasks` / `MaxRevisionCount`

**`MaxGeneratedTasks`**（既定5）は`autonomy.Contract`にではなく、`go/internal/ceoplan`パッケージ自身の定数として実装しました。理由：Autonomy Contractのライフサイクルは「Workflow実行スコープ」（ADR-0035）であり、Plan生成の時点ではまだ構築されていません（`resolveAutonomyContract`はPlanの内容――担当者・Reviewer――に依存するため、Plan生成の**後**にしか呼べません）。Plan生成時点で未成熟・空のAutonomy Contractを無理に先行構築するより、Decomposition自身の責務としてこの定数を持たせる方が、2つの異なるライフサイクルを不必要に結合しない、より単純な設計です。`NormalizeIntent`は非reviewステップ数が`MaxGeneratedTasks`を超えるIntentを、Employee解決や依存グラフ構築より前に、型付きエラー（`NormalizationExcessiveTaskCount`）として拒否します。

**`MaxRevisionCount`**（既定2＝初回実行＋Revision2回）は`autonomy.Contract`へ`MaxParallelTasks`と同じadditiveパターン（`0`＝未設定→既定値、負値・ceiling超は`Validate()`拒否）で追加しました。`ReviewedWorkflowRunService.runBranch`が各branch独立にRevision回数を数え、上限到達時は**新しいRevision Taskを作らずそこで停止**します。既にCompletedになっているTask（Reviewが走る時点で実行は既に完了済み）を`TaskService.Hold`へ遷移させることは、Task状態機械上不可能（`Hold`は`InProgress → OnHold`専用の遷移で、Recoveryが「実行途中で止まったTask」を扱うための仕組みです）と判明したため、Decision section 5が候補として挙げた「Hold」ではなく、既存の`limit_reached`系終端語彙と同型の、型付きstage（`"revision_limit"`）・Code（`REVISION_LIMIT_REACHED`）を持つ結果として表現しています。直近のReview verdict（Request Changes）自体が、既存の正規Review artifactとしてすでに恒久的に記録されている――これがこの終了理由の観測可能な証拠です。

Autonomy Contractの責務分離：`MaxParallelTasks`／`MaxRevisionCount`はコード内コメントで「LoopGuard」として明示し、既存の`TaskExecution`/`Review`/`Revision`/`ExternalPublish`/`Spending`（Autonomy Policy）や`ExecutionLimit`（Execution Limits）とは異なる責務であることを分かるようにしています。`MaxGeneratedTasks`は`ceoplan`側のLoopGuardです。大規模refactorは行わず、将来Autonomy Policy／Execution Limits／Budget Limits／Loop Limitsを別型へ切り出す余地は残しています。

### Correlation root の修正

Checkpoint Dの実装は、`CorrelationID`を`RunParallel`の直接の親（`workflow.reviewed.execute`子Command自身のID）に設定していました。しかしDecision section 8の原文は「そのTask束を生んだ**Root** outer Command ID（例: `interaction.plan.approve_and_execute`のCommand ID）」を意図しており、両者は`interaction.plan.approve_and_execute`経由の場合に一致しません。このCheckpointで`RunParallel`／`runBranch`のシグネチャを`parentCommandID`（子Command ID導出専用）と`correlationID`（lineageのroot、呼び出し元が指定可能）に分離し、`ExecuteReviewedWorkflowInput.CorrelationID`（additive、空なら自己相関でChecking D以前の挙動を保つ）を新設、`runInteractionWorkflowChain`がその呼び出し元自身のroot（`interaction.plan.approve_and_execute`の外側Command ID、または独立呼び出し時は`interaction.workflow.execute`自身のID）を渡すよう配線しました。実CEO承認チェーンを通したEnd-to-Endテストで、Task A/B/C/Synthesisの全EventのCorrelationIDが外側`interaction.plan.approve_and_execute`のCommand IDと一致することを確認済みです。

### 実施した検証・Production wiring確認

- 本番Command経路（`interaction.plan.approve_and_execute → ceo_plan.apply → workflow.reviewed.execute（自動並列）→ Task A/B/C → Synthesis`）をMock Providerと実HTTP・実Command Ledgerで通したGo Integration test。
- 同一outer Command IDでのreplayが二重dispatchしないこと（既存Command Ledger claim/replayをそのまま利用、新しいidempotency機構は追加せず）。
- 1 branch失敗時、他branchの結果を保持したまま全体を非成功として報告し、Synthesisが一切dispatchされないこと。
- 既にcancelされたcontextではdispatchが一切発生しないこと。
- Conversation Projection（ADR-0047、無変更）が、既存の`turn.Workflow.Tasks`スライス走査だけで複数Task（並列で生成されたものを含む）を自然に表示できること――架空の"並列実行中です"のようなcanonical Turnを新設していません。
- Plan UI（`web/app.js`）へ、既存`dependency_ids`から導出する最小限のpresentation改善（並行可能／統合対象の一言ヒント）を追加。新しいDAG可視化コンポーネントは追加していません。
- Browser Gate 1本（`tests/browser/parallel-workflow.spec.mjs`）を追加し、実際のUI経路でCEOの単一承認からA/B/C＋Synthesis完了までを確認。厳密な並行数はGo testの責務のまま。

## Status Decision

Checkpoint Dが記録した3つの逸脱・据え置き事項――外側Commandの表現、fan-out/fan-in生成、`MaxGeneratedTasks`/`MaxRevisionCount`――はいずれもこのCheckpointで解消しました。Decision section 1〜9の主要な要素（parallel readiness、parallel dispatch、Synthesis表現、LoopGuardの最低ライン、lineage、TaskServiceの単一所有権）はすべて実装済みで、Public Betaの実際の単一承認経路から到達可能です。よってStatusを**Accepted**とします。

Accepted後も対象外のまま残る事項（Decision section 4/5/7の一部、Consequencesに明記）：再帰分解、Specialist Routingの実装、No Progress Detectorの実装、Budget Guard（`MaxTokens`/`MaxCost`/`MaxRuntime`/`MaxToolCalls`）の実測・強制、`MaxChildTasksPerTask`/`MaxTaskDepth`、DEGRADE Policy。これらは将来の別ADRとして個別に扱います。

## Consequences

- 新しいDomain／Serviceパッケージを追加しません。`go/internal/workflow`に1関数（`EvaluateAllReadiness`）、`go/internal/policy`に1〜2 optional port（`LoopGuardPolicy`、将来の`ProgressPolicy`）が増えるだけです。
- `task.Task`、`ceoplan.Plan`の既存フィールドは変更しません。JSON Contractへの変更はすべてadditive（Autonomy Contractの新フィールド、`ceoplan.ProposedTask`への将来のPriority/ExpectedDeliverable/EstimatedEffort追加）であり、破壊的変更はありません。
- CEOの承認手順は1回のまま変わりません（ADR-0049の既存chainがWorkflow実行modeをGoが内部決定するだけの拡張を吸収します）。
- 並列実行の並行安全性は、既存のCommand Ledger CAS、Task Version CAS という**2重の既存防御**にそのまま依拠します。新しいlock機構は導入しません。
- Synthesisは新しい実行conceptではなく、依存関係を持つ1つのTaskとして表現されるため、Kernel／TaskService／新Domainへの変更が一切不要です。
- 本ADRが対象外とする事項: 再帰分解（Taskが自分の子Taskを動的生成すること）、Specialist Routingの実装、No Progress Detectorの実装、Budget Guardの実際の計測・強制、DEGRADE Policy。これらは将来のADRとして個別に扱います。
