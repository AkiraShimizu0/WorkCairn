# WorkCairn Roadmap

## North Star

WorkCairnは、Workspace Kernelを中心とするGo Only製品Runtimeです。Project、Organization、Workflow、Task、Event、Worker、Policy、Review、Revision、Deliverable、Auditの中核ルールと通常運用はGoを正本とします。

次の主要な方向性は、**人間の少ない指示から、多くの有用な成果を、安全・有限・追跡可能な形で生み出すこと**（Leverage Engine）です。1回のCEO依頼を独立した複数Taskへ安全に分解し、依存関係のないTaskを並列実行し、最後に結果を統合する — これは新しいengineを追加するのではなく、既存のCEO Plan decomposition、Task Dependency、Reviewed Workflow、Autonomy Contract、Command Ledgerを拡張して実現します（[ADR-0051](adr/ADR-0051-leverage-engine-parallel-decomposition-foundation.md)）。

ロードマップは現在地、次の順序、完了条件を示します。不変条件は[CONSTITUTION.md](CONSTITUTION.md)、現在構造は[SystemOverview.md](SystemOverview.md)と[Architecture.md](Architecture.md)、確定した設計判断は[ADR](adr/)を参照してください。

## Completed — Foundation and Go Migration

### v0.1 Initial Foundation

Obsidian Vaultを利用した社員、組織、Project、Task、Deliverable、Review、Revisionの初期製品を構築しました。移行元実装はPublic Beta前に撤去し、現在の正本はGoです。

### v0.2 Go Core Foundation

- Project／Workflowの純粋ルールをGoへ移植
- JSON Contract v1とlanguage-neutral fixture
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
- `workcairn`への通常製品cutoverとlegacy fallback除去
- Provider依存をRuntime／Adapterへ隔離
- Go toolchainだけで成立する`v1-release-gate`

Go Onlyの詳細なcapability判定は[GoOnlyReleaseGate.md](GoOnlyReleaseGate.md)を参照してください。

## Completed — Public Beta Product Path Consolidation

ADR-0042に基づき、一般利用者の正式経路を`First Run → Interaction → Intent／Canonical Plan → Approval → Reviewed Workflow → Completion → Timeline／Proof of Work`へ一本化しました。

- 一般daemonのside-effect operationを`workspace.setup`、`interaction.start`、`interaction.plan.generate`、`interaction.answer`、`interaction.plan.apply`、`interaction.workflow.execute`へexact allow-list
- direct Task／Review／Revision、plain Workflow、direct Reviewed Workflow、writer、Scheduler、External Actionを一般HTTP／UIからdefault deny
- CLI、Recovery、内部Process、Command Ledger、canonical evidenceは維持
- WordPress、Scheduler、Notification／Metrics、advanced Autonomy／Shadow ModeはBeta後surfaceとして通常UIから非表示

このproduct path固定をactual daemonとbrowserまで検証する次Phaseは、下記Browser Acceptance Gateとして完了しました。

## Completed — Public Beta Browser Acceptance Gate

ADR-0043に基づき、test-only Playwrightでactual daemonとbrowserを通す独立Gateを追加しました。

- Chromium desktopとWebKit iPhone viewport
- actual `workcairn-daemon`、temporary Vault、空きport、graceful shutdown／restart
- parserから生成しないsanitized固定Anthropic互換fixture
- pairing、First Run、clarification draft／focus、single-flight、Request Changes、Revision、再Review、Completion
- reload、daemon restart、Timeline／Proof of Work、FailureEnvelopeのfresh browser再投影
- `make v1-release-gate`と分離した`make public-beta-browser-gate`

実Mac Safari／iPhone Safari、private-LAN secure-context、実ProviderはPublic Beta human Device Acceptanceに残ります。

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
- 撤去済みruntime assetの再混入防止
- `gofmt`、release script、差分whitespace検査を含む`v1-release-gate`

v1.0候補の完了条件：

1. `make v1-release-gate`が実Vault、`.env`、実Providerなしで成功する。
2. 通常運用、CEO plan、Project／Task、Organization／Identity、Execution、Review、Revision、Deliverable／AuditがGoだけで実行可能である。
3. 承認前副作用ゼロ、Task／Event ownership、partial failure、Vault／Provider neutralityを自動testで固定する。
4. JSON Contract v1と既存Vault表示を破壊しない。
5. repository、build、test、release、distributionがGo toolchainだけで成立する。

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

## Completed — Release Engineering Foundation

Go Onlyの閉ループが自然言語依頼から外部公開まで成立したため、新機能追加より配布・運用理解を優先します。

実装済み：

- 初回setup、temporary／approved Vault、Provider／Action credential注入の安全な導線
- loopback daemon、Scheduler、Notification、Recoveryを一貫して扱うoperator guide
- binary packaging、version metadata、upgrade／backup／compatibility checklist
- public exposure前のauthentication／TLS／authorization方針
- 現在の機能を伝える名称候補と旧製品名からの移行判断

`OperatorGuide.md`はtemporary Vaultからapproved Vaultへの導線、backup、plan／approval／execute、daemon、Scheduler、Notification、Recovery、WordPress partial failure、upgradeを一貫して説明します。release packageはversion／commit metadataを持つ3つのGo binary、必要docs、LICENSEだけをarchiveし、SHA-256 checksumを生成します。daemonはremote公開の注意書きだけでなく非loopback bindをcodeで拒否します。

基礎条件を満たしました。新規利用者は実Vaultを誤変更せずplan／approval／execute／inspect／recoveryを再現でき、remote公開、automatic retry、WordPress変換機能の未実装保証を確認できます。名称の採用とnative platform smokeはPublic Beta Preparationで扱います。

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

UIはGo binaryへembedし、iPhone 390×844相当で`依頼→質問→Plan承認→Workflow→完了→成果物／Review詳細`をtemporary VaultとMock Providerで確認済みです。既定loopbackは維持し、明示`--local-network`（旧`--mobile`、ADR-0069）だけprivate／link-local IPとprocess-local pairingを許可します。remote authentication、TLS、native app、Push通知は含めません。

## Completed — Mobile Command Continuity

従来のeffect Commandは同期HTTP requestでした。iPhoneがlock／backgroundへ移りconnectionが切れた場合、request context cancellationとCommand Ledgerの確定状態を確認する必要がありました。新しいbusiness ruleや自動resumeを追加せず「承認をdaemonが受理した後はclient接続と切り離して実行し、同じCommand IDをread-only statusで追跡する」最小境界を採用します。

- 受理前は既存と同じ明示承認、strict payload、Command IDを要求する
- daemon process内のbounded executionだけを扱い、crash後の自動resumeはしない
- terminal／partial／runningは既存Command LedgerとRecoveryを正とする
- UIはpollingでstatusを表示するだけで、automatic retry／adoptionをしない
- CLIと同期HTTP operationを破壊せずadditiveにする

ADR-0032に基づき、`interaction.*`だけが`Prefer: respond-async`でboundedに受理され、既存workspace Command Ledgerをstatus URLとして返すようになりました。UIは同じCommand IDをread-only pollingし、reload後も再実行せずstatus確認だけを再開します。graceful shutdownは受理済みcommandを待ち、猶予切れではcancelしてRecoveryへ止めます。

## Next 1 — Guided Recovery Inspection

Local Web UIのattention表示は現在outer／child Command IDとLedger stateまでです。次は既存ADR-0020のRecovery snapshot／finding／planをread-only HTTP projectionとして公開し、Web UIからも「何がcommit済みで、なぜ自動継続しないか」を理解できるようにします。

- 最初は診断だけとし、自動repair／retry／artifact adoptionを追加しない
- canonical evidence certaintyと既存Recovery error型をそのまま表示する
- Recovery applyを追加する場合は別の明示digest／Version承認に分離する
- Vault path、秘密情報、Prompt、Provider responseをclientへ出さない
- normal Workflowのbusiness ruleやSession stateを変更しない

