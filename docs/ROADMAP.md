# Workspace OS Roadmap

## North Star

Workspace OSを、Workspace Kernelを中心とするGo単一バイナリへ移行します。Pythonは段階移行の完了後に廃止し、Project、Organization、Workflow、Task、Event、Worker、Policy、Scheduler、AuditをGoの型付きDomain／Serviceとして運用します。

ロードマップは方向と完了条件を示します。確定した設計判断は`docs/adr/`、現在の構造は`docs/Architecture.md`を参照してください。

## Released — v0.1.0 Python Foundation

完了済み：

- Obsidian Vaultを利用した社員・組織・Project・Task管理
- 社員IDによる割当、Identity Policy、安全な改名履歴
- Fake Runner対応TaskExecutor、ClaudeRunner、PromptBuilder、ModelRouter
- Deliverable、Progress、Audit、Review、Revisionフロー
- dry-run、明示承認、原子的更新、バックアップ、rollback
- WorkflowEngineによる1タスク実行とReview分岐

位置付け：運用可能なlegacy/reference実装であり、最終アーキテクチャではありません。

## Completed Migration Foundations — v0.2

完了済み：

- CEOCommandServiceのplan-only／approved apply
- Project Task DomainとWorkflow readinessのGo移植
- Python→Go CoreのJSON Contract v1と`workspace-core` CLI
- GoCoreClientによるTASK-ID、Task検証、状態遷移、readiness委譲
- Python／Go共有fixtureと明示fallback方針
- ADR運用の導入

完了条件：Project／Workflowの主要な純粋ビジネスルールがGoを正本とし、PythonはAdapterとしてGoの判定を利用する状態。

## Current — v0.3 Kernel First

実装済み：

- Workspace KernelとService Registry、started／stopped lifecycle
- ProjectService、WorkflowServiceのKernel経由実行
- 型付きEvent Domain、in-process Event Bus、EventService
- Go Task Domain、TaskStore、Version/CAS、TaskService
- Task lifecycle Eventとpartial publication failure
- Go Worker Domain、PromptBuilder port、Runner interface／Registry
- WorkerServiceのcontext、usage、duration、型付きerror境界
- ApprovalPolicy、ExecutionPolicy、ExecutionService
- 正常時`TaskStarted → TaskCompleted`、失敗時`TaskStarted → TaskFailed → TaskHeld`

次の優先順位：

1. Go PromptBuilder
   - Python PromptBuilderの会社・社員・Project・Task contextをGoへ移す
   - Prompt内容とProvider呼び出しを分離したまま維持する
2. Go Provider Runner Adapter
   - Claude Runnerを最初の実Provider Adapterとして追加する
   - APIキー、model設定、timeoutはRuntime／Config層へ置く
   - 実APIなしのcontract／mockテストを先に完成させる
3. Go Runtime／Config
   - Kernel bootstrap、PromptBuilder、Runner Registry、Provider設定をcompositionする
   - `.env`はRuntime Adapterだけが扱い、Coreへ渡さない
4. Deliverable／Audit Adapter
   - WorkerResultの保存をDomainから分離する
   - AuditをEvent subscriberとして実装する
5. OrganizationService
   - Employee ID、Identity、Organization参照の正本をGoへ移す

v0.3完了条件：承認済み1タスクをGo RuntimeからFake／実Provider Runnerへ渡し、Task lifecycle、成果物、AuditをGoの境界で安全に完結できること。

## Planned — v0.4 Durable Runtime

候補機能：

- 永続TaskStore Adapterとtransactional Outbox
- Command ID／Idempotency Key／Command Ledger
- durable Event／Audit Storeと再配送方針
- Scheduler、Notification、Metrics subscriber
- 複数Task Workflow、dependency、Review、RevisionのGo orchestration
- Project／Organization／Identityの永続Adapter
- CLI、daemon、HTTP APIなど複数入口のKernel共通利用
- crash recovery、graceful shutdown、長時間実行監視

完了条件：process再起動、通信retry、複数実行主体が存在しても、Task、Event、Auditの状態を一貫して復旧・追跡できること。

## Target — v1.0 Go Only

- Python TaskExecutor、Worker、PromptBuilder、ModelRouter、Provider Runnerを削除
- Python ProjectManager、Organization、Vault I/OをGo Adapterへ置換
- Python runtime、CrewAI、Python package依存を製品実行経路から除去
- Workspace KernelをCLI／API／daemonの唯一の中核入口とする
- version付き外部Contract、migration tool、運用手順を確定
- セキュリティ、性能、race、障害復旧、データmigrationをRelease Gateとして検証

完了条件：通常運用、AI実行、Project／Organization管理、監査、復旧がGoだけで実行でき、Python削除後も全契約テストが成功すること。

## Cross-Cutting Release Gates

各段階で次を維持します。

- Go Coreへ新しいProvider／Storage依存を持ち込まない
- JSON Contractと共有fixtureの後方互換性
- 明示承認前の副作用ゼロ
- TaskServiceだけがTask状態とTask Eventを変更する
- partial failure、timeout、cancellation、並行実行の観測可能性
- `go test ./...`、`go test -race ./...`、`go vet ./...`、Python移行テストの成功
- 実Vault、`.env`、APIキー、社員・実Projectデータの保護
- 重大判断をADRへ記録し、ConstitutionとArchitectureを同期する
