# ADR-0004: Event DrivenをWorkspace OSの基本設計とする

## Status

Accepted

## Context

Task、Workflow、Project、Organizationなどの状態変化を各コンポーネント間の直接呼び出しだけで連携すると、Audit、Scheduler、Notification、Metrics、Worker、Pluginを追加するたびに依存関係が増えます。一方、Business Eventを単なるログや永続化形式として扱うと、発生した事実と保存方法が密結合します。

Workspace OSは最終的にGoのみで動作し、Workspace Kernel配下のServiceが会社内で起きた事実を型付きで共有できる基盤を必要とします。

## Decision

Event DrivenをWorkspace OSの基本設計とし、GoのEvent DomainとEventServiceをWorkspace Kernelへ登録します。

- Event Typeは閉じた型として定義し、未知のTypeをPublish前に拒否します。
- 初期Event Busはin-process、synchronous、at-most-onceとします。
- 1回のPublish内ではsubscriberを登録順に呼びます。逐次Publishは呼び出し順を維持しますが、並行Publish間の全順序は保証しません。
- handler失敗時も残りのsubscriberへ1回ずつ配送し、失敗を集約して呼び出し元へ返します。自動retryは行いません。
- Publish開始時にsubscriberのsnapshotを取得します。handler中のSubscribe／Unsubscribeは次のEventから反映し、ネストしたPublishは許容します。
- Domain Eventと外部Adapterを分離します。Event DomainとEventServiceはAudit Markdown、Obsidian、外部queue、DBを知りません。
- 永続化はEventServiceの責務に含めません。将来はAudit、Scheduler、Notification、Metricsなどがsubscriberとして接続します。
- Kernel StartでEventServiceを利用可能にし、Kernel Stop後の新規Publishを拒否します。

## Consequences

- TaskServiceやWorkflowServiceは状態変化後にEventServiceへ型付きEventをPublishでき、下流機能との直接依存を避けられます。
- 初期版はプロセス終了時にEventを失い、再配送、永続queue、global orderingを提供しません。
- handler失敗は自動retryされないため、必要なsubscriberは将来Adapter側で冪等性、retry、dead-letter、永続化を設計する必要があります。
- 将来async／durable transportへ置き換える際も、Event DomainとKernelのEventService境界を維持できます。
- AuditはEvent Busのsubscriberとして実装し、EventServiceそのものをログ保存機能にしません。