## Completed — Leverage Engine Foundation and Production Wiring

ADR-0051（Accepted）により、CEOの1回の依頼から複数Taskが安全に自動並列実行され、Synthesisで統合されるところまでを、Public Betaの実際のCommand経路（`interaction.plan.approve_and_execute`）へ配線しました。

- `go/internal/workflow.EvaluateAllReadiness`: 依存関係を満たした全Readyタスクを返す純粋関数（既存`EvaluateReadiness`の兄弟、既存`ValidateDependencies`を再利用）
- `ReviewedWorkflowRunService.RunParallel`: bounded goroutine poolによる並列dispatch（束＝1ラウンド単位で終端を待って次ラウンドへ）。唯一のoperation`workflow.reviewed.execute`（`process.ExecuteReviewedWorkflow`）が常にこれを駆動し、readyなTaskが1件なら逐次相当、複数件ならGoが自動的に並列実行する。**parallel／sequential／concurrencyを選ぶ経路はどこにも存在しない**。新operationは追加していない（前段Checkpointで追加した`workflow.reviewed.execute.parallel`は削除し、既存operationへ統合した）
- `ceoplan.IntentStep.ParallelWithPrevious`: LLMが供給する唯一のfan-out/fan-in signal（「直前のstepと同時に着手できるか」の1 boolean）。Go（`NormalizeIntent`）がこの signalの列から依存グラフを構造的に構築する（1層fan-out＋1層fan-in）。LLMは依存関係IDやグラフ構造を一切出力しない
- Autonomy Contract（`workcairn-autonomy.v1`）への`MaxParallelTasks`（既定3）／`MaxRevisionCount`（既定2、Revision Guard、branch独立counting）追加。`ceoplan.MaxGeneratedTasks`（既定5、Plan生成側のLoopGuard、Autonomy Contractとは別のライフサイクルとして意図的に分離）
- `event.Event.CorrelationID`は`interaction.plan.approve_and_execute`の外側Command IDを一貫してroot、`CausationID`は各段階の子Command ID
- Synthesis（複数branch統合Task）は新Serviceを持たず、依存関係を持つ通常のTaskとして、実Command経路・実HTTPのEnd-to-EndテストとBrowser Gateで動作確認済み

対象外のまま（将来の別ADR候補）：再帰分解（Taskが自分の子Taskを動的生成すること）、Specialist Routingの実装、No Progress Detectorの実装、Budget Guard（`MaxTokens`/`MaxCost`/`MaxRuntime`/`MaxToolCalls`）の実測・強制、`MaxChildTasksPerTask`/`MaxTaskDepth`、DEGRADE Policy。詳細は[ADR-0051](adr/ADR-0051-leverage-engine-parallel-decomposition-foundation.md)を参照してください。

## Completed — Revision Limit Recovery and No-Progress Foundation

[ADR-0052](adr/ADR-0052-revision-limit-recovery-and-no-progress-foundation.md)（Accepted）により、並列実行がRevision Guardの上限に達して停止したあと、CEOが少ない操作で安全に結果を救い、必要な部分だけ続けられる経路を実装しました。成功条件は「上限に達したこと」ではなく「そこからの回復操作の少なさ」です。

- `interaction.workflow.recover_revision`: Revision Limit Recovery専用の新しいadditive Command。既存の`revision.execute`と`runInteractionWorkflowChain`（ADR-0049）を内部で再利用するだけで、新しいWorkflow再開ロジックは持たない
- `interaction.Turn.RecoveryTaskID`/`RecoveryGuidance`（新Turn Kind `revision_recovery_started`）: 新しい永続IDを追加せず、既存のTask ID文字列とCEOの追加指示だけでlineageを表現
- `revision.Intent.AdditionalGuidance`: CEOの追加指示を、既存の唯一のPrompt入力チャネルであるRevision TaskのTitleへ折り込む（新しいPrompt注入機構は追加していない）
- `REVISION_LIMIT_REACHED`/`NO_PROGRESS_DETECTED`のFailureEnvelope統合（`reviewedWorkflowOuterEnvelope`）: 既存のConversation Projection／Command Ledger／HTTP／UI伝播経路（ADR-0041/0047）はそのまま、2つの新しいCodeを転記するだけ
- `policy.ProgressPolicy`（`go/internal/policy/progress.go`）: Task状態を直接変更しない純粋な決定境界。`RepeatedFeedbackProgressPolicy`をNo-Progress v0として実装（同一Task lineageで正規化済みReview所見が既定2回連続一致した場合のみ停止）。Provider非依存、embedding／AI判定は使わない
- 並列Branch（A成功／B上限到達／C成功）のケースで、A/Cの結果を失わず、Synthesisを誤って完了扱いにせず、B単独のRecoveryだけでA/Cを再実行せずにSynthesisが再開することを、新しいコードを書かずに既存の`RunParallel`/`EvaluateAllReadiness`の組み合わせだけで達成（統合テストで確認）
- Web UI: 既存の`taskEvidenceBlock`/`deliverableViewerNode`を再利用したRecovery専用画面。composerは「追加の指示（任意）」を受け付ける専用modeになり、既存のprocessing live status表示とは重複しない

対象外のまま（将来の別Checkpoint候補）：Deliverable内容の意味的比較（embedding／類似度判定）、Cost／Tool Call量に基づくProgress判定、Deliverableハッシュ・digest比較の実装、BudgetGuard／Scheduler統合、ADR-0049 Case B相当の完全なcrash-recovery対称性。詳細は[ADR-0052](adr/ADR-0052-revision-limit-recovery-and-no-progress-foundation.md)を参照してください。

## Completed — Progress Intelligence v1

[ADR-0053](adr/ADR-0053-progress-intelligence-v1.md)（Accepted）により、No-Progress Foundation v0（Review所見の文字列一致）を、Review／Deliverable／Execution 3つの独立したdeterministic signalの組み合わせへ進化させました。成功条件は「AIが何回働いたか」ではなく「働いた結果、成果が実際に改善しているか」です。embedding／semantic AI judgeは使わず、判定のために別のLLMループを新設してもいません。

- `policy.ReviewSignature`/`NewReviewSignature`: Review所見を自由文ではなく既存のtyped enumフィールド（`review.Issue`のCategory／Severity）だけから構造的に比較。sort＋dedupeによりIssueの記述順序やGoのmap iteration順に依存しない。Providerが同じ指摘を別の言い回しで書いても、Category／Severityが同じなら同一signatureになる
- `policy.DeliverableFingerprint`/`NewDeliverableFingerprint`: Deliverable本文を改行コード統一・行末/前後空白trimだけのcontent-blindな正規化後、SHA-256でhash化した内部専用のopaque値。Domain・Vault・Audit・Event・UI・外部JSON Contractのいずれへも一切永続化・露出しない。changed/unchangedの二値のみで、類似度スコアは持たない
- `policy.CompoundProgressPolicy`: Review Progress（同一構造signatureの連続一致）・Deliverable Progress（fingerprintの連続不変）・Execution Progress（Revision消費数）の3条件が**すべて**一致したときだけ停止する保守的な複合Policy。単一信号だけでは絶対に停止しない（false positive対策）。既定threshold（全て2）は`autonomy.DefaultMaxRevisionCount`と同じ保守的な値
- `ProgressSignal`へ`ReviewSignature`／`ConsecutiveSameReviewCount`／`DeliverableChanged`／`ConsecutiveUnchangedDeliverableCount`／`ProviderCallCount`／`ElapsedDuration`をadditiveに追加。Resource Signal（Provider call数・経過時間）は既存の`worker.TokenUsage`/`Duration`から算出する観測用フィールドで、Policyのdecisionには使わない（Cost推定は今回作らず、将来のBudgetGuardへ委ねる）
- Production wiring（`process/reviewed_workflow.go`）を`RepeatedFeedbackProgressPolicy`（v0）から`CompoundProgressPolicy`（v1）へ切り替え。v0自体は削除せず、直接/operator呼び出し元向けに残置
- No-Progress停止も既存のRevision Limit Recovery UX（`interaction.workflow.recover_revision`、既存の`taskEvidenceBlock`/`deliverableViewerNode`）をそのまま再利用——別のRecovery画面は作っていない。並列Branch（A成功／B No-Progress／C成功）でも、A/Cの結果を失わず、B単独のRecoveryだけでSynthesisが再開することを統合テストで確認

