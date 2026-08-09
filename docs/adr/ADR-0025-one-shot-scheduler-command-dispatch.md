# ADR-0025: Schedulerは承認済みone-shot CommandをLedger経路へ配送する

## Status

Accepted

## Context

ADR-0021〜0024により、主要な副作用commandはCommand ID、request digest、terminal replayを持ち、複数Taskも既存Task／Review／Revision commandのcompositionとして実行できます。時間駆動を追加する際にSchedulerがTask、Provider、Vault writerを直接呼ぶと、承認、TaskService ownership、commit ordering、Ledger replayを迂回します。

一方、cron recurrence、missed-run catch-up列、自動retry、`running` command resumeまで同時に導入すると、現在のRecovery境界を越えて実行済み事実を推測する必要があります。最初のAutomationには、承認済みの特定Commandを特定時刻以後に一度だけ配送する境界で十分です。

## Decision

### Immutable approval and typed target

Scheduler foundationはone-shot Scheduleだけを提供します。`schedule-plan`はread-onlyで、Schedule ID、絶対時刻、approval reference、`workspace-command.v1`のtarget Command ID／operation／typed payloadを表示します。`schedule-create`またはHTTP `schedule.create`への1回の明示承認は、この完全なdefinitionを将来実行する承認として扱います。

targetは既存のclosedな副作用operationだけを許可します。unknown field、unknown operation、Scheduler制御command、API key等を含むtyped contract外payloadは永続化前に拒否します。Vault root、Provider secret、Base URLはSchedule recordへ保存せず、実行時もdaemonのRuntime edgeから既存Processへ注入します。Scheduleの承認後にpayload、時刻、Command IDを動的に補完・変更しません。

### Canonical record and commit ordering

canonical Schedule recordはVault直下の`.workspace-os/schedules/<Schedule ID SHA-256>.json`へ保存します。schema version、definition digest、offset付き`due_at`、作成時刻、target Command、state、Version、実行時刻、typed failure／resultを保持します。Schedule IDをfilesystem pathへ直接使いません。

commit orderingは次です。

```text
explicit Schedule approval
→ outer schedule.create Command Ledger claim
→ Schedule pending record atomic create (Version 1)
→ outer terminal outcome

due or missed one-shot tick
→ Schedule pending → dispatching CAS (Version 2)
→ exact target workspace-command.v1
→ target Command Ledger claim / existing product process
→ Schedule terminal outcome CAS (Version 3)
```

`due_at`を過ぎたpending Scheduleはdaemon再起動後の最初のtickで一度だけ対象にします。同じ時刻ではDueAt、Schedule ID順に同期配送します。offsetを含むRFC3339 instantで比較し、timezoneやDSTからrecurrenceを推測しません。

### Duplicate and crash semantics

配送前に`dispatching`をCAS commitし、同一process内の重複tickと複数pollerの二重配送を防ぎます。target Commandは常に同じCommand ID／payloadを使うため、Schedule outcome保存後の再読込やclient retryも既存Ledger semanticsと整合します。

- `pending` commit前の失敗ではtargetを配送しない。
- `dispatching` commit後、target claim前の停止は`dispatching`を残す。
- targetがterminalになった後、Schedule terminal保存に失敗してもtarget resultをrollbackせず、Scheduleは`dispatching`のままpartial stateを示す。
- targetが`running`、Ledger partial、timeout等ならScheduleを`recovery_required`にする。
- targetの確定business failureはScheduleを`failed`にし、自動retryしない。
- atomic writeのunexpected temporary／storage entryは推測削除せずSchedule inventoryを拒否する。

`dispatching`、`failed`、`recovery_required`は自動でpendingへ戻しません。HTTP read-only Schedule inspectionとtarget Command Ledger inspectionで診断します。target result adoption、Schedule reconciliation、running resume、retry policyは確定証拠と運用要件を確認する後続ADRへ延期します。

### Lifecycle and boundaries

Scheduler Domain／ServiceはVault、Markdown、HTTP、Provider、Task状態を知りません。Vault AdapterはSchedule recordだけをatomic create／CAS updateします。Dispatcher Adapterは既存HTTP ProcessExecutorと同じtyped Processを直接呼び、subprocessやPython fallbackを使いません。

Scheduler lifecycleはWorkspace Kernelが所有し、`workspace-daemon`が具体Store、Dispatcher、clock、poll intervalを注入します。KernelはSchedulerの保存形式、Command payload、時刻計算ルールを知りません。shutdown時は新しいtickを停止し、in-flight contextをcancelして既存Ledger／partial failure境界へ接続します。

## Consequences

- 承認済みReviewed Workflow等を、Pythonなしで将来時刻から自律実行できます。
- SchedulerはTaskService、Review／Revision ordering、Provider Adapter、Auditを迂回しません。
- missed one-shotと重複triggerは安全に扱えますが、cron／recurrence、並列配送、catch-up batchはありません。
- crash後の曖昧な`dispatching`は観測できますが、自動resume／adoptionはしません。
- Notification／MetricsはSchedule record直書きではなく、既存Event subscriberまたは後続の観測境界として追加します。
