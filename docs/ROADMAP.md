# Workspace OS Roadmap

## North Star

Workspace OSは、Workspace Kernelを中心とするGo Only製品Runtimeです。Project、Organization、Workflow、Task、Event、Worker、Policy、Review、Revision、Deliverable、Auditの中核ルールと通常運用はGoを正本とします。

ロードマップは現在地、次の順序、完了条件を示します。不変条件は[CONSTITUTION.md](CONSTITUTION.md)、現在構造は[SystemOverview.md](SystemOverview.md)と[Architecture.md](Architecture.md)、確定した設計判断は[ADR](adr/)を参照してください。

## Completed — Python Foundation and Go Migration

### v0.1 Python Foundation

Obsidian Vaultを利用した社員、組織、Project、Task、Deliverable、Review、Revisionの初期製品を構築しました。この実装は現在、公開API互換とmigration referenceです。新しい中核ルールは追加しません。

### v0.2 Go Core Foundation

- Project／Workflowの純粋ルールをGoへ移植
- Python／Go間のJSON Contract v1と共有fixture
- Workspace Kernel、Service lifecycle、typed Event、Task Domain、Version/CAS
- Worker／RunnerのProvider-neutral境界、Approval／Execution Policy

### v0.3 Go Only Runtime

- 通常Task Prompt、Claude Runner Adapter、Runtime composition
- Vault Context、TaskStore、managed metadata migration
- immutable Deliverable、Audit Event subscriber、partial failure semantics
- Review canonical JSON／Markdown／Event orchestration
- Revision intent／Task／Event orchestration
- Project bootstrap、Task creation、Task Dependencies
- Organization inventory、Identity validation、採用、改名、ID repair、同期
- CEO自然言語依頼からtyped plan生成、検証、承認、Go writer適用
- `workspace-run`への通常製品cutoverとPython legacy fallback除去
- Python compatibility manifest、Provider依存隔離、削除条件inventory
- Python interpreterを使用しない`go-only-release-gate`

Go Onlyの詳細なcapability判定は[GoOnlyReleaseGate.md](GoOnlyReleaseGate.md)を参照してください。

## Completed — v1.0 Candidate Stabilization

このフェーズでは新機能より、既存保証の固定、ドキュメント、配布境界、回帰検知を優先します。

実装済みRelease Gate：

- Go binary build、全Go test、race test、vet
- 全Go sourceの`os/exec`／`os.StartProcess`禁止
- Go-only capabilityと製品operationの対応確認
- Domain／Service／KernelからAdapter／Runtime／Processへの逆向き依存禁止
- Task lifecycle Event生成をTaskServiceへ限定
- 全書込みCLIの承認前Vault／Provider I/O禁止
- JSON Contract v1と共有fixtureの互換test
- Python compatibility import、legacy marker、Provider依存allow-list
- lockfile整合、Python compile、差分whitespace検査を含む`v1-release-gate`

v1.0候補の完了条件：

1. `make v1-release-gate`が実Vault、`.env`、実Providerなしで成功する。
2. 通常運用、CEO plan、Project／Task、Organization／Identity、Execution、Review、Revision、Deliverable／AuditがPython interpreterなしで実行可能である。
3. 承認前副作用ゼロ、Task／Event ownership、partial failure、Vault／Provider neutralityを自動testで固定する。
4. JSON Contract v1、既存Vault表示、公開Python v0.1 import surfaceを破壊しない。
5. Python compatibility distributionが製品artifact／通常運用手順から分離され、物理削除条件が明文化されている。

v1.0候補安定化の完了条件として先取りしなかったもの：永続Outbox、Command Ledger、Scheduler、distributed execution、汎用workflow engine、常駐daemon。その後の次期RoadmapでRecovery、Command Ledger、loopback daemon、bounded Multi-task Workflow、one-shot Schedulerを段階的に実装済みですが、これらはv1.0判定自体の前提へ遡及追加しません。永続Outbox、cron／recurring Scheduler、distributed execution、汎用workflow engineは引き続き未実装です。

## Completed — Durability and Recovery Foundation

ADR-0020に基づき、新機能を増やす前に現在のpartial failureを診断し、安全性を証明できる操作だけを明示回復できる基盤を実装しました。

実装済み：

- commit済みTask／artifact、期待Audit欠落、Review／Revision partial stateのread-only inventory
- process crash時に残り得るstaging／temporary stateの診断
- evidence SHA-256、source revision、Task expected Version／CASに拘束されたversion付きplan
- `recovery-inspect`、`recovery-plan`、明示承認付き`recovery-apply`
- Deliverable commit後のTask Completeと、Deliverableなし中断TaskのFail／Hold
- Event／Audit欠落を`unverifiable`として分離し、replayしない診断model

成立済みfactを推測・上書きせず、人間が状態を診断し、安全な回復操作を選べます。Review projection再構成、Revision intent adoption、Event replay、temporary削除は証拠不足のため診断だけに留めます。詳細は[Recovery.md](Recovery.md)を参照してください。

## Completed — Idempotency and Command Ledger Foundation

Recoveryの観測モデルを固めた後、外部retryと二重commandを安全に扱います。

実装済みfoundation：

- ADR-0021のstorage-neutral Command ID／request digest／running／terminal outcome
- Vault hidden metadataへのatomic claim、Version/CAS付きterminal update
- 明示`--command-id`付き通常Task／Review／Revision、CEO apply、Project／Task writer、Organization writerの同一result replay、異payload conflict、running拒否
- outcome commit failureをrollbackしないpartial failureとRecovery inventory
- Project作成前／Organization command用workspace scopeと、既存Project内command用project scope