対象外のまま（将来の別Checkpoint候補）：意味的類似度・embeddingベースの比較、Cost estimate、Token usageのProgressSignalへの追加、ErrorKind（FailureEnvelope Code）反復をProgress Intelligenceへ使う実装（設計ノートのみ）。詳細は[ADR-0053](adr/ADR-0053-progress-intelligence-v1.md)を参照してください。

## Completed — BudgetGuard v1

[ADR-0054](adr/ADR-0054-budgetguard-v1.md)（Accepted）により、「出力が改善しているか」（Progress Intelligence）とは独立した第三のPolicyとして、「許可された資源envelopeを既に超えたか」を判断するBudgetGuardを実装しました。成功条件は「少ない人間操作から多くの成果を出す」こと自体ではなく、「1回の依頼が有限時間・有限Provider利用で必ず停止可能であること」です。

- `policy.BudgetPolicy`/`FixedBudgetPolicy`/`BudgetDecision`/`BudgetSignal`/`TokenUsageSignal`（`go/internal/policy/budget.go`）: Task状態を直接変更しない純粋な決定境界。Runtime／Provider Call Countの2軸を独立してチェックし、いずれか一方の超過だけで`BudgetEscalate`を返す（Progress Intelligenceの保守的なAND条件とは逆の設計——Budgetは安全上限であり、収束判断ではないため）
- `autonomy.Contract.MaxProviderCalls`（既定60、上限300）/`MaxRuntime`（既定30分、上限2時間）: 既存の`MaxParallelTasks`等と同じ0=未設定規約。scopeは1 Reviewed Workflow execution単位（CEO Plan生成・Recovery自体は対象外）
- `internal/service`の`budgetTracker`: `policy.BudgetPolicy`とは意図的に分離した、実際に並列安全な予約primitive（atomic reserve→invoke→record）。並列Branchが同時に最後の1枠を要求しても、超過は決して起きないことを200 goroutine・`-race`下の並列テストで確認。process-local・1呼び出しscopeのみの保証（durable化は将来課題として明記）
- `worker.TokenUsage`の既存nilable設計（`*int`、unknown≠0）をそのまま利用したToken集計。v1ではゲーティングに使わず観測用のみ。Cost Budgetは未実装（価格tableが存在しないため、`ProviderCallCount × 仮単価`のような計算はどこにも作っていない）
- FailureEnvelope: 単一Code `BUDGET_EXCEEDED` + `Category`（`"runtime"`/`"provider_call"`）で区別。実装中に見つけた実バグ（`runBranch`内部ループのcontext deadline判定が常に汎用的な`"cancelled"`へ分類されていた）を修正し、専用の回帰テストで検証
- 並列Branch（A成功／B Budget停止／C成功）でA/Cの結果を失わず、Synthesisを誤って完了扱いにせず、replayが新しいProvider呼び出しをゼロにすることを、実HTTP mock・実Command Ledgerを通した統合テストで確認
- Recovery UX（ADR-0054時点）: Budget停止が残す「既存Revision Task作成済み・未実行」の状態は、Revision Limit用のstalled-task検出と構造的に一致しないため、BudgetGuard v1単体ではRecoveryを配線しなかった。このgapは次節のADR-0055で、既存Revisionを二重作成しないContinuationとして解消済み

対象外のまま（将来の別Checkpoint候補）：Cost Budget／価格table、outer CEO Command全体を対象にしたdurable root Budget（CEO Plan生成と複数Recoveryの合計を含む）、durable/Ledgerベースの予約、Metrics集計（provider-calls-per-command等）。詳細は[ADR-0054](adr/ADR-0054-budgetguard-v1.md)を参照してください。

## Completed — BudgetGuard Recovery Continuation

[ADR-0055](adr/ADR-0055-budget-recovery-continuation.md)（Accepted）により、BudgetGuard停止時に既に作成済みだったRevision Taskを、CEOの1回の明示操作と新しいCommandで安全に続行できるようにしました。これはProvider callのretryでも、元Task／Workflowの再実行でもありません。

- `interaction.workflow.recover_revision`を共通のCEO-facing operationとして維持。Revision Limit／No Progressでは従来どおり新しいRevisionを作り、`BUDGET_EXCEEDED`ではcanonical evidenceから一意に導出・再検証した既存Unstarted Revision Taskだけを続行する
- `ReviewedWorkflowRunService.ResumeRevision`: pending Revisionを最初のtargeted roundとして必ず先に実行し、成功後だけ既存`EvaluateAllReadiness`へ戻る最小Continuation primitive。Synthesis専用resume logic、新Domain、新Task状態ownerは追加しない
- Recoveryは新しいCommand ID／Ledger claim／deterministic child IDs／Interaction Session CASが必須。同じCommand replay、元Workflow replay、stale画面からの再送はいずれもProvider callゼロ。異なるRecovery Commandの同時送信も1件だけがCASを通る
- Recoveryごとに通常のsafe default（Provider calls 60、Runtime 30分）を持つ新しいbounded Workflow Budget scopeを開始。CEOへBudget counter／concurrency／resume modeを入力させない。繰り返す人間承認Recoveryを含むdurable root Budgetは未実装
- 元Workflow CommandをTask Eventの`CorrelationID`、新しいdeterministic child Commandを`CausationID`として保持。既存Task ID、Revision intent、Interaction recovery Turn、Command Ledgerだけでlineageを追跡し、新しい永続lineage IDは追加しない
- Browser Acceptance専用の小さいBudgetは、明示loopback Provider fixtureに限るdaemon edge optionから注入。production default、public Command JSON、`.env`、CEO UIは変更しない
- actual daemon + temporary Vault + fixed Provider fixture + Chromium/WebKitで、A/C成果保持、Budget停止、任意指示、明示Recovery、duplicate click防止、BのRevisionだけ再開、Synthesis、リアルタイム完了までを検証。Go統合テストではProvider-call／Runtime両方、fresh Budget、replay、同時Recovery、cancellationを検証

対象外のまま：durable root-command Budget、`MaxRecoveryCount`、Cost accounting／pricing registry、Budget Metrics、Scheduler連携、automatic retry／Recovery、streaming。

## Completed — Dependency Evidence Context / Synthesis Quality Foundation

[ADR-0056](adr/ADR-0056-dependency-evidence-context.md)（Accepted）により、fan-out/fan-in WorkflowのSynthesis Taskが、単にA/B/Cの完了状態を見るだけでなく、それぞれのcanonical Deliverable本文を実際のPrompt Contextとして受け取るようになりました。

