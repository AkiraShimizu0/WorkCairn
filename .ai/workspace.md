# WorkCairn — AI Workspace Context

このファイルは、WorkCairnへ参加するAIエージェント向けの短い入口です。詳細な規範は`AGENTS.md`、`docs/CONSTITUTION.md`、設計は`docs/Architecture.md`と`docs/adr/`を参照してください。

## Mission

WorkCairnは、自分専用のAI会社へ仕事を任せ、必要な質問と重要な承認だけを人間へ返す製品です。Project、Task、AI社員、Workflow、EventをWorkspace Kernelで管理し、製品コード、build、test、release、distributionはGo Onlyです。

## Current State

- `workcairn`は通常運用、CEO plan、Project／Task、Organization／Identity、Task execution、Review、Revision、Deliverable／AuditをGoだけで提供します。
- ADR-0020に基づく`recovery-inspect|plan|apply`は、Task／artifact／Audit／temporary stateをread-only診断し、証拠SHA-256とTask Versionに拘束された2つの明示Task recoveryだけを提供します。Event replayやartifact adoptionはしません。
- ADR-0021のCommand Ledger foundationは、明示Command ID付き通常Task／Review／Revision、CEO apply、Project／Task writer、Organization writer、Sequential／Reviewed Workflow executionで副作用前claim、request digest、terminal outcomeを永続化し、同一requestを副作用なしでreplayします。Project作成前／Organization commandはworkspace scope、既存Project内commandはproject scopeを使い、running recordはRecoveryで診断し自動resumeしません。
- ADR-0022の`workcairn-daemon`はloopback既定の`workspace-command.v1`を提供し、HTTPではCommand IDを必須化します。CLIと同じGo process／Serviceを直接利用し、graceful shutdownとread-only Ledger statusを持ちます。remote公開、認証／TLS、durable queueは未実装です。
- ADR-0023の順次Multi-task Workflowに加え、ADR-0024のReviewed Workflowは各Task後に既存Reviewを実行し、Request Changesなら既存Revisionで修正Taskを作成・実行・再Reviewしてから本流へ戻ります。役割付きchild Command ID、Revision Task限定targeted readiness、最大100 Taskを使い、自動resume／並列実行はしません。
- ADR-0025のone-shot Schedulerは承認済み`workspace-command.v1`をoffset付き時刻以後に既存Processへ一度だけ配送します。Schedule CASとtarget Command Ledgerを再利用し、crash後の`dispatching`を自動resumeしません。
- ADR-0026のNotification／MetricsはTask／Review／Revision EventへRuntime edgeから接続します。Notificationはpayload-free immutable local Inbox、Metricsはbounded process-local counterで、subscriber失敗はcanonical factをrollbackしません。
- ADR-0027のExternal Actionは既存Deliverable digestに拘束したrequest evidenceを先行commitし、WordPress公開、result evidence、`action.completed`の順で調停します。credentialはRuntime edgeだけにあり、公開後のpartial failureをrollbackしません。
- Public Release Preparationとして、temporary VaultからのOperator Guide、linker注入するversion／commit metadata、Go Only archive／checksum、非loopback bind拒否、release checklistを追加しました。
- ADR-0028/0029のInteraction Sessionは自然言語request、CEO質問回答、再plan、plan digest承認、既存Project／Task writer適用、Reviewed Workflow実行をappend-only turnとVersion/CASで調停します。未回答質問をblockし、Workflow完全Resultはproject Ledgerへ残してSessionにはdigestとbounded typed summaryだけを保存します。
- ADR-0030の任意External Action handoffはcompleted Workflow内の明示Taskだけを既存WordPress Action child Commandへ渡し、source／plan digest承認後の結果summaryをSessionへ記録します。公開意図や対象を推測しません。
- `interaction-next`はSession stateと最新turnから次のoperation、必要field、質問、承認要否、Recovery参照をread-onlyに導出します。自動承認・実行・Recoveryは行いません。
- ADR-0031のmobile-first Local Web UIは`workcairn-daemon`へembedされ、iPhoneからInteraction／Next Action／Command APIを利用します。既定loopbackを維持し、明示`--mobile`だけprivate／link-local addressとprocess-local pairingを許可します。UIはbusiness ruleを持たず、Task／Deliverable／canonical Review evidenceをread-onlyで後から表示します。
- ADR-0032によりmobile Interaction commandはtyped validation後にbounded受理でき、client接続から切り離して既存workspace Ledgerを追跡します。同期API、CLI、commit pointは変更せず、daemon crash後の自動resumeは行いません。
- Workspace Kernel、Project／Workflow／Task／Event／Worker／Policy Domain、PromptBuilder、Claude Adapter、Vault Adapter、Runtime compositionはGoです。temporary VaultとMock ProviderでEnd-to-End検証します。
- ADR-0033により移行用Python package、tests、entry point、SDK、build metadataはPublic Beta前に削除済みです。製品surfaceは`workcairn`、`workcairn-daemon`、JSON Contract v1の`workcairn-core`です。
- ADR-0034により正式製品名、binary、archive、Go module、固有環境変数をWorkCairnへ統一しました。Local Web UIはiPhone既定のMy ActionsとPC／iPad既定のCompany Viewを持ち、既存Next Action／Organization／evidenceだけを投影します。
- RevisionはADR-0012のimmutable intent、TaskService.Create、`revision.created`、Auditをtemporary VaultでEnd-to-End検証済みです。
- Go Review PromptBuilder、構造化結果parser、ReviewService、ADR-0010 Vault Review Store、`workcairn review-*`は実装済みです。
- ADR-0011 Review orchestrationがcanonical JSON commit後だけ`review.completed`を発行し、Vault Audit subscriberが保存します。projection／Event失敗はartifactを保持したpartial failureです。
- Organization／Identity inventory、構造検証、氏名policy、採用、改名、ID repair、Workspace State同期はGo Domain／Vault Adapterへ移行済みです。
- ADR-0013に基づくProject directory単位bootstrapと、TaskService／Task Event／Auditを通る通常Task作成を`workcairn project-bootstrap-*|task-create-*`で利用できます。
- ADR-0014に基づく単一Employee採用はEmployee Markdownをcanonical commit後、Workspace Stateをprojectionし、partial failureを保持します。batch候補はGoで全件検査後、1社員ずつGo writerへ委譲します。
- ADR-0015/0017に基づくEmployee renameはbatch全件をread-only preflight後、単一renameのimmutable intent、filename Identity commit、検証済みprojectionを順次実行します。historical recordと自由文章を変更せず、partial failureを明示します。
- ADR-0016に基づく重複Employee ID repairは確認済みplanの再検証、immutable intent、Employee Markdown canonical commit、明確に特定できるprojectionの順で実装済みです。Task assigneeや自由文章は推測更新しません。
- ADR-0018/0019に基づき、CEOの自然言語依頼はGoのProvider-neutral CEO Plan Service、Claude Runner Adapter、strict typed validationを通り、別承認のapplyでProject、TaskService.Create、immutable Task Dependencies projectionへ渡ります。
- Kernelの標準ライフサイクル順は`Event → Task → Worker → Execution`、停止時は逆順です。
- JSON Contract v1は`workcairn-core`が公開する安定した外部process boundaryです。対応operationは`project.next_task_id`、`project.validate_task`、`project.can_transition`、`workflow.readiness`です。
- 共有fixtureは`fixtures/go_core/`、`fixtures/project/`、`fixtures/workflow/`にあります。
- 現在の全体像は`docs/SystemOverview.md`、詳細は`docs/Architecture.md`、次期優先順位は`docs/ROADMAP.md`を正とします。
- 移行完了と削除資産、残したlanguage-neutral fixtureは`docs/MigrationHistory.md`に記録しています。
- `make v1-release-gate`がPublic Beta／v1候補の正式な単一Release Gateです。
- Public Beta候補は`v1.0.0-beta.1`です。macOS／arm64をTier 1とし、他のmacOS／Linux targetはnative smoke前のcandidate、WindowsはVault file lock未対応です。
- `make public-beta-smoke`はtemporary VaultとMock ProviderでTask、Review／Revision、mobile Interaction完了を検証します。

