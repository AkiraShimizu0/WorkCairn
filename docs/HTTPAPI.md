# HTTP Command API

`workspace-daemon`は、Go Only Runtimeをloopback HTTPから利用する同期Command入口です。現在はremote公開用ではありません。認証、TLS、authorizationを追加するまで、既定の`127.0.0.1:8787`から外へbindしないでください。

## 起動

```bash
bin/workspace-daemon --vault /path/to/temporary-or-approved-vault
```

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

対応operation：

| operation | scope | payloadの主なidentity |
|---|---|---|
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

Payloadはunknown fieldを拒否します。CEO plan generation、read-only plan／inspection、migration、Recovery applyはこのeffect Command endpointへ含めません。

`GET /v1/schedules`と`GET /v1/schedules/{schedule_id}`はone-shot Scheduleのpending／dispatching／terminal stateをread-onlyで返します。daemonは`--scheduler-interval`ごとにdueまたはmissed pending Scheduleを確認し、保存済みの同一Command ID／payloadを既存Processへ配送します。`dispatching`は自動resumeしません。

`GET /v1/notifications`と`GET /v1/notifications/{event_id}`は、Task／Review／Revision Eventから作成したimmutable local Inboxをread-onlyで返します。recordはEvent envelopeのidentityだけを持ち、payload、metadata、Prompt、Task title、Provider情報を含みません。`GET /v1/metrics`はdaemon process開始後のEvent type別件数を返し、再起動時にresetされます。

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