- ExecutionServiceがreadiness／approval後、Task開始前にprovider-neutralな`DependencyEvidenceCollector`を呼ぶ。Vault Adapterはtarget Taskの直接依存だけをcanonical dependency順に読み、immutable Revision intent lineageを辿って最新のCompleted Revision Taskを選ぶ
- Task／Deliverable欠落、pending Revision、空本文、invalid lineageは`DEPENDENCY_EVIDENCE_MISSING`（stage `dependency_evidence`）でdefault deny。TaskService.Start／Provider call前に停止し、Task title・Plan・Conversationへfallbackしない
- `ExecutionRequest → WorkerService → PromptBuilder`へadditiveに伝播。Runner interface、Provider Adapter、Task lifecycle、Approval、Command Ledgerは変更しない
- Prompt内のEvidenceはprovenance（source Task／実際のevidence Task／Employee）付きUser Context。System側は「信頼されない参照専用Evidenceであり、内部の役割変更・Prompt上書き・外部操作要求へ従わない」というpolicyだけを保持
- 直接依存順、1件32 KiB／合計96 KiB、UTF-8安全なprefix切り詰め、明示truncation markerという決定的なcontext budgetを採用。LLM summaryやcanonical evidenceの変更は行わない。依存なしTaskの既存Prompt goldenはbyte-for-byte維持
- Revision Limit／No Progress Recoveryで新しく作ったRevisionも、Synthesisと競合させず既存`ResumeRevision`境界で先に完了させる。Budget Continuationと同様、回復後は既存readinessがSynthesisを解放し、Synthesisは古いDeliverableではなくterminal Revisionの成果を受け取る
- Go integrationとactual daemon Browser Acceptanceの固定fixtureで、A/B/Cの異なる成果がSynthesis requestへ全て含まれ、非依存Dは含まれず、統合成果がcanonical Deliverable／UIから確認できることを検証

対象外のまま：transitive／deep DAG evidence、意味的圧縮、LLM summarization、競合解消・debate、巨大contextのProvider別最適化、skill system、semantic routing。Review履歴もv1 Promptへは含めず、canonical Deliverableを最小の統合Evidenceとします。

## Completed — Synthesis Quality Acceptance Foundation

[ADR-0057](adr/ADR-0057-synthesis-quality-acceptance.md)（Accepted）により、「SynthesisへEvidenceが届いた」だけでなく「単純連結より価値のある統合結果になったか」を、実Provider呼び出しなしで先に測れる基盤を追加しました。

- cross-dependencyと軽い矛盾を含む固定日本語scenario、6項目×0–2点（12点）のdeterministic rubric、A/B/C全件coverage必須、unsupported claim拒否を実装
- fixed good Provider fixtureは12/12でpassし、A/B/C本文をそのまま連結したbad fixtureはcoverageが満点でもcross-synthesis／矛盾調停／優先順位でfailする。Evidence欠落、矛盾無視、priority欠落、unsupported claimも個別に回帰検証
- temporary VaultへA/B/CをTaskService／Deliverable Store経由でcanonical commitし、既存Reviewed Workflow／BudgetGuardがSynthesisを実行、canonical DeliverableをEvaluatorがread-only採点するproduction-path integration
- Prompt observationはsystem/user byte数、Evidence順序、dependencyごとのtruncation、安全instructionだけをmemory内で保持。credential、Authorization header、raw user data、Provider responseをAcceptance artifactとして永続化しない
- `make synthesis-acceptance PROVIDER=fake-good`でFake baseline、`PROVIDER=claude`でnetwork/credential不要dry-run。actual Claudeは人間が別途`EXECUTE=1`を明示したときだけ1 Synthesis callを許可し、Reviewは固定fixture、Budgetは2 calls／10分、retry／fallbackなし
- ResultはProvider／model／logical route、rubric、TokenUsage、duration、prompt shape、call数、canonical commit有無をsafe JSONで出す。結果とtemporary Vaultは自動保存／Git追加しない

未完了：actual real-provider benchmark、複数Provider／model比較、Provider-specific prompt tuning、role-based model routing、反復run集計、semantic evaluator／LLM-as-Judge、pricingを含むCost比較。Provider-specific policyは再現可能なAcceptance差が得られるまで導入しません。

### Prompt Quality v2 follow-up（Cross-Evidence + Actionability）

実Claude baseline（`claude-sonnet-5`、10/12、Cross-Evidence Synthesisと Actionabilityがそれぞれ1/2）を受け、この2項目だけを狙った最小限のPrompt追加とobservability追加を行いました（ADR-0057 Addendum参照）。

- `internal/prompt`の通常Task Prompt Builderへ、依存2件以上（Synthesis相当のfan-in）の場合だけ「複数の参照情報を関連付けて1つの結論を作る」「最優先の提案にはaction・根拠・期待効果・検証方法を含める」という方針を追加。scenario固有keywordは含まず、依存0〜1件のPromptは既存goldenとbyte-for-byte互換
- `worker.StopReason`（completed／max_tokens／stop_sequence／unknown）をClaude Adapterの生`stop_reason`から変換し、`worker.RunResult`→`ExecutionResult`→`execution.Result`へadditiveに伝播。production dispatchの判断には使わずobservability専用
- Synthesis Quality Acceptanceのsafe reportへ`stop_reason`／`output_truncated`（max_tokensのときだけtrue）を追加し、明示`ArtifactPath`設定時だけcanonical Synthesis Deliverable全文をGit外・実Vault外のファイルへ書き出すHuman Review Artifactを追加（既定では何も書かない）
- Evidence Coverage／Conflict Handling／Prioritization／Unsupported Claims（既に満点だった4項目）とrubric・threshold（10/12）は無変更。deterministic keyword-group評価戦略も変更なし

実測結果（`claude-sonnet-5`、1回のexternal call、one-shot、retry/fallbackなし）：Score 8/12（FAIL、baseline比 -2）、Cross-Evidence Synthesisは1/2で変化なし、Actionabilityは1/2→0/2で悪化。`StopReason=max_tokens`／`OutputTruncated=true`——出力は最優先項目のExpected Effect途中で打ち切られ、validation methodおよび後続の優先度には到達しませんでした。Human Reviewでは、生存しているprose自体はEvidence間の関連付けを既に相応に行っていることを確認——スコアの主因はEvaluatorの厳格さではなくoutput truncationである可能性が高いことが分かりました。この実測結果を受けて調査・実装したのが次節のProvider Output Completeness Policyです。

## Completed — Provider Output Completeness Policy

[ADR-0058](adr/ADR-0058-provider-output-completeness-policy.md)（Accepted）により、Provider呼び出し自体の成功（HTTP成功、`claude.Error`なし）と、Task deliverableとしての完全性を別の問いとして扱うようにしました。目的はSynthesis品質改善ではなく、`Failure / Partial Completion Observability`の欠落を閉じることです。

- `internal/execution`へ`ErrorOutputIncomplete`（`OUTPUT_INCOMPLETE`、既存stage `worker`を再利用）をadditiveに追加。`ExecutionService.Execute()`は`workerResult.StopReason == worker.StopReasonMaxTokens`を検出した時点でDeliverable保存へ進まず、既存の`recoverExecutionFailure`（TaskService.Fail→既存`policy.ExecutionPolicy`によるHold判断→TaskService.Hold）へ分岐する。新しいTask state・新しいRecovery機構は追加していない
- Runner／WorkerServiceは無変更——判断はExecutionServiceだけが行い、Provider呼び出しの成否とDeliverable完全性の判断を混同しない
- truncated contentはcanonical Deliverableとして確定保存されない。生成された部分本文は`execution.Result.WorkerResult.Content`（既存field）を通じて診断目的にのみ残る
- `executionFailureEnvelope`（ADR-0041が確立した既存の唯一の分類地点）が新しいKindを1つ扱うだけで、Reviewed Workflow／Command Ledger／HTTP／UIへの伝播ロジックは無変更のまま新しい失敗がそのまま流れる
- Synthesis Acceptance harnessの`StopReason`/`OutputTruncated`抽出を、`ExecuteReviewedWorkflow`失敗時の早期returnより前へ移動——truncated attemptでもこれらが安全reportから観測可能なままになるよう回帰を防いだ。新しい`FailureOutputIncomplete`カテゴリを既存`FailureProvider`判定より先にチェックし、誤ってProvider失敗として分類しないようにした
- `defaultMaxTokens=3000`は変更していない。CLIでの値変更機能も追加していない——`ClaudeProcessConfig.MaxTokens`のoverride plumbing自体は既に存在するため、将来必要になった場合の新規実装コストは小さい
- Go unit/integration test（ExecutionService単体、`ExecuteTask`の実Command chain end-to-end、`RunParallel`での並列branch保存、Synthesis Acceptance harness）で、completed／stop_sequence／unknownは既存成功経路のまま変更なし、max_tokensだけが新しい経路を通ることを確認。実Provider呼び出しはこのCheckpointでは行っていない

