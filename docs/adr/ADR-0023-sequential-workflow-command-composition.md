# ADR-0023: Multi-task Workflowは再planする順次Task commandとして構成する

## Status

Accepted

## Context

単一Task execution、Task dependency readiness、Command Ledger、Recovery、HTTP daemonが成立したため、依存順に複数Taskを自律実行する最小のcompositionへ進めます。一方、durable queue、workflow自動resume、Review／Revision分岐、Schedulerまで同時に導入すると、ADR-0020／0021で延期した推測recoveryを先取りします。

Task lifecycle ownership、各TaskのDeliverable ordering、Provider cancellation、Command replayを再利用し、workflow専用のTask writerやEvent sourceを作らない必要があります。

## Decision

### Sequential orchestration

Workflow runは同一Project内で一度に1 Taskだけを実行します。各Task完了後にmanaged TaskStoreとTask Dependenciesから次のreadinessを再planし、最初のready Taskを既存`ExecuteTask`へ渡します。並列実行、ready Taskの任意選択、blocked Taskの飛び越しは行いません。

Task状態変更とTask lifecycle Eventは引き続きTaskServiceだけが所有します。Workflow Run Serviceは次step、子Command identity、停止条件、partial resultだけを調停します。

### Durable identities

workflow executeは明示Command IDを必須とし、Project scopeへoperation `workflow.execute`のouter claimを副作用前にcommitします。各Task executionには、parent Command IDとTask IDのSHA-256から決定的に導出したchild Command IDを渡します。

- 同じouter ID／requestのterminal replayはworkflow全体の保存済みresultを返す。
- child Task commandも同じID／requestならProviderとTask effectsをreplayしない。
- outerまたはchildが`running`なら自動resumeせずRecoveryで診断する。
- 異なるpayloadでouter IDを再利用した場合は副作用前に拒否する。

### Stop and failure semantics

- 全Task完了: `completed`
- 次Taskがblocked／waiting: `blocked`として正常に停止し、blocking reasonsを返す
- 明示上限へ到達: `limit_reached`として次stepを返す
- Task execution失敗: 完了済み／開始済みchild resultを保持した`partial_failure`
- Ledger outcome失敗: 成立済みTask／Deliverable／Eventをrollbackしない

1 runの上限は1〜100 Taskです。時刻はrequestから注入し、途中でglobal clockを読みません。

### Deferred

- `running` workflowの自動resume、abandon、retry
- workflow run queue／scheduler
- Review結果によるRevision分岐
- Event Outbox、Event replay、artifact adoption
- Task並列実行、priority、resource allocation

## Consequences

- 依存順の通常Task群を1つの承認済みCommandで順次実行できます。
- 各Taskは既存Kernel／ExecutionService／TaskService／Runner／Deliverable／Audit境界をそのまま利用します。
- crash後に二重Provider実行を推測で開始しません。outer／child runningをRecoveryで確認する必要があります。
- Review／Revisionを含む完全な自律loopは次のcomposition段階です。
