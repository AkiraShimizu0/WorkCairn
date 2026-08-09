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

## Completed — Notification and Metrics Foundation

ADR-0026に基づき、既存Event／Audit ownershipを維持した観測subscriberを実装しました。

実装済み：

- Task lifecycle、Review、Revision Runtimeへのordered observer injection
- Event payload／metadataを保存しない`workspace-notification.v1` immutable local Inbox
- atomic create、Event ID hash filename、破損／unexpected entry／不整合の安全な拒否
- Event type別件数と最終観測時刻だけを持つbounded process-local Metrics
- daemonのread-only Notification／Metrics HTTP inspection
- Command replayでEvent／Notification／Metricsを重複させないE2E
- subscriber失敗後もcanonical factをrollbackしないpartial publication test

外部channel配送、未読／ack、永続Metrics、token／duration、Event replay、Outboxは含めません。

## Completed — External Action Adapter Foundation

ADR-0027に基づき、WordPress公開をProvider Runnerとは別のAction Adapterとして実装しました。

実装済み：

- 既存immutable Deliverableをsource reference／SHA-256へ拘束するread-only plan
- 明示承認とproject-scoped Command Ledger claim
- payload本文を複製しないimmutable request evidence
- Runtime edgeからcredentialを注入するSDK-free WordPress REST Adapter
- remote公開後のimmutable result evidenceと`action.completed` Event／Audit／Notification
- Provider失敗、result保存失敗、Event失敗のtyped partial state
- CLI、HTTP、one-shot Schedulerで共有する`action.wordpress.publish` contract
- terminal replay非重複、異request conflict、承認前副作用ゼロのMock／temporary Vault E2E

HTML変換、update／delete、media upload、external reconciliation、自動retry、複数Action Providerは含めません。

## Completed — Public Release Preparation

Go Onlyの閉ループが自然言語依頼から外部公開まで成立したため、新機能追加より配布・運用理解を優先します。

実装済み：

- 初回setup、temporary／approved Vault、Provider／Action credential注入の安全な導線
- loopback daemon、Scheduler、Notification、Recoveryを一貫して扱うoperator guide
- binary packaging、version metadata、upgrade／backup／compatibility checklist
- public exposure前のauthentication／TLS／authorization方針
- 現在の機能を伝える製品名候補と既存Workspace OS名称との移行判断

`OperatorGuide.md`はtemporary Vaultからapproved Vaultへの導線、backup、plan／approval／execute、daemon、Scheduler、Notification、Recovery、WordPress partial failure、upgradeを一貫して説明します。release packageはversion／commit metadataを持つ3つのGo binary、必要docs、LICENSEだけをarchiveし、SHA-256 checksumを生成します。daemonはremote公開の注意書きだけでなく非loopback bindをcodeで拒否します。

完了条件を満たしました。新規利用者は実Vaultを誤変更せずplan／approval／execute／inspect／recoveryを再現でき、remote公開、automatic retry、WordPress変換機能の未実装保証を確認できます。製品名は互換性を優先して当面`Workspace OS`を推奨し、名称変更そのものは商標・市場判断後の独立migrationとします。

## Completed — Interaction and Approval Session Foundation

現在のCLI／HTTPは自然言語依頼から実行までの閉ループを持ちますが、必要な質問、plan提示、承認待ち、partial failure後の次の安全な選択を1つの継続sessionとして表現していません。

ADR-0028に基づく実装済みfoundation：

- natural-language request、plan generation approval required、clarification required、plan approval required、ready to executeのclosed typed state。running／recovery requiredはCommand Ledgerで分離
- immutable requestと承認対象digestに拘束されたsession evidence
- CEO plan生成と既存Project／Task writer適用を既存Commandへ変換する薄いorchestration
- CLIとloopback APIが同じsession Serviceを利用するread／respond境界
- credential、Vault root、Provider生responseをsession contractへ入れないredaction

最初はsingle-user、single active step、手動応答だけを扱います。chat UI、remote exposure、parallel workflow、automatic approval、automatic resume、recurring Schedulerは先取りしません。

自然言語requestはimmutable digest、Provider plan／質問回答／Project適用はappend-only turnとしてVersion/CAS保存します。未回答質問をblockし、回答後の再planで質問ゼロになった最新plan SHA-256だけを別承認で適用します。CLIとloopback APIは同じDomain／Service／Processを使い、全writerはworkspace Command Ledgerを通ります。

## Completed — Interaction Workflow Execution Composition

`ready_to_execute` Sessionから既存Reviewed Workflowを開始し、Accept／Request Changes／Revision／再Reviewの結果をSessionへ記録します。

ADR-0029に基づく実装：

- reviewer ID、最大Task数、approval referenceを含むread-only execution plan
- approved Session VersionとProject identityに拘束したouter Interaction Command
- 既存`ExecuteReviewedWorkflow`を決定的project child Commandとして実行し、完全Result digestとbounded typed summaryをappend-only turnへ保存
- blocked／limit／partial resultをSessionから観測し、自動resumeしない
- Workflow完了後だけExternal Action planへ進めるclosed next action