対象外のまま（future candidate、本Checkpointとは意図的に分離）：

- **output token default policy**: `defaultMaxTokens=3000`自体の変更判断、値の選定、ADR-0045に倣ったRuntime composition levelでの明示override機構の実装。次節のClaude Output Token Policyで対応済み
- **Prompt compression**: token ceilingを増やさずtruncationを避けるための、priority数の明示制限やoutput構造のcompact化。引き続き未着手
- **Cross-Evidence evaluator再検討**: truncationが解消され再測定できるまで保留。引き続き保留（次のreal Acceptance再実行の結果待ち）

## Completed — Claude Output Token Policy

[ADR-0059](adr/ADR-0059-claude-output-token-policy.md)（Accepted）により、`internal/adapter/claude`のprivate defaultとして暗黙的だったoutput token ceilingを、Runtime composition-owned・明示的なpolicyへ整理しました。ADR-0058（Provider output completeness、truncation自体をどう扱うか）とは別概念であることを明確化——本CheckpointはProvider output allowance policy（`max_tokens`に何を設定するか）だけを扱います。

- `internal/runtime.DefaultClaudeMaxTokens`（新規、`internal/runtime/claude_output_policy.go`、既定6000）を、ADR-0045の`DefaultProviderRequestTimeout`と同じ構造のRuntime composition-owned canonical policyとして追加
- `cmd/workcairn`（全10箇所）、`cmd/workcairn-daemon`、`internal/synthesisacceptance`のAcceptance harnessが、すべて同一のこの定数を`ClaudeProcessConfig.MaxTokens`へ明示的に渡す。Acceptance専用の別値は使わない（`TestHarnessUsesTheSameProductionMaxTokensPolicyNotATestOnlySpecialValue`で検証）
- `internal/adapter/claude`の既存`defaultMaxTokens=3000`は変更せず、MaxTokens未設定（0）のcaller向けdefensive fallbackとしての位置づけに限定。二重source-of-truthにはならない
- 6000という値は、実測v2 one-shot benchmarkのtruncation地点（3000、生成内容から全体の40〜50%程度と推定）を主要根拠とし、単純な2倍という追跡しやすい係数で決定。Claudeモデル自体のtoken capabilityやrequest timeoutとの関係は外部知識として参考にしたが、real API呼び出しによる検証はしていないことをADRで明記
- Task-type別のspeculative routing（Synthesisだけ別値等）は、証拠が単一の実測truncationのみで根拠が不足しているため見送り、単一共有default（Option A）を採用
- CLI／daemonへの明示override flagは、明確な運用ユースケースが無いため追加していない。既存plumbingにより将来の追加コストは小さい
- Go unit/integration test（`ExecuteTask`のconfig propagation、Adapter defensive fallbackの独立test、Acceptance parity、ADR-0058のmax_tokens typed failure regressionが値変更後も維持されること）で検証。実Provider呼び出しはこのCheckpointでも行っていない

対象外のまま（future candidate）：CLI override flag、task-type別policy、Prompt compression、Cross-Evidence evaluator再検討、Cost/pricing、real Claude Acceptanceの再実行（次Checkpoint候補）。

## Investigation Record — Agentic OS Quality Foundation

[AgenticOSQualityFoundation.md](AgenticOSQualityFoundation.md)は、Synthesis Quality Acceptance（ADR-0057）を「会社型AI OS」全体の品質観測という将来観点から位置づけ直した調査記録です（ADRではなく、実装済み機能でもありません）。Benchmark artifactに現在欠けているmetadata（Evaluator version、Prompt version、Scenario content fingerprint、Human review notes）の候補一覧、Role／Planning／Execution／Memory／Governanceの5軸のうちどれが現在測定済み・未測定・「Go側で構造的に保証済みなためLLM品質questionとして測ること自体が誤り」かの分類、Skill/Role/Memory/Policy/Evidence/Approval/Evaluationの既存対応先マッピング、Agent Routingの既存トリガー条件確認を記録します。実装は伴わず、次に real evidence が蓄積した際の判断材料としてのみ機能します。

Phase Hの追記（同ファイルAddendum）で、`synthesisacceptance.Result`／`ReviewArtifact`へ`MaxOutputTokens`（ADR-0059の`DefaultClaudeMaxTokens`をそのまま反映、additive field）を実装しました——design invention不要かつuser指定の具体的gapだったための例外的な最小実装です。`evaluator_version`／`prompt_version`／`scenario_content_hash`／`human_review_notes`は、real run数がまだ少なくversioning schemeを設計する根拠が不足しているため引き続き未実装のままです。

Phase O（Synthesis）でCross-Evidence語彙calibrationをreal evidenceに基づき完了・loop closeし、Phase Pの調査を経て、Phase Qで[docs/PlanningQualityAcceptance.md](PlanningQualityAcceptance.md)（`internal/planningacceptance`）をFake Provider限定のFoundationとして追加しました。既存`process.GenerateCEOPlan`をread-onlyに再利用し、Structural Gate（既存`ceoplan`不変条件）とQuality Rubric（Intent Coverage／Dependency Quality／Unsupported Assumptions／Missing Information Awareness の4軸）を分離しています。Real Provider実行・pass threshold・Decomposition/Prioritization/Execution Readiness評価は未着手のままです。

Phase Qが発見したCompany OS Governance gap（`CEOPlanService.Generate`が`worker.StopReason`を一切確認せず、truncated出力が単なるIntent parse failureとして扱われていた問題）を、Phase Rで[ADR-0058](adr/ADR-0058-provider-output-completeness-policy.md)の適用範囲拡張として最小実装しました。`CEOPlanOutputIncompleteStage`（Code `OUTPUT_INCOMPLETE`、既存`ErrProviderOutputIncomplete`sentinel再利用）を`ParseIntent`実行前に追加し、Execution側と同一のCodeで統一的に検索可能にしています。新しいTask state、Provider failure分類、recovery機構は追加していません。

Phase Sの調査（Real Provider実行なし）で、Planning AcceptanceにSynthesis Acceptance相当のArtifact／metadata／CLI基盤が欠けていることを確認し、Phase T-0でFake Provider限定のまま整備しました。`internal/planningacceptance`へ`ReviewArtifact`（Normalized Planを保存、credential非含有）、`TokenUsage`/`Duration`/`StopReason`/`Runner`/`MaxOutputTokens`のResult metadata、`scenario_v1.json`内の`provider_fixtures`（good/bad named fixture、CLIから選択可能）を追加し、`cmd/workcairn-planning-acceptance`と`make planning-acceptance`を`workcairn-synthesis-acceptance`と同型で新設しました。`service.CEOPlanResult`へ`StopReason`をadditiveに追加（成功経路のみ、失敗経路の診断保持は今回対象外として明記）。Real Provider実行は0のまま、次のPhase Tでの実行判断に委ねています。

