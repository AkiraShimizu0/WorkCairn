# HTTP Command API

`workcairn-daemon`は、Go Only Runtimeをloopback HTTPから利用する同期Command入口であり、同じGo binaryからLocal Web UIを配信します。現在はinternet／remote公開用ではありません。既定は`127.0.0.1`で、明示`--local-network`だけprivate／link-local IPとprocess-local pairingを許可します。

## 起動

```bash
bin/workcairn-daemon
```

別デバイス（iPhone等）を同じtrusted local networkへ接続し、次で`--local-network`を起動します。

```bash
bin/workcairn-daemon --local-network
```

private IPv4を自動選択し、別デバイスで開くURLとpairing codeをterminalへ表示します。必要なら`--listen 192.168.x.x:8787`のように明示できます。`0.0.0.0`、public IP、hostnameは拒否します。pairing codeは起動ごとに変わり、Vault／Session／browser storageへ保存しません。`--local-network`のeffect POSTはpairing cookie、same-origin、`X-Workspace-Intent: local-network-ui.v1`を要求します。

daemonは`.env` fileを読みません。Provider commandに必要な設定はRuntime environmentから注入され、HTTP payloadではAPI key、Base URL、Vault rootを受け取りません。

## Command contract v1

```json
{
  "version": "workspace-command.v1",
  "command_id": "CMD-20260808-001",
  "operation": "interaction.plan.generate",
  "approved": true,
  "payload": {
    "session_id": "SESSION-20260808-001",
    "expected_version": 1,
    "current_time": "2026-08-08T16:30:00+09:00"
  }
}
```

`POST /v1/commands`は`application/json`だけを受け付けます。Command ID、version、operation、`approved: true`は必須です。同じscopeで同じCommand ID／requestを再送すると保存済みresultを返し、異なるrequestは`COMMAND_ID_CONFLICT`、未確定の`running`は`COMMAND_IN_PROGRESS`で拒否します。

Interaction Sessionの開始前planはread-only `POST /v1/interaction-plans`、Project適用後のReviewed Workflow planは`POST /v1/interaction-workflow-plans`を使います。いずれも`workspace-interaction.v1`で承認対象digestを返し、Provider credentialを読みません。その後の`interaction.*`は通常の承認済み`workspace-command.v1`です。

Public Betaの一般daemonで許可するside-effect operation：

| operation | scope | payloadの主なidentity |
|---|---|---|
| `workspace.setup` | workspace | 時刻。選択済み専用rootのlayoutとRuntime管理Starter Organizationを明示承認で作成。path／Employee／Provider設定はpayload外 |
| `interaction.start` | workspace | Session ID、自然言語request、logical model、承認済みrequest digest、時刻 |
| `interaction.plan.generate` | workspace | Session ID、expected Version、時刻。Provider credentialはpayload外 |
| `interaction.answer` | workspace | Session ID、expected Version、全質問へのtyped回答、時刻 |
| `interaction.plan.apply` | workspace | Session ID、expected Version、Project ID、承認済みplan digest、時刻 |
| `interaction.workflow.execute` | workspace outer＋project child | Session ID、expected Version、reviewer ID、Task上限、承認済みWorkflow plan digest、時刻。既存Reviewed Workflowを決定的child Commandで実行 |

allow-listはexact matchです。unknown operationと、`task.execute`、`review.execute`、`revision.execute`、`workflow.execute`、`workflow.reviewed.execute`、writer、Scheduler、External Action等の既知operator operationは`OPERATION_NOT_AVAILABLE`でExecutor前にdefault denyします。Payloadはunknown fieldを拒否します。

これらoperator operationは削除されていません。`workcairn` CLI、内部Process、Scheduler Dispatcher、Recoveryは従来のtyped contractを利用できます。`POST /v1/commands`の一般製品surfaceだけをADR-0042のInteraction Reviewed Workflow経路へ限定しています。JSON Contract v1、Ledger record、Vault schemaは変更していません。