## Architecture at a Glance

```text
Runtime / CLI / Adapter
        ↓
Workspace Kernel
    ├── ProjectService   → Project Domain
    ├── WorkflowService  → Workflow Domain
    ├── ExecutionService → ApprovalPolicy / ExecutionPolicy
    ├── TaskService      → Task Domain / TaskStore / EventService
    ├── WorkerService    → PromptBuilder / RunnerRegistry / Runner
    └── EventService     → in-process Event Bus
```

Go CoreはObsidian、言語runtime、LLM SDK、`.env`、APIキーへ依存しません。Provider設定、Vault I/O、CLI入力などはRuntime／Adapterの責務です。

## Repository Map

- `go/internal/*`: Go Domain、Service、Kernel、Adapter境界、Interaction、Scheduler、Notification／Metrics、External Action、通常Task PromptBuilder、Claude Runner／WordPress Adapter、Vault Context／TaskStore／Deliverable／Audit Adapter、Go Runtime composition。新しい中核ルールの実装先
- `go/cmd/workcairn-core`: JSON Contract v1を公開するCLI
- `go/cmd/workcairn`: Organization参照、CEO plan生成／適用、Project／Task作成、Task metadata migration、通常Task／Review／Revision／Reviewed Workflow、明示Recoveryを提供するGo運用CLI
- `go/cmd/workcairn-daemon`: 必須Command IDの同期default HTTP v1、bounded mobile Interaction acceptance、mobile-first Web UI、graceful shutdownを提供するloopback既定Go daemon
- `go/internal/httpapi`: HTTP contract／handler、embed Web UI、trusted-LAN pairingと、既存Go process compositionへのAdapter
- `docs/Recovery.md`: partial state inventory、診断certainty、安全な明示Recovery操作
- `fixtures/`: Go testsが直接検証するlanguage-neutralな契約データ
- `docs/adr/`: Acceptedな設計判断
- `docs/CONSTITUTION.md`: 変更時に守る不変条件
- `docs/SystemOverview.md`: 現在の利用フローと保証
- `docs/ROADMAP.md`: v1.0安定化とGo Only後の優先順位

## Standard Validation

```bash
make v1-release-gate
```

ドキュメントだけの変更では、リンク、差分、機密ファイル非変更を確認し、必要性に応じてテスト範囲を調整します。

## Safety Defaults

- 実Vault、`.env`、APIキー、社員・実Projectデータは、依頼に明記がない限り変更しません。
- 実LLM APIは明示的な許可なしに呼びません。
- 状態変更、外部公開、push、採用、タスク実行には明示的な承認を要求します。
- 既存の未コミット変更を消去・上書きしません。
- 外部runtime fallbackとCoreへのProvider／Vault依存追加は禁止です。

## Definition of Done

1. 変更前の状態と関連ADRを確認した。
2. Domain、Service、Kernel、Adapterの境界を維持した。
3. 失敗、partial failure、並行実行、context cancellationを必要に応じて検証した。
4. Go、fixture、JSON Contractの互換性を変更リスクに応じて確認した。
5. 実Vault、秘密情報、無関係なファイルに差分がない。
6. `git diff --check`と`git status`で最終差分を確認した。