各commandの既存commit orderingは変更せず、外側Ledgerだけを共通化しました。`running`の自動resume、artifact adoption、Event replay、Transactional Outboxは導入していません。

今後HTTP／daemon境界で扱う事項：

- Command ID／Idempotency Keyの永続契約
- request digest、実行段階、結果を持つCommand Ledger
- 同一command再送、異なるpayloadでのID再利用、既存artifactとの関係
- Task CAS、artifact immutability、Event publicationとの責務分離
- 必要性を検証した後のTransactional Outbox

完了条件を満たしました。同一commandの再送は二重副作用を起こさず、曖昧な既存artifactを自動adoptしません。Outboxは実際のEvent配送要件を確認した後にのみ導入します。

## Completed — HTTP API and Daemon Foundation

耐久性とidempotencyを前提に、CLI以外の製品入口を追加します。

実装済み：

- Kernel／Service／process compositionを再利用する`workspace-command.v1`
- HTTPでのCommand ID必須化、同一result replay、conflict／running拒否
- synchronous long-running command、client cancellation、graceful shutdown
- health／readiness、workspace／project Ledgerのread-only status
- loopback既定、request size、Provider timeout、Vault／secret非入力化

CLIとAPIはビジネスルールを二重実装せず、client retryとprocess再起動後のstatus確認を同じLedgerへ接続します。認証、TLS、authorization、remote exposure、非同期queueはpublic deployment前の別フェーズです。

## Completed — Sequential Multi-task Workflow Foundation

単一Taskの保証を保ったまま、依存グラフ上の複数Taskを調停します。

実装済み：

- dependency readiness済みTaskの選択と順次実行
- outer workflow claimと決定的child Task Command ID
- 各Task後の再plan、blocked／limit／partial result
- Task lifecycleの所有権をTaskServiceに維持

workflow orchestrationはTask状態を直接変更せず、各Task executionを既存Service境界で実行します。途中停止は自動resumeせずouter／child LedgerとRecoveryから判断します。

## Completed — Review and Revision Workflow Branches

ADR-0024に基づき実装済み：

- reviewer IDと条件付きRevisionを明示するread-only plan
- 既存Task execution、Review、Revision processのcomposition
- Approveならdependency再plan、Request ChangesならRevision Task実行後に再Review
- outer claimと役割付き決定的child Command ID
- Revision Taskに限定したtargeted readiness
- canonical evidenceを保持するblocked／limit／partial result

Review／Revision orchestration、Task lifecycle、artifact orderingを再実装せず、temporary VaultとMock Providerで`Task → Request Changes → Revision → Approve → 次Task`をEnd-to-End検証します。自動resume、並列実行、Schedulerは含めません。

## Completed — Scheduler and Automation Foundation

ADR-0025に基づき、durable commandとworkflow runを変更せず時間駆動入口を追加しました。

実装済み：

- storage-neutral Scheduler Domain／ServiceとVault Schedule Store
- exact typed `workspace-command.v1`を保存するread-only plan／明示承認create
- pending → dispatching → terminalのVersion／CASとatomic replacement
- missed one-shotの次tick配送、重複trigger拒否、offset付き絶対時刻
- target Command Ledger、Kernel lifecycle、daemon graceful shutdownの再利用
- `schedule-plan|create|list`、HTTP `schedule.create`／read-only inspection

SchedulerはTask状態やProviderを直接操作せず、temporary Vault E2Eで既存writerへの一回配送とLedger terminal resultを検証します。cron／recurrence、並列配送、`dispatching`自動resume、target result adoptionは含めません。

## Next 1 — Notification and Metrics

Event subscriberとして観測性を追加します。

候補：

- execution／review／revision／recoveryのMetrics subscriber
- notification Adapterと配信失敗の分離
- token、duration、failure stage、partial stateの集計
- secret／Prompt本文／個人情報を漏らさないredaction policy

完了条件：通知やMetricsの失敗がbusiness factをrollbackせず、Event／Auditの責務を侵食しないこと。

## Next 2 — External Action Adapters

WordPress等への公開は、Provider Runnerとは別の明示的Action Adapterとして追加します。

候補：

- typed Action request／result、dry-run、approval
- credentialをRuntime edgeから注入
- publish前後のimmutable evidenceとpartial failure
- WordPressを最初の具体Adapter候補とする

完了条件：外部ActionがKernel／DomainへSDKや秘密情報を持ち込まず、Task状態、Deliverable、Auditを直接変更しないこと。

## Python Compatibility End of Life

Pythonの物理削除は新機能ではなく、公開互換方針を終了できる時点のrelease作業です。外部callerのGo CLI／将来API移行、reference fixtureのGo化、console script廃止方針を満たした後に実施します。詳細な対象、順序、削除時検証は[PythonRuntimeInventory.md](PythonRuntimeInventory.md)を正とします。

## Cross-Cutting Gates

すべてのフェーズで次を維持します。

- Go CoreへProvider／Storage／`.env`依存を持ち込まない
- JSON Contract v1と共有fixtureの後方互換性
- 明示承認前の副作用ゼロ
- TaskServiceだけがTask状態とTask lifecycle Eventを変更する
- partial failure、timeout、cancellation、commit済み状態の観測可能性
- Fake、Mock、temporary directoryだけを使う検証
- 実Vault、秘密情報、実Provider、ユーザーデータの保護
- 重大判断をADRへ記録し、Constitution／Architecture／Roadmapを同期する
