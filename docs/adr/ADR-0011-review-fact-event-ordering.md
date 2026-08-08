# ADR-0011: Review factをcanonical JSON commit後にEventとして発行する

## Status

Accepted

## Context

ADR-0010は構造化Review JSONをimmutable canonical evidence、Markdownをhuman-readable projectionと定義しました。Go Review processを製品経路にした後も、Python ReviewerWorkerが記録していたReview Auditに相当する事実をEvent Drivenな境界で保存する必要があります。

ReviewService、Review Store、processのいずれかへAudit書込みを直接追加すると、Provider実行、artifact commit、Event配送、Vault形式が再び密結合します。また、JSON commit後にMarkdownまたはEvent配送が失敗する部分状態を、Review fact自体の不成立として扱うとADR-0010のcanonical evidence定義と矛盾します。

## Decision

専用Review orchestration serviceが次の順序だけを調停します。

```text
ReviewService.Execute
→ ReviewStore.Save
→ EventService.Publish review.completed
→ Audit subscriber
```

ReviewServiceはPrompt、Runner、構造化結果検証だけを担当し、artifact、Event、Auditを知りません。Review StoreはJSON／Markdown保存だけを担当し、Event、Audit、Task状態を知りません。AuditはEvent subscriberとしてのみ接続します。

### Review factのcommit point

canonical JSONのcommit成功をReview factのcommit pointとします。

- JSONが未commitなら`review.completed`を発行しません。
- JSONがcommit済みなら、Markdown projectionが失敗していてもReview factは成立しており、`review.completed`を発行します。
- Event payloadはcanonical path、projection pathとそのcommit状態、Task ID、reviewer ID、verdict、review versionを含みます。
- Task lifecycle Eventとは分離し、初期実装では既存closed type `review.completed`だけを使用します。requested、failed、projection-specific Eventは先取りして増やしません。

### Partial failure

Markdown失敗はcanonical artifactとEvent factを保持したpartial failureです。Event publication失敗もcanonical artifactを保持したpartial publication failureです。両方が失敗した場合もそれぞれの原因とcommit状態を失わず返します。

保存済みJSON、公開済みMarkdown、配送済みEvent／Auditをrollback、削除、上書きしません。Task状態は変更しません。Task lifecycle Eventの唯一の所有者は引き続きTaskServiceです。

### Deferred work

automatic retry、既存artifact adoption、idempotency、reconciliation、Event再配送、Command Ledger、Outboxはv0.4へ延期します。同期in-process Event配送とAudit保存のatomicityは提供しません。

## Consequences

- Review factとAuditはcanonical evidenceのcommit後にだけ成立します。
- Markdown projectionの可用性とReview factの成立を区別できます。
- Event subscriber失敗を成功扱いせず、artifactを消さないpartial publication failureとして観測できます。
- Review orchestration service、Runtime、processは型付きpartial Resultを伝播する必要があります。
