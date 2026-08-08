# ADR-0007: Workflow executionとPolicyをTask lifecycleから分離する

## Status

Accepted

## Context

Python TaskExecutorは明示承認、Task開始、Worker実行、成功・失敗処理、保留判断、成果物保存を一つの実装で扱っています。この構造をそのままGoへ移すと、Task状態機械、AI実行、承認、失敗回復、Markdown Adapterが再び密結合します。また、Worker timeoutやEvent配信だけが失敗した部分成功を、最終Task状態と分けて表現する必要があります。

## Decision

- readiness判定を担う既存WorkflowServiceとは別に、1件のTaskを調停するExecutionServiceをKernelへ登録します。
- ExecutionServiceは`readiness → ApprovalPolicy → TaskService.Start → WorkerService.Execute → DeliverableStore.Save → TaskService.Complete/Fail → ExecutionPolicy → TaskService.Hold`の順序だけを所有します。Deliverable commit orderingの詳細はADR-0009で拡張します。
- Task状態は必ずTaskService経由で変更し、Task lifecycle Eventの発行元もTaskServiceだけとします。ExecutionServiceはTask Eventを二重発行しません。
- ApprovalPolicyを独立portとし、初期実装は明示承認が存在し、かつGrantedの場合だけ承認します。将来は会社設定や人間承認を同じportへ接続します。
- ExecutionPolicyを独立portとし、初期実装はWorker失敗をTaskService.Failで記録した後、TaskService.Holdを要求します。Failという事実とHoldという回復判断を分離します。
- WorkerServiceは承認、Task状態、retry、Hold Policyを知りません。ExecutionServiceもRunnerやProviderを知りません。
- `context.Context`はreadiness前後で確認し、ApprovalPolicy、TaskService、WorkerServiceへ伝播します。Worker実行中にcaller contextがtimeout/cancelされた後のFail/Hold記録だけは、元contextの値を維持しcancelを切り離した5秒上限のrecovery contextで行います。
- 各段階の失敗はStage、ErrorKind、部分Resultを持つ型付きExecutionErrorで返します。Task Store更新後のEvent配信失敗は`EVENT_PUBLICATION_PARTIAL`として保存済み状態を明示します。
- 初期版では除外していたDeliverable保存をADR-0009で追加します。retry、backoff、alternate model、Command Ledger、Workflow固有Event、Audit直接書き込みは引き続き実装しません。Execution ID、Command ID、Idempotency Keyは将来用optional fieldとして契約に確保します。
- 通常Taskの製品実行はGo `workspace-run`を正本とし、Python WorkflowEngineは明示的なGo process gatewayだけを呼びます。Python TaskExecutorは製品callerから外し、公開API互換とreference testに限定して残します。暗黙のPython fallbackは追加しません。

## Consequences

- 承認がなければTask Start、Worker、Eventのいずれも実行されません。
- 同一Taskの並行実行はTaskServiceのVersion/CASで1件だけStartに成功します。ExecutionService独自のlockやファイルlockは不要です。
- 成功Event順序はTaskStarted、TaskCompleted、失敗Event順序はTaskStarted、TaskFailed、TaskHeldになります。
- timeout/cancel後も失敗事実とHoldを記録できますが、recovery処理自体が5秒を超えた場合は部分失敗として呼び出し元が扱う必要があります。
- WorkerResult contentはADR-0009に従い、Task Completeより先にDeliverable Adapterへ保存します。保存済み成果物はComplete失敗時も削除しません。
