# ADR-0020: 確定証拠に拘束された診断と明示Recoveryを先行する

## Status

Accepted

## Context

Workspace OSは、単一ファイルのatomic operationと、複数commit pointを明示するorderingで成立済みfactを保護しています。一方、process停止や後続I/O失敗により、Deliverable commit後かつTask Complete前、Review JSON後かつMarkdown前、Revision intent後かつTask作成前、Task保存後かつEvent／Audit前などのpartial stateが残り得ます。

これらを一律にretry、rollback、adoptすると、Provider結果やEvent deliveryのようにVaultだけから再構成できない情報を推測することになります。既存のcanonical evidenceを削除・上書きする危険もあります。Command Ledger、Transactional Outbox、汎用idempotencyを導入する前に、現在のcommit pointを正確に観測し、安全性を証明できる操作だけを人間の明示承認で実行できる基盤が必要です。

## Decision

### Read-only inventory

Project単位のRecovery Snapshot Adapterが、managed `Tasks.md`、Deliverable、Review JSON／Markdown、Revision intent、Audit Event envelope、既知のtemporary／staging名を構造化証拠へ変換します。Domain／ServiceはVault path、Markdown parser、transportを知りません。

`recovery-inspect`は状態を書き換えません。findingは少なくともcertainty、severity、参照、recoverable、推奨actionを持ちます。Vaultから確定できる状態と、原因を区別できない状態を分離します。

- `confirmed`: commit済みartifactやTask状態の組合せから確定できる。
- `unverifiable`: 期待するAuditがない等、Event未発行とsubscriber失敗を区別できない。

`unverifiable`な状態にEvent replayやartifact生成を提案しません。

### Versioned recovery plan

Recovery plan schema v1はProject、Task ID、action、期待Task status／Version、証拠参照／SHA-256、理由、source revision、blocking reason、approval requirementを保持します。planはread-only snapshotから決定的に生成します。

apply直前に同じ要求からplanを再生成し、承認済みplanと完全一致することを要求します。Task更新時も期待VersionをTaskServiceへ渡し、Vault TaskStoreのCASで検証します。証拠、Task Version、理由のいずれかが変化した場合はstaleとして拒否し、最新状態を推測して適用しません。

### Foundationで許可する操作

初期版で実行可能にするのは、次の2操作だけです。

1. `complete_task`: Taskが進行中で、同一Project、Task ID、担当社員IDに一致するvalidかつ唯一のimmutable Deliverableが存在する場合、TaskService.Completeを期待Version付きで実行する。
2. `fail_and_hold_task`: Taskが進行中でDeliverableが存在せず、人間がfailure reasonを明示した場合、TaskService.Fail、TaskService.Holdを期待Version付きで順に実行する。

Task状態変更とTask lifecycle Event生成の所有者は引き続きTaskServiceだけです。Recovery ServiceはTask Storeを直接更新せず、Auditも直接書きません。process compositionが既存EventServiceへAudit subscriberを接続します。

Deliverable commit後のCompleteでEvent／Audit publicationが失敗した場合、Task完了は成立済みpartial failureとして返します。fail後のpublication failureでもTask失敗がcommit済みなら、明示planで予定したHoldを続行し、欠けたpublicationをpartial failureとして返します。成立済みTaskやartifactをrollbackしません。

### 診断のみとする状態

- Review JSONだけから、Providerが生成した元Markdown本文を再構成しない。
- Revision intentだけがある場合、Task ID予約や既存intent adoptionを推測してTaskを作らない。
- Audit欠落時、元Event IDやpublication成否を推測してEventを再発行しない。
- residual temporary／stagingはactive process所有かを判定できないため、自動削除しない。
- completed TaskのDeliverable欠落、artifact identity不整合、破損・重複は拒否し、生成や関連付けを推測しない。
- Organization canonical／projection不整合は既存の`organization-sync-plan`／明示executeを利用し、本Recovery actionへ重複実装しない。

### 延期する事項

自動retry、自動rollback、既存artifact adoption、Event replay、crash reconciliation、Command Ledger、汎用idempotency、Transactional Outboxは本foundationへ含めません。これらはRecovery inventoryとcommit pointを入力に、別ADRで段階的に設計します。

## Consequences

- 再起動後にVaultだけから何がcommit済みかを同じtyped reportで確認できます。
- 安全性を証明できるTask partial stateは、read-only planと明示承認を経て回復できます。
- plan後の並行変更はsource revisionとCASの両方で拒否されます。
- Review projection、Revision intent、Event／Auditの一部は引き続き診断のみです。これは成功扱いではなく、証拠不足を明示する安全な制約です。
- Recovery command自体が途中停止した場合も自動で成功へ丸めず、再inspectして新しい証拠から判断します。
- 将来のLedger／Outboxは、本ADRのcanonical commit pointやTaskService ownershipを変更せず追加する必要があります。
