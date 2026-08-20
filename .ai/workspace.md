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
- ADR-0035のAutonomy ContractはWorkflow承認範囲を安全側のtyped valueへ固定します。Proof of Work／CEO AttentionはInteraction、Task、Deliverable、canonical Review、Revision intent、Command Ledger、Auditから再構成するread-only projectionで、新しいsource of truthや自動修復を持ちません。
- ADR-0036によりdaemonは起動時Provider設定をnetwork accessなしのredacted statusとして公開し、未接続時はPlan生成前に案内します。新規Web Interactionは依頼ごとのModel選択を要求せず論理`workcairn-auto`を使い、Claude Adapter edgeのversioned policyがsupported modelへ解決します。製品CLI／daemonにmodel環境変数は不要です。Role／Task別routingは既存Runner Registry手前の将来typed policyであり、未接続Providerへのfallbackはしません。
- ADR-0042によりPublic Betaの一般daemonは`workspace.setup`と`interaction.start|plan.generate|answer|plan.apply|workflow.execute`だけをside-effect allow-listへ持ちます。direct Task／Review／Revision、plain／direct Reviewed Workflow、writer、Scheduler、External Actionはoperator CLI／内部Processとして維持しますが、一般Web UI／daemonからは実行できません。
- ADR-0043の`make public-beta-browser-gate`はtest-only Playwrightでactual daemon、temporary Vault、固定Provider fixture、Chromium／WebKitを通し、pairing、polling、Revision、reload、restart、FailureEnvelope UIを検証します。NodeはBrowser Acceptance harness限定で、Go module、製品Runtime、release archive、`v1-release-gate`へ含めません。
- ADR-0044によりMac loopback限定のClaude接続は、native hidden-inputからanonymous socketでbounded helperへcredentialを渡し、Security.frameworkを直接呼んでKeychainへ保存・read-backします。`security`対話PTY、secret argv、平文fallbackは使わず、timeout時はhelperをkill/reapします。Local Web UIはredacted接続状態とAutomatic routingだけを表示します。
- ADR-0045によりProvider request timeoutはRuntime compositionへ一本化し、Public Beta defaultをboundedな5分にします。CLI／daemon override、request context cancellation、typed `provider_timeout`、no retry／no fallbackを維持し、streamingは後続候補です。
- ADR-0046によりCEO Intent Contractの`project_name`／`objective`／`summary`をProvider必須fieldから外しました。`objective`は`interaction.Record.PlanningRequest()`（CEOの元request＋確認済みclarification回答からなるcanonical planning input）へ、`project_name`はSessionID／RequestDigestだけから決定的に導出するfallbackへGoが委ねます（`time.Now()`や乱数は使いません）。3フィールドとも、キー欠落・空文字列・whitespace-onlyは許可しますが、明示的な`null`や非string型は型違反として拒否します。`steps`／`ceo_questions`はLLMのsemantic理解が必要な責務として厳格なrequiredのまま維持します。`required_role`のOrganization-constrained enum化とcapability-based assignmentへの長期移行は別ADRの将来作業です。
- ADR-0047により`process.InspectConversation`が、Interaction／Task／Review／Revision／Deliverable／Command Ledgerの既存canonical evidenceからread-onlyなConversation Projection（`ConversationEntry`: CEO Message／Directed Communication／Company Fact／Systemの4分類）を構築します。Speaker・Recipientは両方がcanonicalに確定する場合だけDirected Communicationとして設定し、Task assignmentのような一方向の事実からSpeakerを捏造しません。完成済みの日本語文章はGoで組み立てず、typed factsだけを返します。Domain Event自体は無変更です。UI・HTTP露出は今回行っていません。
- ADR-0048により`ceoplan.IntentJSONSchema`は`steps[].required_role`を、現在のOrganization rosterから決定的に導出した`CanonicalRoleTitles`（trim・空文字列除外・重複排除・辞書順ソート）のenumへ制約します。Starter Organization固有のRole名はハードコードせず、Prompt（`BuildPrompt`）とSchemaは同一の`employees`入力から同一関数で語彙を導出するため乖離しません。使用可能なRoleが0件の場合はfree-form stringへ自動fallbackせず`ErrNoAllowedRoles`でfail-closedします。`organization.ResolveTaskAssignment`・write fallback・capability policyは無変更です。`required_role`はcapability-based assignment（Provider-neutralなcapability enum → Go所有の`AssignmentPolicy` → Organization resolver）への長期移行に伴うdeprecation candidateとして記録しました（未実装）。
- ADR-0049によりCEOの通常操作を2回の明示承認へ縮約しました。`interaction.start`／`interaction.answer`は、Session commit直後に同一Command実行内で決定的child ID（`commandledger.DeriveChildCommandID`）を使い`interaction.plan.generate`の核へ直接進み、`plan_generation_approval_required`での追加承認を通常経路では発生させません。新設した`interaction.plan.approve_and_execute`は、`Record.Next()`が`plan_approval_required`で返す`operation`として、CEOの単一承認を`ceo_plan.apply`・`workflow.reviewed.execute`という2つの独立したdeterministic child Command（各々既存のLedger claim／replay／finishをそのまま使用）への承認として扱います。Reviewer・Autonomy Contract・Task上限（既定`20`、既存UIが常に固定送信していた値）はGoが内部で決定し、ブラウザはchild Commandを一切送信しません。child失敗のFailureEnvelopeは再分類せずforwardし、自動retryはしません。crash後の「Plan apply成功／Workflow未着手」は`Turn.PreAuthorizedWorkflowCommandID`と`Record.PendingWorkflowPreAuthorization()`により、`Next()`が既存の`NextInspectWorkflow`（新しいKindは追加せず）でLedger参照を示す形で機械的に判別可能です。既存の標準`interaction.plan.apply`／`interaction.workflow.execute`／`interaction.plan.generate`はoperator／crash-recovery向けに削除せず維持し、Public Beta allow-listへ`interaction.plan.approve_and_execute`を追加しました。実装過程で、複数段のCommand chainが`errors.Join`のJoin順序により`errors.As`が古いchildの分類を誤って返し得る不具合を発見し、`finishDurableCommandRecord`の引数順序を修正しました。
- ADR-0050により「履歴から削除」をアーカイブ／アンアーカイブ（一覧からの可視性切り替え）として実装しました。物理削除ではなく、Interaction Session・Turn履歴・Project／Task／Deliverable／Review／Revision／Command Ledger／FailureEnvelopeのいずれも変更しません。新設Turn Kind `TurnArchived`/`TurnUnarchived`をSessionのappend-only履歴へ追記するだけで、`Record.State`（既存Workflow状態機械）とは完全に直交します。`Record.IsArchived()`はTurn履歴から都度導出する読み取り専用の判定で、ストアされたbooleanフィールドは持ちません。新設Command `interaction.archive`/`interaction.unarchive`は既存の`POST /v1/commands`契約・Command Ledger claim-before-effect・CAS（`expected_version`必須）をそのまま再利用し、Public Beta allow-listへ追加済みです。既にアーカイブ済み／既にアクティブなSessionへの新しいCommand IDでの再操作は、既存`RecordX`系メソッドの規約に揃えて`ErrInvalidState`で明示的に拒否します（同一Command IDの再送はCommand Ledgerのreplayが別途保証）。`GET /v1/interactions`は既定でアクティブのみを返し（後方互換）、`?archived=true|all`で切り替えます。詳細参照（`.../{id}`・`.../conversation`・`.../work-report`）はアーカイブ後も無条件に継続可能です。UI変更・物理削除は本ラウンドの対象外です。
- ADR-0051（Accepted）のLeverage Engineは、CEOの単一承認（`interaction.plan.approve_and_execute`）から複数Taskが依存グラフの形に応じて自動的にbounded並列実行され、Synthesis（依存を持つ通常のTask）で統合されるところまでを本番経路へ配線しました。新operationは追加せず、既存の唯一のoperation`workflow.reviewed.execute`（`ExecuteReviewedWorkflow`）が内部で常に`ReviewedWorkflowRunService.RunParallel`を駆動し、readyなTaskが1件なら逐次相当、複数件ならGoが自動的に並列実行します――呼び出し元がparallel／sequential／concurrencyを選ぶ経路はどこにも存在しません。`ceoplan.IntentStep.ParallelWithPrevious`（LLMが供給する唯一のfan-out/fan-in signal）からGoが依存グラフを構造的に構築し、`autonomy.Contract`へ追加した`MaxParallelTasks`／`MaxRevisionCount`（Revision Guard、branch独立counting）と、`ceoplan.MaxGeneratedTasks`（Plan生成側、既定5）がLoopGuardの最低ラインを構成します。`event.Event.CorrelationID`は`interaction.plan.approve_and_execute`の外側Command IDを一貫してrootとし、`CausationID`は各段階の子Command IDです。再帰分解、Specialist Routing、No Progress Detector、Budget Guardの実測・強制は引き続き対象外です。
- ADR-0052（Accepted）のRevision Limit Recoveryは、Revision Guard／No-Progress Foundationで止まったBranchを、新設Command`interaction.workflow.recover_revision`によりCEOの新しい明示承認（対象Task＋任意の追加指示）から安全に再開します。内部は既存の`revision.execute`と`runInteractionWorkflowChain`（ADR-0049）の再利用のみで、新しいWorkflow再開ロジックは持ちません。追加指示は既存の唯一のPrompt入力チャネル（Revision TaskのTitle、`revision.Intent.AdditionalGuidance`）へ折り込み、lineageは新しい永続IDを追加せず既存のTask ID／Command IDだけ（`interaction.Turn.RecoveryTaskID`/`RecoveryGuidance`）で表現します。`REVISION_LIMIT_REACHED`／`NO_PROGRESS_DETECTED`は既存のFailureEnvelope伝播経路（ADR-0041）へ2つの新しいCodeとして統合され、Conversation Projection／Command Ledger／HTTP／UIの既存コードパスはそのままです。新設の`policy.ProgressPolicy`（Task状態を直接変更しない純粋な決定境界、optional設定）は、No-Progress v0として`RepeatedFeedbackProgressPolicy`（同一lineageで正規化済みReview所見が既定2回連続一致した場合のみ停止、非AI・非embedding）を実装しました。並列Branchの一部だけがRecoveryされ、他Branchを再実行せずSynthesisが再開するケースは新規コードなしで既存`RunParallel`/`EvaluateAllReadiness`の組み合わせだけで動作します。Web UIは既存の`taskEvidenceBlock`/`deliverableViewerNode`を再利用したRecovery専用画面を追加し、composerは「追加の指示（任意）」を受け付ける専用modeになります。意味的Progress判定、Cost/Tool Call量ベースの判定、Deliverable比較の実装は引き続き対象外です。
- ADR-0053（Accepted）のProgress Intelligence v1は、No-Progress Foundation v0（Review文字列の完全一致比較）を、Review／Deliverable／Execution 3つの独立したdeterministic signalの複合判定へ進化させました。`policy.ReviewSignature`/`NewReviewSignature`はReview所見を自由文ではなく既存のtyped enum（`review.Issue`のCategory／Severity）だけから構造的に比較し、sort＋dedupeによりIssue記述順序やmap iteration順に依存しません。`policy.DeliverableFingerprint`/`NewDeliverableFingerprint`はDeliverable本文をcontent-blindに正規化（改行コード統一・行末/前後空白trim）したSHA-256で、Domain・Vault・Audit・Event・UI・外部JSON Contractのいずれへも一切永続化・露出しない内部専用のopaque値です（changed/unchangedの二値のみ、類似度スコアは持ちません）。`policy.CompoundProgressPolicy`はReview Progress・Deliverable Progress・Execution Progressの3条件が**すべて**一致したときだけ停止する保守的なPolicyで、単一信号だけでは絶対に停止しません（既定threshold全て2）。Production wiring（`process/reviewed_workflow.go`）は`RepeatedFeedbackProgressPolicy`（v0、削除せず残置）から`CompoundProgressPolicy`（v1）へ切り替えました。`ProgressSignal`へ`ReviewSignature`／`ConsecutiveSameReviewCount`／`DeliverableChanged`／`ConsecutiveUnchangedDeliverableCount`／`ProviderCallCount`／`ElapsedDuration`をadditiveに追加しましたが、Resource Signal（Provider call数・経過時間、既存の`worker.TokenUsage`/`Duration`から算出）はPolicyのdecisionには使わない観測用フィールドです（Cost推定は作らず、将来のBudgetGuardへ委ねます）。No-Progress停止は既存のRevision Limit Recovery UXをそのまま再利用し、別のRecovery画面は追加していません。embedding／semantic AI judge、意味的類似度判定、ErrorKind反復の実装は引き続き対象外です。
- ADR-0054（Accepted）のBudgetGuard v1は、「出力が改善しているか」（Progress Intelligence）とは独立した第三のPolicyとして、「許可された資源envelopeを既に超えたか」を判断します。`policy.BudgetPolicy`/`FixedBudgetPolicy`（Task状態を直接変更しない純粋な決定境界）はRuntime／Provider Call Countの2軸を独立してチェックし、いずれか一方の超過だけで`BudgetEscalate`を返します（Progress Intelligenceの保守的なAND条件とは逆――Budgetは安全上限であり収束判断ではないため）。`autonomy.Contract.MaxProviderCalls`（既定60、上限300）/`MaxRuntime`（既定30分、上限2時間）は既存の0=未設定規約に従い、scopeは1 Reviewed Workflow execution単位です（LoopGuardの構造的counterとは別物、CEO Plan生成・Recovery自体は対象外）。`internal/service`の`budgetTracker`は`policy.BudgetPolicy`とは意図的に分離した、実際に並列安全な予約primitive（atomic reserve→invoke→record）で、並列Branchが同時に最後の1枠を要求しても超過が決して起きないことを200 goroutine・`-race`下の並列テストで確認済みです（process-local・1呼び出しscopeのみの保証、durable化は将来課題）。`worker.TokenUsage`の既存nilable設計（unknown≠0）をそのまま利用しますが、v1ではゲーティングに使わず観測用のみで、Cost Budgetは未実装です。FailureEnvelopeは単一Code`BUDGET_EXCEEDED`+`Category`（`"runtime"`/`"provider_call"`）で区別し、実装中に見つけた実バグ（`runBranch`内部ループのcontext deadline判定が常に汎用的な`"cancelled"`へ分類されていた）を修正・回帰テストで検証しました。Recovery UXは既存の`interaction.workflow.recover_revision`を意図的に配線していません――Budget停止が残す状態は既存のstalled-task検出と構造的に一致しないため、v1のCEOは完了済み成果の閲覧のみ可能です（無意味なdead UIを提示しない設計判断、詳細はADR-0054）。Budget停止に対するRecovery Command、Recovery時のBudget reset/inherit決定、Cost Budget、outer CEO Command全体を対象にしたBudgetは引き続き対象外です。
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