Phase Tで初回Real Provider Planning Acceptance Runを実行し、`CEOPlanRunnerStage`が2種類の失敗を混同していたことを確認、Phase T+1の調査を経てPhase T-2で`CEOPlanRunnerFailedStage`/`CEOPlanInvalidRunnerResultStage`/`CEOPlanTimeoutStage`/`CEOPlanCanceledStage`へ分離しました。Phase T-3で初のReal Planning Quality Evidence（Structural Gate通過）を取得し、Phase T-4のReplication Runでは別の失敗（`CEOPlanIntentStage`）が発生。Phase T-5の調査で、`ceoplan.IntentParseError`が既に安全なReason/Field/FieldShapeを保持し、production Interaction flowは既存ADR-0041の`failure.ParseDiagnostic`へ変換済みである一方、`internal/planningacceptance`だけがStage文字列のみへ縮退させていたことを特定しました。Phase T-6で`planningacceptance.Result`へ`Parse *failure.ParseDiagnostic`をadditiveに追加し、既存primitiveの再利用のみでこのgapを閉じています（新taxonomy・新Stage・新Artifactは追加せず）。Phase T-7はcredential preflightで停止（Real API calls: 0）、Phase T-8で2件目のReal Planning Quality Evidence（1 Taskのみ生成、Human Review poor）を取得しました。Phase T-9の調査で、T-3（5 Tasks, good）とT-8（1 Task, poor）が同一Score 5/8だった原因を特定：既存Intent CoverageはObjective/Summary/Task textを一括でconcept matchingするため、ObjectiveがCEO requestをほぼ復唱していたT-8ではTaskへ仕事が落ちていなくても満点になっていました。Phase T-10で`expected_intent_concept_groups`を再利用したWork Coverage軸（Task Title/Rationaleのみを評価）をadditiveに追加し、MaxScoreを8→10へ拡張、T-3/T-8のreal artifactsをpinned regression fixtureとして固定しました（T-3: 7/10, T-8: 5/10）。Dependency Qualityのtask-count gate責務混同（同Investigationで発見）は今回未着手のまま残しています。Phase T-11の調査でこの混同を分解し（task-count completenessとgraph reasoning correctnessの責務分離、position-based比較の長期妥当性、concept-mapping/required-edge modelの実現可能性を検討、いずれもschema変更不要と確認）、Phase T-12で最小変更を実施しました：`len(actual)!=len(expected)`によるhard 0-gateを撤廃し、`min(actual,expected)`のcommon-prefix比較へ変更、count mismatchはDetailsへdiagnostic（`task_count_mismatch:expected=N:actual=M`/`compared_positions:K/N`）として残し、満点は比較対象が期待graph全体をカバーした場合のみとしました。T-3は9/10（Dependency 2/2）、T-8は6/10（Dependency 1/2）となり、同じ0という曖昧な意味は解消されています。Concept-based node mapping、required-edge/forbidden-edge model、順序非依存比較は引き続きdeferredです。

Phase T-13でFresh Real Planning Quality Validation Run（2 Tasks生成、Work Coverage 1/2、Dependency 1/2、Total 7/10）を実行し、5-axis EvaluatorがT-3（9/10, good）とT-8（6/10, poor）の間に自然に位置する第三のReal Evidenceを正しく識別できることを確認しました。既知の限定事項（Missing Information literal matching、position-based Dependency mapping、Summary "placeholder"の再発）はいずれもblockerではないと判断し、Planning Quality Foundationを「sufficient for now」としてclose、Company OS本線（Goal/Responsibility階層）へ戻りました。Phase Uの調査で現行repositoryにGoal/Responsibility/Routine概念が存在しないことを確認し、`Goal → Responsibility (future) → Planning (Routine/Workflow) → Task`という階層と、Goal firstでResponsibilityを別Checkpointへ分離する方針（Case C）を確定しました。Phase U-1で[ADR-0060](adr/ADR-0060-goal-domain-foundation.md)に基づきGoal v1 domain foundationを実装しました：`internal/goal`（GoalID/Scope/Status/Version、`scheduler`のID・CAS shapeを再利用）、`internal/adapter/vault/goal_store.go`（`会社/Goals/`・`プロジェクト/<name>/Goals/`の両scope、canonical JSON→Markdown projection、ADR-0010のcommit順序を再利用）、`internal/service/goal_service.go`（Kernel未登録、`goal.created`/`goal.achieved`/`goal.abandoned`のみをEvent発行）、`internal/process/goal.go`（既存Command Ledger claim-before-effectをreuse）、`workcairn`操作CLI（`goal-create`/`goal-list`/`goal-show`/`goal-achieve`/`goal-abandon`）を追加。Employee ownership、Task/Plan直接接続、Responsibility、Routine、deadline/priorityはすべて意図的に未実装のままです。

Phase RC-0の調査でPython除去（旧Go Only移行）はrepository上すでに完了済みであることを確認し（`.py`ファイル0件、`make v1-release-gate`自体が再混入を拒否するGate済み）、残るPublic Beta前タスクは人間・実機・ビジネス側の項目のみでcode変更を要しないと判断しました。Phase U-2で[ADR-0061](adr/ADR-0061-responsibility-domain-foundation.md)に基づきResponsibility v1 domain foundationを実装しました：`internal/responsibility`（ResponsibilityID/Scope/Status/GoalRefs、GoalのID・CAS shapeを再利用しつつStatusはactive/inactiveの再activate可能な2-stateへ独自化）、`internal/adapter/vault/responsibility_store.go`（`会社/Responsibilities/`・`プロジェクト/<name>/Responsibilities/`の両scope、Recordと独立したCAS lineageを持つBinding canonical file `*.binding.json`）、`internal/service/responsibility_service.go`（Kernel未登録、GoalRefs存在確認を`GoalLookup`経由・同scope限定で実施、Employee bindingを`EmployeeLookup`経由で既存Organization rosterへ確認、single-owner Assign/Unassign、5種のEvent発行）、`internal/process/responsibility.go`（既存Command Ledger claim-before-effectをreuse、`vaultEmployeeLookup`は既存`vault.Loader`をラップするのみで新Vault primitiveなし）、`workcairn`操作CLI（`responsibility-create`/`list`/`show`/`activate`/`deactivate`/`assign`/`unassign`）を追加。GoalへResponsibilityRefsは追加せず一方向参照を維持、Goal achieved/abandonedからResponsibilityへの自動cascadeなし、Responsibility→Work generation（Plan/Task/Workflow/Schedule自動生成）は次Checkpointへ明示的に分離したままです。

Phase U-3で[ADR-0062](adr/ADR-0062-responsibility-work-generation.md)に基づきResponsibility→Planning接続を実装しました：`internal/process/responsibility_work.go`（`process.GenerateResponsibilityPlan`、手動trigger限定）がResponsibilityのTitle／linked Goals／Bindingを既存の`responsibilityStoreFor`/`goalStoreFor`/`goalScopeFrom`ヘルパーで解決し、明示Human instructionと合成したRequest文字列（`interaction.Record.PlanningRequest()`と同型のplain string concatenation）を既存の`GenerateCEOPlan`（新Planning engineは作らず、Provider呼び出し・`ceoplan.BuildPrompt`/`ParseIntent`/`NormalizeIntent`をverbatim再利用）へそのまま渡します。Responsibility TitleのみからWork requestを捏造しない設計（Model B、明示instruction必須）、未assignedなResponsibilityも計画可能（Binding不要と判断——`ceoplan`のassignment解決はRequiredRoleのみでownerを一切参照しないため）、Responsibility ownerはTask assignmentを一切上書きしない、traceabilityは`ceoplan.Plan`自体のschemaを変更せず新wrapper`ResponsibilityPlanningResult`のみで実現、Command Ledger未wrap・新Event未追加（いずれも`GenerateCEOPlan`自体の既存precedentに合わせた判断）。`workcairn responsibility-plan`CLI操作（`--instruction`新flag、既存`--responsibility-id`/`--responsibility-scope`/`--model`/`--approved`を再利用）を追加し、生成されたPlanは既存`ceo-plan-apply`（別途明示承認）へそのまま渡せることをintegration testで確認しました。Responsibility自身はTaskを直接作成せずWorkflowも直接実行しません。Scheduler／Event trigger、Routine、Interaction Session統合は次Checkpointへ明示的に分離したままです。

