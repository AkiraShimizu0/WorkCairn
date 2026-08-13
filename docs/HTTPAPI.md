# HTTP Command API

`workcairn-daemon`は、Go Only Runtimeをloopback HTTPから利用する同期Command入口であり、同じGo binaryからmobile-first Local Web UIを配信します。現在はinternet／remote公開用ではありません。既定は`127.0.0.1`で、明示mobile modeだけprivate／link-local IPとprocess-local pairingを許可します。

## 起動

```bash
bin/workcairn-daemon
```

iPhoneとMacを同じtrusted local networkへ接続し、次でmobile modeを起動します。

```bash
bin/workcairn-daemon --mobile
```

private IPv4を自動選択し、iPhoneで開くURLとpairing codeをterminalへ表示します。必要なら`--listen 192.168.x.x:8787`のように明示できます。`0.0.0.0`、public IP、hostnameは拒否します。pairing codeは起動ごとに変わり、Vault／Session／browser storageへ保存しません。mobile modeのeffect POSTはpairing cookie、same-origin、`X-Workspace-Intent: mobile-ui.v1`を要求します。

daemonは`.env` fileを読みません。Provider commandに必要な設定はRuntime environmentから注入され、HTTP payloadではAPI key、Base URL、Vault rootを受け取りません。

## Command contract v1

```json
{
  "version": "workspace-command.v1",
  "command_id": "CMD-20260808-001",
  "operation": "task.execute",
  "approved": true,
  "payload": {
    "project_id": "PROJECT-001",
    "project_name": "ToDoアプリ",
    "task_id": "TASK-001",
    "current_time": "2026-08-08T16:30:00+09:00",
    "approval_reference": "approval-001",
    "execution_id": "EXEC-001"
  }
}
```

`POST /v1/commands`は`application/json`だけを受け付けます。Command ID、version、operation、`approved: true`は必須です。同じscopeで同じCommand ID／requestを再送すると保存済みresultを返し、異なるrequestは`COMMAND_ID_CONFLICT`、未確定の`running`は`COMMAND_IN_PROGRESS`で拒否します。

Interaction Sessionの開始前planはread-only `POST /v1/interaction-plans`、Project適用後のReviewed Workflow planは`POST /v1/interaction-workflow-plans`、任意のExternal Action handoffは`POST /v1/interaction-action-plans`を使います。いずれも`workspace-interaction.v1`で承認対象digestを返し、Provider／Action credentialを読みません。その後の`interaction.*`は通常の承認済み`workspace-command.v1`です。

対応operation：

| operation | scope | payloadの主なidentity |
|---|---|---|
| `workspace.setup` | workspace | 時刻。選択済み専用rootのlayoutとRuntime管理Starter Organizationを明示承認で作成。path／Employee／Provider設定はpayload外 |
| `task.execute` | project | Project ID／name、Task ID、時刻、approval reference、Execution ID |
| `review.execute` | project | Project、Task、Reviewer ID、Review version、時刻 |
| `revision.execute` | project | Project、source Task、Review version、時刻 |
| `workflow.execute` | project | Project、時刻、approval reference、1〜100のTask上限 |
| `workflow.reviewed.execute` | project | Project、Reviewer ID、時刻、approval reference、1〜100のTask上限 |
| `schedule.create` | workspace | Schedule ID、due at、作成時刻、approval reference、typed target Command |
| `ceo_plan.apply` | workspace | Project ID、validated CEO Plan、時刻 |
| `project.bootstrap` | workspace | Project ID／name、description、時刻 |
| `task.create` | project | Project name、title、assignee ID、時刻 |
| `project.dependencies.create` | project | Project name、typed dependency rows、時刻 |
| `organization.employee_hire` | workspace | typed Employee candidate、時刻 |
| `organization.employee_rename` | workspace | typed Rename request、時刻 |
| `organization.employee_id_repair` | workspace | approved ID repair list、時刻 |
| `organization.sync` | workspace | 時刻 |
| `action.wordpress.publish` | project | Project、Task、logical target ID、承認済みsource SHA-256、時刻。credentialはpayload外 |
| `interaction.start` | workspace | Session ID、自然言語request、logical model、承認済みrequest digest、時刻 |
| `interaction.plan.generate` | workspace | Session ID、expected Version、時刻。Provider credentialはpayload外 |
| `interaction.answer` | workspace | Session ID、expected Version、全質問へのtyped回答、時刻 |
| `interaction.plan.apply` | workspace | Session ID、expected Version、Project ID、承認済みplan digest、時刻 |
| `interaction.workflow.execute` | workspace outer＋project child | Session ID、expected Version、reviewer ID、Task上限、承認済みWorkflow plan digest、時刻。既存Reviewed Workflowを決定的child Commandで実行 |
| `interaction.action.wordpress.publish` | workspace outer＋project child | completed Session、Workflow内Task ID、logical target、承認済みAction plan digest、時刻。credentialはpayload外 |