Local Web UIの`interaction.*` commandは、同じendpointへ`Prefer: respond-async`を付けられます。daemonはtyped payloadとapprovalを先に検証し、受理時に`202 Accepted`、`Preference-Applied: respond-async`、`Location: /v1/commands/{command_id}?scope=workspace`を返します。実行はrequest contextから切り離されますがdaemon process lifetimeには従い、UIは既存Ledgerをread-only pollingします。CLI、Project scope command、通常の同期HTTP responseは変更しません。

これはdurable queueではありません。`202`後でもdurable claim前にprocessがcrashすればLedgerが存在しない可能性があります。`running`／partial／Ledger欠落を自動resume、retry、adoptせず、既存Recovery境界へ止めます。

`GET /v1/workspace-status`は、選択済みrootのstorage種別、WorkCairn layout、Starter Organizationの準備状態だけをredactedに返します。absolute path、Employee ID／model、Provider設定は返しません。

`GET /v1/interactions`と`GET /v1/interactions/{session_id}`はappend-only turn、state、Versionをread-onlyで返します。`GET /v1/interactions/{session_id}/next`は次のoperation、必要field、質問、承認要否、attention時のLedger参照を決定的に返します。Workflow turnは完全Result digestとbounded summaryを返し、Deliverable本文は複製しません。`GET /v1/interactions/{session_id}/work-report`は、保存済みInteraction、Task、Deliverable、canonical Review、Revision intent、Command Ledger、Auditを再読込し、Autonomy Contract、Proof of Work、CEO Attentionを返します。これは新しい永続sourceではなく、欠落を成功へ推測しないread-only projectionです。`GET /v1/organization`はReviewer選択用inventory、`GET /v1/projects/{project_name}/tasks/{task_id}/evidence`はcommit済みTask、Deliverable、canonical Reviewをread-onlyで返します。Session／成果物はredacted endpointではないため、loopbackまたはpairing済みtrusted LAN外へ公開しないでください。Interaction commandは人間の質問回答・承認を必要とするためScheduler対象ではありません。

`GET /v1/schedules`と`GET /v1/schedules/{schedule_id}`はoperator-onlyのread-only inspectionです。daemonの既存Scheduler backendは`--scheduler-interval`ごとにdueまたはmissed pending Scheduleを確認しますが、一般daemonから新しいScheduleを作成できません。`dispatching`は自動resumeしません。

`GET /v1/notifications`と`GET /v1/notifications/{event_id}`は、Task／Review／Revision／Action Eventから作成したimmutable local Inboxをread-onlyで返します。recordはEvent envelopeのidentityだけを持ち、payload、metadata、Prompt、Task title、Provider情報を含みません。`GET /v1/metrics`はdaemon process開始後のEvent type別件数を返し、再起動時にresetされます。

Notification／Metricsと既存External Action plan endpointはoperator-onlyのread-only inspectionとして残り、Public Beta UIには導線がありません。WordPress実行は一般daemon allow-list外です。operator CLI／内部Processで利用する場合もcredential、Base URL、Vault rootをCommandへ含めず、既存immutable evidence orderingを維持します。

## Status and recovery

```text
GET /v1/commands/CMD-20260808-001?scope=project&project=ToDoアプリ
GET /v1/commands/CMD-20260808-002?scope=workspace
```

statusはLedger recordをread-onlyで返します。`recovery_required: true`は自動retryの指示ではありません。`running`、outcome commit failure、partial failureは既存`recovery-inspect`でcanonical evidenceを確認し、新しいCommand IDでの再実行やartifact adoptionを推測で行わないでください。

## Lifecycle

- `GET /healthz`: process liveness
- `GET /readyz`: handler readiness
- `--scheduler-interval`: one-shot Scheduleのpoll間隔
- SIGINT／SIGTERM: 新規受付を止め、`--shutdown-timeout`まで実行中commandを待つ
- Provider command: ADR-0045のbounded default 5分を適用し、`--provider-timeout` overrideとrequest cancellationを維持

v1は同期requestです。durable background queue、自動resume、Event replay、Transactional Outboxは実装していません。
