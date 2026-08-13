# ADR-0042: Public Betaの一般daemonをInteraction Reviewed Workflow経路へ限定する

## Status

Accepted

## Context

Go Only移行の過程で、`workcairn-daemon`の`POST /v1/commands`は通常Task、Review、Revision、plain／Reviewed Workflow、Project／Task／Organization writer、Scheduler、External Actionまで、内部Processが持つほぼすべての副作用Commandを直接dispatchできるようになりました。これらはoperator、Recovery、内部compositionの検証には有用ですが、一般利用者がdirect CommandとInteraction経路を混在させると、質問、plan digest承認、reviewer解決、Reviewed Workflow、Session evidenceというPublic Betaの安全境界を迂回できます。

Public Betaの主要happy pathを一度に一つの入口へ限定しつつ、CLI、Recovery、Scheduler、既存Processを壊さないexposure boundaryが必要です。

## Decision

### 正式product path

一般利用者の正式経路は次だけです。

```text
First Run
→ Interaction Start
→ CEO Intent
→ deterministic Go Canonical Plan
→ Plan Approval
→ Project / Task commit
→ Reviewed Workflow Approval
→ Task Execution / Deliverable
→ Reviewer / Typed Review
→ Revision / Re-review
→ Completion
→ Timeline / Proof of Work
```

### daemon side-effect allow-list

一般`workcairn-daemon`の`POST /v1/commands`は、次のexact allow-listだけをExecutorへ渡します。

- `workspace.setup`
- `interaction.start`
- `interaction.plan.generate`
- `interaction.answer`
- `interaction.plan.apply`
- `interaction.workflow.execute`

unknown operationとそれ以外の既知operationは、Process実行より前に`OPERATION_NOT_AVAILABLE`でdefault denyします。prefix matchは使いません。`Prefer: respond-async`も同じallow-listを共有します。

### operator-only capability

`task.execute`、`review.execute`、`revision.execute`、`workflow.execute`、`workflow.reviewed.execute`、`ceo_plan.apply`、Project／Task／Organization writer、Scheduler、External Actionは削除しません。既存`workcairn` CLI、内部Process composition、Scheduler Dispatcher、Recoveryから引き続き利用できますが、一般daemonのside-effect surfaceからは到達不能にします。

Command Ledger status、Interaction／Next Action、Organization、Task evidence、Work Reportなど、正式経路のTimeline／Proof of Work／failure確認に必要なread-only inspectionは維持します。Schedule、Notification、Metrics、External Action planの既存read-only endpointはoperator inspectionとして残しますが、一般UIから導線を持ちません。

### Local Web UI

Local Web UIはInteractionと`workspace.setup`だけを発行します。plain Workflow、direct Task／Review／Revision、External Actionの操作UIは持ちません。Workflow完了時はCompletion、Timeline、Proof of Workを表示します。Project IDはclientが安全な乱数で生成し、通常画面で利用者へ入力させず、technical detailsでのみ確認できます。

UIはTask／Review／Revisionのbusiness ruleやoperation allow-listを再実装しません。最終防御はHTTP Adapterのallow-listであり、UIは既存Next Actionを表示する薄いprojectionのままです。

## Compatibility

- `workspace-command.v1`のJSON shape、Command Ledger record、JSON Contract v1、Vault schemaは変更しません。
- CLI operationと内部Process APIは削除・改名しません。
- 既存Ledger recordとcanonical evidenceは引き続きread-only inspectionできます。
- Public Beta前のHTTP exposureを狭める変更であり、保存データmigrationは不要です。

## Consequences

- 一般利用者はInteractionが所有するclarification、approval、Reviewed Workflow、Session evidenceを迂回できません。
- plain Sequential WorkflowはReviewed Workflowの内部planner／operator capabilityとして維持されますが、一般製品経路には現れません。
- Scheduler、Notification／Metrics、WordPress External Action、advanced Autonomy／Shadow ModeはBeta後surfaceとなり、一般UIからは非表示です。
- Browser E2Eは別Phaseです。本ADRでは既存Go handler／temporary Vault／Mock Provider smokeで正式経路とdefault denyを固定します。