Reviewed Workflow、TaskService、Review／Revision、child Command IDを再実装しません。External Actionの自動承認、remote reconciliation、parallel Sessionは含めません。

`completed`だけをSession終端とし、blocked／limitは新しいplanと明示承認で継続できます。partial／failedは`workflow_attention_required`で停止し、Sessionから自動resumeしません。Workflow成功後のSession CAS失敗は成立済みTask／Review／Revision／Deliverableを保持したouter partial failureです。

## Completed — Interaction Completion and External Action Handoff

Workflow完了後、ユーザーへ「完了」または既存Deliverableに対するExternal Action候補をread-onlyで提示し、必要な場合だけ別のsource digest承認へ進めます。

- External Actionを自然言語から推測して自動実行しない
- 既存`action-wordpress-plan|publish`の承認、immutable evidence、partial publication semanticsを再利用する
- Actionを要求しないSessionは`completed`のまま終える
- Action intentをCEO planへ追加する場合も既存plan contractを破壊せずadditiveにする

最初は明示targetとTask／Deliverable identityを受けるhandoffだけを候補とし、automatic approval、content変換、remote reconciliation、複数Action、汎用chat UIは含めません。

ADR-0030に基づき、completed Workflow evidence内の明示Taskだけを、prospective outer Command IDから導出した既存WordPress Action child Commandへ渡します。read-only planはsource／Action digestを固定し、成功は`action_completed`、失敗／partialは`action_attention_required`としてbounded evidenceをSessionへappendします。

## Completed — Interaction Next-action Read Model

現在のSession stateを利用者が独自解釈せず、次に必要な質問、plan、承認、Recovery確認、完了を1つのread-only projectionとして取得できるようにします。

- stateと最新turnだけから決定的に導出し、Provider／Vault writerを呼ばない
- operation、expected Version、必要identity、承認要否、attention時のLedger参照をtypedに返す
- credential、Prompt、Deliverable本文を含めない
- 自動承認、自動実行、自動Recoveryへ変換しない

これはchat UIやagent loopではなく、CLI／loopback clientが「必要な質問・承認だけ」を表示するための薄いread modelです。

`interaction-next`とHTTP next endpointは、closed Session stateと最新turnだけからoperation、expected Version、必要field、質問、承認要否、attention時のouter／child Ledger参照を返します。Provider、writer、Recoveryを呼ばず、credential、Prompt、Deliverable本文を含みません。

## Completed — Guided Local Interaction Client

ADR-0031に基づき、既存Interaction plan／command／next endpointを使い、利用者が個別operation名を組み立てずに自然言語依頼、質問回答、digest確認、明示承認を順に進められるmobile-first Local Web UIを実装しました。

- Domain／Processを再実装せず、既存HTTP APIの同一origin clientに限定する
- approval promptで対象digest、Project、reviewer、Task上限、外部Actionを明示する
- attention stateでは自動継続せずLedger／Recovery案内へ止める
- credentialをclient historyやSessionへ保存しない

UIはGo binaryへembedし、iPhone 390×844相当で`依頼→質問→Plan承認→Workflow→完了→成果物／Review詳細`をtemporary VaultとMock Providerで確認済みです。既定loopbackは維持し、明示mobile modeだけprivate／link-local IPとprocess-local pairingを許可します。remote authentication、TLS、native app、Push通知は含めません。

## Completed — Mobile Command Continuity

従来のeffect Commandは同期HTTP requestでした。iPhoneがlock／backgroundへ移りconnectionが切れた場合、request context cancellationとCommand Ledgerの確定状態を確認する必要がありました。新しいbusiness ruleや自動resumeを追加せず「承認をdaemonが受理した後はclient接続と切り離して実行し、同じCommand IDをread-only statusで追跡する」最小境界を採用します。

- 受理前は既存と同じ明示承認、strict payload、Command IDを要求する
- daemon process内のbounded executionだけを扱い、crash後の自動resumeはしない
- terminal／partial／runningは既存Command LedgerとRecoveryを正とする
- UIはpollingでstatusを表示するだけで、automatic retry／adoptionをしない
- CLIと同期HTTP operationを破壊せずadditiveにする

ADR-0032に基づき、`interaction.*`だけが`Prefer: respond-async`でboundedに受理され、既存workspace Command Ledgerをstatus URLとして返すようになりました。UIは同じCommand IDをread-only pollingし、reload後も再実行せずstatus確認だけを再開します。graceful shutdownは受理済みcommandを待ち、猶予切れではcancelしてRecoveryへ止めます。

## Next 1 — Guided Recovery Inspection

mobile attention表示は現在outer／child Command IDとLedger stateまでです。次は既存ADR-0020のRecovery snapshot／finding／planをread-only HTTP projectionとして公開し、iPhoneでも「何がcommit済みで、なぜ自動継続しないか」を理解できるようにします。

- 最初は診断だけとし、自動repair／retry／artifact adoptionを追加しない
- canonical evidence certaintyと既存Recovery error型をそのまま表示する
- Recovery applyを追加する場合は別の明示digest／Version承認に分離する
- Vault path、秘密情報、Prompt、Provider responseをclientへ出さない
- normal Workflowのbusiness ruleやSession stateを変更しない

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
