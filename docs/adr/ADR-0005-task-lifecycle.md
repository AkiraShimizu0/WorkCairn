# ADR-0005: Task lifecycleをGo TaskServiceの責務とする

## Status

Accepted

## Context

Task状態は従来Go Project DomainとPython TaskExecutorに分散していました。AI実行、Markdown更新、状態遷移、二重実行防止、失敗処理が同じ実装へ集まると、Worker、Storage、Audit、WorkflowをGoへ移行する際に境界が曖昧になります。

Task状態の更新とEvent Publishは別コンポーネントへの操作です。分散transactionを導入しない初期段階でも、Store成功後にEventだけ失敗した状態を隠さず表現する必要があります。

## Decision

- Task lifecycleの正本を`go/internal/task`へ置きます。既存Project APIは後方互換wrapperとしてTask Domainへ委譲します。
- 正式状態は`未着手`、`進行中`、`保留`、`完了`とし、既存の4遷移を維持します。
- `Fail`は実行試行が失敗した事実を記録し、状態を自動的に`保留`へ変えません。Taskは`進行中`のままversionと最終失敗理由を更新し、`TaskFailed`を発行します。
- `Hold`はPolicyまたは明示操作による`進行中 → 保留`の状態遷移です。`Resume`は`保留 → 未着手`として`TaskResumed`を発行します。
- TaskServiceはWorker、Runner、LLM出力、Audit、Progress、Markdownを知りません。
- 永続化はTaskStore interfaceで分離し、初期AdapterとしてInMemoryTaskStoreを提供します。
- Taskは`Version uint64`を持ち、Store Updateはexpected versionとのcompare-and-setで二重更新を拒否します。
- 状態変更EventはStore成功後にPublishします。Publish失敗時にStoreを巻き戻さず、保存済みTaskとEvent情報を含む型付き`EventPublicationError`を返します。
- 初期版ではOutboxを実装しません。永続Store導入時に、Task更新とOutbox追加を同一transactionへ入れられる境界を維持します。
- Idempotency Keyは初期版へ導入しません。Version/CASは並行更新を防ぎますが、通信retryの同一Command判定は行いません。外部API／Scheduler導入時に永続Command ledgerとして追加します。

## Consequences

- Task状態機械、失敗事実、並行更新の正本がGoへ集約されます。
- TaskService利用者は`EventPublicationError`を部分成功として扱い、同じ状態変更を盲目的に再実行してはいけません。
- InMemoryTaskStoreはprocess-localであり、再起動時の状態保持や複数process間CASを提供しません。
- 将来のObsidian、SQLite、PostgreSQL Adapterは同じTaskStore契約とVersionを実装できます。
- WorkerServiceは`TaskStarted`を受けて実行し、結果に応じてTaskServiceの`Complete`または`Fail`を呼べます。
- AuditはEvent subscriberとして接続し、TaskServiceへ直接依存しません。