Phase U-4で[ADR-0063](adr/ADR-0063-routine-automation-foundation.md)に基づきRoutine v1（Responsibility自動化）を実装しました：`internal/routine`（RoutineID/Scope/ResponsibilityID/Instruction/Model/Trigger/Status、初期状態はInactive——Goal/Responsibilityの「常にActiveで開始」から意図的に逸脱し、Create自体をScheduler副作用なしに保つ）、`Trigger`はdaily／weekly限定・`NextOccurrence`はUTC日付演算のみでcron parserなし。既存Scheduler（ADR-0025）を監査した結果one-shot限定であることを確認し、汎用recurring schedulerは作らず、代わりにRoutine側がrecurrence semanticを所有——`ExecuteRoutineActivate`が既存の`ExecuteScheduleCreation`を1回呼び次回発火分のSchedule 1件だけを作成し、新schedulable operation`routine.plan`（`commandcontract`/`ProcessExecutor`へ追加、Command Ledger管理——手動`responsibility-plan`自体は既存precedent通り非Ledgerのまま）がActiveなRoutineなら既存`GenerateResponsibilityPlan`をverbatim再利用してPlan生成のみ行い、成功・失敗を問わず次occurrenceを再Chain（`Trigger.NextOccurrence`が常に入力より厳密に後の時刻を返すため、recurrenceとretryが構造的に区別される）。InactiveなRoutineへの発火はdispatch時のfresh Status確認でno-op skip（Schedule取消機能が`internal/scheduler`に存在しないため新規追加はせず、既存の一shot lifecycleのみで対応）。Routine activateの重複はDomain層のno-op transition拒否で自然に防止（新dedupe機構なし）。`internal/adapter/vault/routine_store.go`（`会社/Routines/`・`プロジェクト/<name>/Routines/`）、`internal/service/routine_service.go`（Kernel未登録、ResponsibilityID存在確認を`ResponsibilityLookup`経由・同scope限定、3種のEvent発行）、`internal/process/routine.go`/`routine_plan.go`、`workcairn`操作CLI（`routine-create`/`list`/`show`/`activate`/`deactivate`/`run-now`）を追加。`routine-run-now`はSchedule状態に一切触れない手動acceptance primitiveとして、Inactive Routineでも動作します。実際のSchedulerディスパッチ経路（`SchedulerService.RunDue`→`ProcessExecutor`→`routine.plan`）を実Vault不使用のend-to-end testで確認しました。Interaction Session統合、Attention Feed投影、Routine削除、汎用cron cadenceは次Checkpointへ明示的に分離したままです。

Phase U-4のFinal Reportが指摘したcorrectness risk（`ExecuteRoutineActivate`のRoutine active commit成功後にSchedule creationが失敗すると、「active Routineなのに次回occurrenceが存在しない」というContinuity violationが残り得る）を、Phase U-5で[ADR-0064](adr/ADR-0064-routine-scheduling-reliability.md)に基づき閉じました。既存Scheduler／Command Ledger／CAS／Routine Statusのみを再利用し、新しいtransaction framework・rollback・hidden retryは一切追加していません。`scheduleNextRoutineOccurrence`（activate・post-occurrence chaining・新規`routine-reconcile`の3箇所が共有する唯一のhelper）を書き込み前読み取りにより冪等化：この occurrence の決定的ID（`routineOccurrenceIdentity`、既存の`ROUTINE-<ID>-<dueAt>`schemeを再利用、新hashなし）でSchedule Storeを直接読み、非terminalなScheduleが既に存在すればそれをそのまま返して二重作成を防止。`InspectRoutineScheduleHealth`（Schedule Store走査のみ、新規永続stateなし）を追加し`routine-show`へ`schedule_healthy`として投影——Active Routineの次回occurrence欠落をon-demandで検出可能にしました（`internal/recovery`はTask/Deliverable/Review特化のためdomain不一致と判断し拡張せず）。唯一の明示的repair primitiveとして`routine-reconcile`CLI操作（承認必須・operator供給CommandID必須、Inactive Routineは拒否）を追加——`scheduleNextRoutineOccurrence`を再利用するのみで新repair機構は作っていません。Deactivate→Reactivateの重複防止も、従来のCommandID決定性への暗黙依存から、このSchedule Store直接読み取りによる明示的保証へ強化されました。Command Ledgerのreplay/conflict、Event publication failure（既存の「commit survives Event failure」discipline）はいずれも変更していません。

Phase U-6で[ADR-0065](adr/ADR-0065-company-attention-feed.md)に基づきCompany Attention / Decision Feed v1を実装しました：`internal/attention`（Type／EntityType／ActionKind／Item、他Domainを一切importしない純粋read model）と`process.InspectAttention`（新規state保存・Command Ledger claim・Event発行のいずれも行わないread-only集約）。既存の「次にHumanが何をすべきか」primitiveを再利用のみで構成：Interaction側は`interaction.Record.State`/`Next()`をそのまま分類（`approval_required`／`human_input_required`／`interaction_attention_required`）、Routine側は`InspectRoutineScheduleHealth`（Phase U-5）をそのまま再利用（`routine_recovery_required`）。`recovery_required`（`internal/recovery`）・Task Hold・project-scope Routine attentionは「全Project列挙」の既存primitiveが存在しないため、Responsibility未割当・Responsibility無しGoalは既存semantics上actionableな根拠がないため、いずれもv1では意図的に不採用としました（ADR-0065に判断根拠を記録）。Dedupe（Type+EntityID）とSort（Type順→ObservedAt→EntityID）は決定的で、AI ranking・embeddings・rules engineはいずれも使っていません。`workcairn attention-list`CLI操作と、`GET /v1/attention`HTTP endpoint（既存`CompanyActivityInspector`と同型のoptional-capability interfaceで配線）を追加しました。Company Viewは将来この`InspectAttention`結果をそのまま描画するだけで構築可能な状態です。

Phase U-7で[ADR-0066](adr/ADR-0066-headless-credential-resolution.md)に基づきHeadless Credential / Unattended Operation foundationを実装しました。daemon composition rootのcredential resolutionを`automatic`／`environment`／`keychain`／`headless-local`のclosed sourceへ集約し、明示sourceは他sourceを一切読まないdefault-denyにしました。既定`automatic`は既存interactive First-run互換のenvironment→Keychainだけで、headless-localへ暗黙fallbackしません。headless-localはOS user config root配下の固定fileをread-onlyで扱い、regular／non-symlink、0600、current-user owner、Lstat→open→fstat同一性、bounded sizeを満たさなければ安全拒否します。WorkCairnによるsecret file writer／Keychain migration／`.env` fallbackは追加せず、GUI Keychain setupもenvironment／headless-local modeでは起動しません。external secret manager、credential rotation、automatic migration、Linux keyring統合は後続候補です。

Phase PQ-1で[ADR-0067](adr/ADR-0067-planning-placeholder-rejection.md)に基づき、実Provider Planning出力で繰り返し観測されていた「placeholder」defect（Phase T-3／T-8／T-13、および直近のHuman Acceptance run——社内FAQページ作業計画requestでSummary／Task titleがいずれも文字列"placeholder"）を閉じました。根本原因はProvider自由生成（構造的にvalidだが意味的に空）とPipeline側のcontent検証欠如の組み合わせと確認し（Prompt literal漏出・Schema example汚染・fallback混入・test fixture汚染はいずれもsource確認により否定）、新しいEvaluatorやbanned-word engineは作らず、既存の唯一の共有choke point`ceoplan.NormalizeCandidate`（`ParseRunnerOutput`／`NormalizeIntent`（Interaction・Responsibility・Routineが共有する`GenerateCEOPlan`経由）／`ValidateApprovedPlan`のApply時再検証、いずれもここを通過）へ、大文字小文字を無視したtrim済み完全一致のみで判定する狭い`isPlaceholderValue`チェックを1箇所だけ追加しました。対象はPlan Summary／Task Title／Task Rationaleの3 fieldのみ（根拠のないObjective／ProjectName／CEOQuestionsへの拡張はせず）。検出時は既存の`ceoplan.ParseFailureReason`（新規`ParseFailurePlaceholderValue`を1件追加のみ）経由の型付き失敗として拒否し、`CEOPlanService.Generate`の既存Stage分類ロジックは無変更のまま自然に`CEOPlanParserStage`として分類されます。自動retry・自動regenerate・fallback content・空文字列や「未設定」への置換はいずれも追加していません（型付き拒否のみ、次のPlanningはHuman Operatorが明示的に開始）。防御的にPrompt（1行）とJSON Schema field description（2箇所）へも同じ語彙の注意書きを追加しましたが、実際の保証はruntime側の`NormalizeCandidate`チェックが担います。`internal/planningacceptance`の5-axis Evaluator自体は無変更です（runtime contractとEvaluator scoringの混同を避けるため）。

