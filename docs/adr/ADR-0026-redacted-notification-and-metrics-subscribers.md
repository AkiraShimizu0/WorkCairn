# ADR-0026: NotificationとMetricsをredacted Event subscriberとして接続する

## Status

Accepted

## Context

ADR-0004／0011／0012により、Task lifecycle、Review、Revisionの成立済みfactはclosed typed Eventとして同期Event Busへ発行され、Auditはsubscriberとして保存されます。ADR-0022／0025のdaemonとSchedulerにより、ユーザーがrequestを待っていない間にも承認済みCommandが実行されるため、結果を安全に観測する入口が必要です。

一方、Event payloadにはTask title、社員ID、Review artifact path等が含まれます。Prompt、Provider response、API key、自由文章を通知やMetricsへ複製すると、canonical evidenceではない観測データが新しい秘密情報・個人情報の保存面になります。また、この段階で外部通知Provider、durable delivery queue、Outbox、Event replayを導入すると、配送保証とbusiness commit orderingを不必要に結合します。

## Decision

### Existing Event fact is the only source

NotificationとMetricsは既存Event ServiceへRuntime edgeから追加するsubscriberです。Task／Review／Revision Serviceは通知形式、Metrics形式、Vault path、HTTPを知りません。Auditを既存順序で先に登録し、その後にNotification、Metricsを登録します。

対象は現在daemonから発行されるTask lifecycle、`review.completed`、`revision.created`です。新しい観測目的だけのbusiness Event typeは追加しません。Command Ledger replayはEventを再発行しないため、NotificationとMetricsも増えません。

### Redacted immutable Notification Inbox

Notification recordは`workspace-notification.v1`とし、Event ID、closed Event type、UTC発生時刻、aggregate type／ID、任意のcorrelation／causation IDだけを保持します。Event payloadとmetadataは読み替え・要約・保存しません。Prompt、Task title、Review本文、Provider情報、API keyはNotificationへ含めません。

Vault AdapterはrecordをVault直下`.workspace-os/notifications/<Event ID SHA-256>.json`へimmutable atomic createします。Event IDをpathへ直接使わず、既存recordを上書き・adoptしません。欠落、破損、unexpected entry、filename／record不整合は推測修復せずinspectionを拒否します。HTTPはlistとEvent ID指定のread-only inspectionだけを公開し、既読状態、削除、外部配送状態は持ちません。

### Bounded in-memory Metrics

Metrics subscriberは`workspace-metrics.v1` snapshotとして、process開始後の総Event数、closed Event type別件数、最後に観測したEvent時刻だけを保持します。Event ID、aggregate ID、payload、metadata、Prompt、token本文、個人情報は保持しません。状態はboundedかつin-memoryであり、daemon再起動時にresetされます。

HTTP `/v1/metrics`はこのprocess-local snapshotをread-onlyで返します。永続時系列DB、Prometheus dependency、histogram、token／duration集計は、redaction可能なtyped Eventが必要になった時点まで追加しません。

### Failure and ordering semantics

business factのcommit orderingは変更しません。

```text
canonical business commit
→ business Event publication
    → Audit subscriber
    → Notification subscriber
    → Metrics subscriber
```

Notification保存失敗はEvent Busのtyped delivery errorとして既存processへ返り、commit済みTask／Review JSON／Revision intent／Task／Auditをrollbackしません。結果は既存のpartial publication failureです。Event Busは1 subscriber失敗後も後続subscriberを実行するため、Notification失敗がMetrics deliveryを抑止しません。

Metrics handlerはvalidation済みEventからcounterだけを同期更新し、I/Oを持ちません。NotificationもMetricsもTask状態を変更せず、EventやAuditを再発行しません。crash後の通知再構成、Event replay、external delivery retry、acknowledgement、Outboxは導入しません。

## Consequences

- daemon利用者は自律実行のbusiness Eventをpayload-free Inboxとcounterから確認できます。
- Notification／MetricsはKernel、Domain、TaskService、ReviewService、RevisionServiceの責務を侵食しません。
- immutable Notification failureは成立済みfactを保持したpartial failureとして観測されます。
- Notificationはcanonical evidenceではなくredacted projectionです。完全な内容はTask／Review／Revision／Auditの既存canonical evidenceを参照します。
- process再起動をまたぐMetrics、外部channel配送、未読管理、Event replay、Transactional Outboxは将来要件です。
