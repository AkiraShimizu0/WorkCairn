# ADR-0012: Revision intentをTask作成より先にcommitする

## Status

Accepted

## Context

Request Changesから修正Taskを作る際、Revision metadata、TaskStore、Event／Auditは別commitです。Taskを先に作るとmetadata失敗時に出所を追跡できないTaskが残ります。metadataを後からrollbackする方式は、TaskServiceが既に発行したTask lifecycle Eventと整合せず、ADR-0005の所有権を破壊します。

## Decision

Revision metadataをimmutable intentとして先にcommitし、次の順序をRevision orchestration serviceが調停します。

```text
RevisionIntentStore.Save
→ TaskService.Create
→ EventService.Publish revision.created
→ Audit subscriber
```

intentは元Task、canonical Review JSON、Request Changesのissues、元担当社員ID、予約したRevision Task IDとtitleを保持します。Task作成成功を偽装しないため`state: intent_committed`とします。`metadata_version: 1`でschemaを明示し、`source_review_canonical`をGoの正本参照、既存Python duplicate readerが理解する`source_review`／`source_review_path`をMarkdown projection互換参照とします。Taskの正式な存在と状態はADR-0008 Tasks.mdとTaskServiceが正本です。

- intent未commitならTaskを作成せず、Revision Eventも発行しません。
- intent commit後のTask作成失敗はintentを保持するpartial failureです。
- TaskStore commit後のTaskCreated Event失敗はTaskServiceのpartial publication failureとして保持します。
- Taskが保存済みなら`revision.created`を発行し、Audit subscriberへ配送します。
- Revision Event失敗でもintentとTaskをrollback、削除しません。
- Task状態変更とTask lifecycle Eventの所有者はTaskServiceだけです。
- Revision StoreはTask、Event、Auditを知りません。

intentは`Revisions/<Revision Task ID>.revision.md`へtemporary fileのwrite、sync、排他的な原子的publish、directory syncの順で作成し、既存targetを上書きしません。publish後のcleanup／directory sync失敗は`committed=true`のpartial failureです。process crash後のintentだけ、intent＋Taskだけという状態は証拠として保持し、自動rollbackしません。

TaskService.CreateがTaskStoreへcommitした後に`task.created`の配送だけ失敗した場合、Revision orchestrationはTaskが成立した部分状態として扱い、`revision.created`の発行を試みます。それ以外のTaskService errorでTask commitを推測しません。`revision.created`とTask lifecycle Eventは別のclosed typed Eventで、Auditは両方のsubscriberとしてのみ接続します。

同じsource Review、同じRevision Task ID、既存Taskとの衝突は推測、adopt、上書きせず拒否します。自動retry、ID再採番、reconciliation、idempotency、Command Ledger、Outboxはv0.4へ延期します。

## Consequences

- Task作成に失敗しても、何を作ろうとしたかがimmutable evidenceとして残ります。
- intentだけ、intent＋Task、Event配送済みまでの部分状態を明示的に区別できます。
- Python legacy RevisionTaskServiceはintent metadataをsource Review重複として読めますが、managed Tasks.mdのwriterには使いません。
- Python削除後はRevision Store port、commit ordering、TaskService所有権を維持する限り、Vault metadata形式を別schemaへ移行できます。
