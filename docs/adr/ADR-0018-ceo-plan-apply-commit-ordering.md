# ADR-0018: CEO plan applyはProject、Task、Dependency projectionを順次commitする

## Status

Accepted

## Context

CEOCommandServiceのplanは副作用を持たない一方、legacy applyはPython ProjectManagerでProjectとTaskを作成し、最後にTask Dependencies.mdを書きます。途中失敗時には新Project全体を削除してrollbackします。しかしGo Project bootstrapはADR-0013のcanonical directory commit、Task作成はTaskServiceとTask lifecycle Eventを所有しており、commit済みの事実をPythonから削除することは境界違反です。

## Decision

Go managed applyのcommit順序は次の通りです。

```text
ProjectStore.Bootstrap
→ TaskService.Createをplan順に実行
→ immutable Task Dependencies.md projectionを作成
```

全段階で明示承認を要求します。Task Dependencies.mdは、確定したTask ID、計画内Proposed ID、依存Task ID、rationaleの人間可読projectionです。作成前に全Taskの存在、ID一意性、自己依存、未知依存、循環を検証し、既存fileを上書きしません。

Projectまたは一部Taskのcommit後に後続段階が失敗した場合はpartial failureです。commit済みProject、Task、Event、AuditをPython orchestrationから削除・rollbackしません。成立済みTask件数とDependency projection未commitを観測可能にします。legacy ProjectManagerを明示注入した公開互換経路だけは従来挙動を維持します。

CEO plan生成、Project IDの割当、Go apply processは[ADR-0019](ADR-0019-ceo-plan-generation-and-cutover.md)で拡張します。retry、idempotency、reconciliation、crash recoveryは引き続きv0.4 durabilityへ延期します。

## Consequences

- CEO applyからPython Project／Task／Dependency writerを外せます。
- Task状態変更とTask lifecycle Eventの所有者はTaskServiceのままです。
- Go managed applyは「全部rollbackされた」という不正確な見かけを作らず、partial commitを明示します。
- Python CEO plannerとlegacy applyは公開互換referenceとしてだけ残り、通常製品経路ではADR-0019のGo process gatewayを使用します。