Payloadはunknown fieldを拒否します。CEO plan generation、read-only plan／inspection、migration、Recovery applyはこのeffect Command endpointへ含めません。

Local Web UIの`interaction.*` commandは、同じendpointへ`Prefer: respond-async`を付けられます。daemonはtyped payloadとapprovalを先に検証し、受理時に`202 Accepted`、`Preference-Applied: respond-async`、`Location: /v1/commands/{command_id}?scope=workspace`を返します。実行はrequest contextから切り離されますがdaemon process lifetimeには従い、UIは既存Ledgerをread-only pollingします。CLI、Project scope command、通常の同期HTTP responseは変更しません。

これはdurable queueではありません。`202`後でもdurable claim前にprocessがcrashすればLedgerが存在しない可能性があります。`running`／partial／Ledger欠落を自動resume、retry、adoptせず、既存Recovery境界へ止めます。

`GET /v1/workspace-status`は、選択済みrootのstorage種別、WorkCairn layout、Starter Organizationの準備状態だけをredactedに返します。absolute path、Employee ID／model、Provider設定は返しません。

`GET /v1/interactions`と`GET /v1/interactions/{session_id}`はappend-only turn、state、Versionをread-onlyで返します。`GET /v1/interactions/{session_id}/next`は次のoperation、必要field、質問、承認要否、attention時のLedger参照を決定的に返します。Workflow turnは完全Result digestとbounded summaryを返し、Deliverable本文は複製しません。`GET /v1/interactions/{session_id}/work-report`は、保存済みInteraction、Task、Deliverable、canonical Review、Revision intent、Command Ledger、Auditを再読込し、Autonomy Contract、Proof of Work、CEO Attentionを返します。これは新しい永続sourceではなく、欠落を成功へ推測しないread-only projectionです。`GET /v1/organization`はReviewer選択用inventory、`GET /v1/projects/{project_name}/tasks/{task_id}/evidence`はcommit済みTask、Deliverable、canonical Reviewをread-onlyで返します。Session／成果物はredacted endpointではないため、loopbackまたはpairing済みtrusted LAN外へ公開しないでください。Interaction commandは人間の質問回答・承認を必要とするためScheduler対象ではありません。

`GET /v1/schedules`と`GET /v1/schedules/{schedule_id}`はone-shot Scheduleのpending／dispatching／terminal stateをread-onlyで返します。daemonは`--scheduler-interval`ごとにdueまたはmissed pending Scheduleを確認し、保存済みの同一Command ID／payloadを既存Processへ配送します。`dispatching`は自動resumeしません。

`GET /v1/notifications`と`GET /v1/notifications/{event_id}`は、Task／Review／Revision／Action Eventから作成したimmutable local Inboxをread-onlyで返します。recordはEvent envelopeのidentityだけを持ち、payload、metadata、Prompt、Task title、Provider情報を含みません。`GET /v1/metrics`はdaemon process開始後のEvent type別件数を返し、再起動時にresetされます。

WordPress Actionは`WORKCAIRN_WORDPRESS_TARGET_ID`、`WORKCAIRN_WORDPRESS_BASE_URL`、`WORKCAIRN_WORDPRESS_USERNAME`、`WORKCAIRN_WORDPRESS_APPLICATION_PASSWORD`をdaemon process環境から受け取ります。HTTP commandやScheduleへcredential、Base URL、Vault rootを含めません。`action.completed`はimmutable result evidence commit後にAudit／Notificationへ流れます。

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
- Provider command: `--provider-timeout`とrequest cancellationを適用

v1は同期requestです。durable background queue、自動resume、Event replay、Transactional Outboxは実装していません。