## Completed — Public Beta Go Only Repository

ADR-0033に基づき、外部公開前に移行用compatibility distribution、tests、entry point、package metadata、SDK依存、専用build／release toolingを撤去しました。JSON Contract v1、Prompt golden、Markdown／migration fixtureはGo testsが直接検証するlanguage-neutralな契約資産として残します。完了記録は[MigrationHistory.md](MigrationHistory.md)を参照してください。

## Current — Public Beta Preparation

大規模機能追加を止め、第三者がclone／installして安全に試せる状態を固定します。候補versionは`v1.0.0-beta.1`です。

実装済み：

- macOS／Linux 4 targetのCGOなしcross-build gate
- temporary Vault + Mock ProviderのTask／Review／Revision／mobile Interaction smoke target
- clean install、temporary Vault、iPhone到達手順
- `VERSION`、SECURITY、CONTRIBUTING、Public Beta Release Checklist
- archive allow-listとmacOS／Linux checksum生成
- ADR-0034に基づくWorkCairn product／binary／module／archive rename
- iPhone既定のMy Actionsと、PC／iPad既定のCompany View foundation
- ADR-0035に基づくWorkflow単位のAutonomy Contract、canonical evidence由来のProof of Work／CEO Attention
- ADR-0036に基づくredacted Provider Connection StatusとAutomatic model resolution
- ADR-0037に基づくFirst-run Wizard、明示Starter Organization setup、一般向け「進め方」、read-only Timeline、persistent failure、polling state保護

Public Beta向け差別化foundationでは、安全側の固定contractだけを提供します。Shadow Mode、Employee Authority、支出上限、長期KPI Storeは既存Approval／Ledger／Adapter境界へ自然に追加できる設計だけを記録し、先取り実装しません。

PHASE PB-1で外部公開前のPublic Beta Readiness Inventoryを実施し、Internal Core／Go Only／PQ-1（placeholder修正）が既にengineering blocker 0であることを確認しました。続くPHASE PB-2で、Human Operatorが決定済みの製品方針（初期対応環境はmacOS／arm64のみ、iPhoneはfeatureとして存在するがPublic Beta必須acceptance条件ではない、Obsidianはoptional viewer、iCloud Driveは必須ではない、複数Mac／VMはPublic Beta必須ではない）へ、CHANGELOG、[Release Notes](ReleaseNotes.md)、[Public Beta Release Checklist](PublicReleaseChecklist.md)を同期し、`v1.0.0-beta.1`のdarwin/arm64 release archiveを実際にbuild・checksum・allow-list検証しました。PHASE PB-2.1で旧`Workspace OS`ブランド残存の監査（新規発見なし）とUI上の不要な機種名依存（Mac固有表現、Employee個人名の代わりにRole表示）を整理し、PHASE PB-2.2でGo module pathをGitHub canonical repository（`WorkCairn`）の大文字小文字へ揃え（ADR-0068）、PHASE PB-2.3で「AI社員が動いている様子」を見せるCompany/Employee activity UIをPublic Betaから一旦非表示・削除しました。PHASE PB-2.4でREADMEを日本語中心の利用者向け構成へ全面再編し、`--mobile`daemon flagを実態に合わせて`--local-network`へrename（ADR-0069、compatibility不要のため旧名は完全削除）、AGENTS.md／`.ai/workspace.md`／ROADMAP.mdのcurrent-state syncを行いました。PHASE PB-2.5でREADMEを英語標準・日本語併記のbilingual構成（`README.md`／`README.ja.md`）へ分割し、PHASE PB-2.6〜PB-2.8でREADME簡潔化とdark mode contrast修正（Settings dialog、First-run Wizard、主要action button）を行い、PHASE PB-2.9でCONTRIBUTING.mdを開発者向けに全面拡充して`CONTRIBUTING.ja.md`を追加しました。PHASE PB-2.10でREADME／Quickstartの`/releases/latest`導線を`/releases`一覧へ変更して旧`Workspace OS v0.1.0`がlatestとして案内される問題を避け、Public Release Checklistのplatform matrixからautomated Browser Gateと物理iPhone任意確認の記述を分離し、PB-2時点のarchiveが検証サンプルでありfinal archiveはfinal cross-reviewと全修正後のtag対象commitから再生成することを明確化し、staleなUI空状態表現をliving docsで現在の表示へ同期しました（Accepted ADRの歴史的記述は変更していません）。残る項目はいずれもHuman Operatorのaccount／実機操作であり、追加のengineering／documentation作業ではありません。

PHASE PB-2.11でCodexによる独立Final Public-Beta Cross-Reviewを実施し、Engineering Gate（`make v1-release-gate`、`make public-beta-smoke`、`make check-ui-full`）とArchitecture／Security実装はいずれもGreenでしたが、Release Notesのrepository-relative linkとcredential policyの記述にP1を2件発見しました。PHASE PB-2.12でこの2件のP1と、OperatorGuide／Architecture／Public Release Checklistの小さなP2／P3 docs driftを修正しました。PHASE PB-2.13とPB-2.13aのCodex focused re-reviewでは、Plan生成時にCommand LedgerとInteraction Sessionへ証拠を保存する境界までOperator Guideへ明記し、全findingの解消を確認しました。PHASE PB-2.14では、`CONTRIBUTING.ja.md`と`SECURITY.md`に残っていた一般語の不自然な英語混在を整理し、設定値や型名などの識別子を維持したまま日本語表現を統一します。PB-3は引き続きpackaged-binary／Provider Human Acceptanceとして維持します。

Public Beta公開前に残る人間／実環境確認：

- WorkCairnの正式商標clearance、GitHub Private Vulnerability Reportingの有効化、GitHub Discussions有効化（support窓口として採用する場合）。GitHub repository slugは`WorkCairn`へ実rename済み（PHASE PB-2.1）
- Human Operator自身のMacでのpackaged binary（darwin/arm64）acceptance。iPhone実機、iCloud Drive、Obsidian連携はいずれもPublic Beta必須条件ではなく、確認する場合も任意項目として扱う
- 配布対象を将来darwin/amd64、linux/amd64、linux/arm64へ拡張する場合のnative filesystem／daemon smoke（初期Public Betaでは不要）
- temporary Vaultとtest credentialによる最小Provider smoke（packaged binaryに対して、PB-3で実施）
- tag作成、Release title確定、push、GitHub Release公開のrelease owner承認
- [完了] native folder picker、Application Supportの再起動参照、Mac native hidden-input＋Keychainによりterminal不要のmacOS First-runを実装。trusted LANへpath／secret値を受けるendpointは追加しない
- [完了] ADR-0044でmacOS Keychain保存を不定な`security`対話PTYから、anonymous socketとbounded helperを使うSecurity.framework native Adapterへ置換。write後read-back、existing update、restart read、timeout時kill/reapを固定
- [完了] ADR-0045でProvider request timeoutをRuntime compositionへ一本化し、Public Beta defaultをboundedな5分へ変更。CEO Intent、Task、Review、Revision Task／再Reviewは同じclient policyを使い、operator override、typed timeout、no retryを維持
- Provider streamingはprogress観測、partial stream failure、cancellation、durable diagnosticsを設計する後続候補とし、Public Beta timeout安定化では導入しない
- ADR-0036の`workcairn-auto`を起点に、複数Provider導入時だけEmployee Role／Task capability／接続済みRuntime／quality・cost・latency policyからtyped Routeを解決する拡張（暗黙fallbackなし）

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
