# ADR-0009: DeliverableをTask完了より先にcommitする

## Status

Accepted

## Context

ADR-0007は、初期ExecutionServiceの順序を`TaskService.Start → WorkerService.Execute → TaskService.Complete/Fail`とし、Deliverable保存を後続段階へ残しました。Go Vault Deliverable Adapterが利用可能になったため、Workerが生成した成果物とTaskの`完了`状態をどの順序で確定するかを明示する必要があります。

Taskを先に`完了`へすると、その後のDeliverable保存失敗によって「完了だが成果物がない」状態になります。一方、DeliverableとTaskStoreは別の保存先であり、単一transactionとしてcommitできません。保存済み成果物の削除やretryによる推測でこの差を隠すと、実行証跡を失い、同じRunner出力かどうか保証できない再実行を既存成果物へ結び付ける危険があります。

## Decision

### Commit ordering

通常Taskの成功経路は次の順序とします。

```text
TaskService.Start
→ WorkerService.Execute
→ DeliverableStore.Save
→ TaskService.Complete
```

DeliverableはTaskの`完了`状態より先にcommitします。ExecutionServiceはこの順序を調停しますが、Markdown形式やVault pathを知りません。構造化されたDeliverable DocumentをStorage-neutralな`deliverable.Store` portへ渡します。

### Deliverable保存失敗

Deliverable保存失敗はTask execution failureとして扱います。

```text
DeliverableStore.Save failure
→ TaskService.Fail
→ ExecutionPolicy
→ 必要ならTaskService.Hold
```

ExecutionServiceは保存失敗を型付き`Stage`と`ErrorKind`へ変換し、Providerやfilesystemの生エラーを公開契約へ漏らしません。Taskの失敗事実とHold判断は従来どおり分離します。

atomic publication後のdirectory sync失敗など、Adapterが「Deliverableはcommit済みだがSaveは完全成功していない」と返した場合も成功扱いしません。保存済みRecordをpartial Resultへ残した上でFail／Hold経路へ進みます。

### Deliverable保存後のTask Complete失敗

Deliverable保存後に`TaskService.Complete`が失敗した場合、Executionはpartial failureです。

- Resultは保存済みDeliverable Record、最終的に確認できたTask状態、失敗Stageを保持します。
- 保存済みDeliverableはimmutable evidenceとして自動削除、上書き、rollbackしません。
- TaskStore更新前のComplete失敗と、TaskStore更新後のEvent publication失敗を型付きerrorで区別します。
- 呼び出し元はTaskが完了したと推測せず、ResultとStore状態を確認します。

### Retryと既存Deliverable

同じTask IDのDeliverableが既に存在する場合、Adapterは上書きせず`already exists`として拒否します。ExecutionServiceは次を推測しません。

- 既存Deliverableが今回のWorker出力と同一か
- 前回実行がTask Complete直前まで成功したか
- 既存Deliverableを再利用、削除、置換してよいか

v0.3では自動retry、成果物adoption、Idempotency Key照合、Command Ledgerを実装しません。再試行や復旧は保存済みRecordとTask状態を人間または将来の明示recovery commandが確認して行います。

### Ownership boundaries

- Task状態変更とTask lifecycle Event発行の唯一の所有者は引き続きTaskServiceです。
- ExecutionServiceは順序と失敗回復を調停しますが、Task状態を直接保存せず、Task Eventも発行しません。
- Deliverable AdapterはTask状態、承認、ExecutionPolicy、Audit、Task Eventを知りません。
- WorkerServiceとRunnerはDeliverable保存、Task状態、Auditを知りません。
- AuditはEvent subscriberとして接続し、Deliverable AdapterやExecutionServiceから直接Audit Logへ書きません。

### ADR-0007との関係

本ADRはADR-0007のExecution順序と「Deliverable保存は将来追加する」という初期判断を拡張します。ADR-0007のTaskService ownership、ApprovalPolicy、ExecutionPolicy、recovery context、partial failureの原則は変更しません。競合する箇所では本ADRのDeliverable commit orderingを適用します。

## Consequences

- `完了`Taskには、先にcommitされたimmutable Deliverableが存在します。
- Deliverable保存失敗はTaskFailed／必要に応じてTaskHeldとして観測できます。
- Deliverable commit後にTask Completeが失敗した部分状態は残り、呼び出し元が安全に復旧判断できます。
- 保存先を跨ぐ完全なatomicity、retry、idempotency、crash recoveryは提供しません。これらはv0.4のCommand Ledger／Outbox／明示recovery設計へ残します。
- ExecutionService、bootstrap、RuntimeはDeliverable Storeを明示dependencyとしてcompositionする必要があります。
