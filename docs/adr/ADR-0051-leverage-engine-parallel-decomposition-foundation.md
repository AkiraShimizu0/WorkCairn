# ADR-0051: Leverage Engine — Parallel Reviewed Workflow and Decomposition Bounds Foundation

## Status

Proposed

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

## Consequences

- 新しいDomain／Serviceパッケージを追加しません。`go/internal/workflow`に1関数（`EvaluateAllReadiness`）、`go/internal/policy`に1〜2 optional port（`LoopGuardPolicy`、将来の`ProgressPolicy`）が増えるだけです。
- `task.Task`、`ceoplan.Plan`の既存フィールドは変更しません。JSON Contractへの変更はすべてadditive（Autonomy Contractの新フィールド、`ceoplan.ProposedTask`への将来のPriority/ExpectedDeliverable/EstimatedEffort追加）であり、破壊的変更はありません。
- CEOの承認手順は1回のまま変わりません（ADR-0049の既存chainがWorkflow実行modeをGoが内部決定するだけの拡張を吸収します）。
- 並列実行の並行安全性は、既存のCommand Ledger CAS、Task Version CAS という**2重の既存防御**にそのまま依拠します。新しいlock機構は導入しません。
- Synthesisは新しい実行conceptではなく、依存関係を持つ1つのTaskとして表現されるため、Kernel／TaskService／新Domainへの変更が一切不要です。
- 本ADRが対象外とする事項: 再帰分解（Taskが自分の子Taskを動的生成すること）、Specialist Routingの実装、No Progress Detectorの実装、Budget Guardの実際の計測・強制、DEGRADE Policy。これらは将来のADRとして個別に扱います。
