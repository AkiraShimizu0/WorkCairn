# ADR-0024: Reviewed Workflowは既存Task、Review、Revision commandを決定的に構成する

## Status

Accepted

## Context

ADR-0023のSequential Workflowは依存順に通常Taskを完了できますが、Task完了後のReview、`Request Changes`時のRevision Task作成、Revision後の再Reviewを含みません。既存のReview／Revision processはADR-0010〜0012のcanonical commit point、Event／Audit、partial failureを既に所有しています。Workflowが同じ処理を再実装すると、artifact orderingとTask lifecycle ownershipが二重化します。

Revision TaskはTasks.mdの末尾へ追加されます。本流の未着手Taskが先に存在する場合、ADR-0023の「最初の未着手Task」選択ではRevision Taskを直ちに実行できません。一方、Task表の並べ替え、下流Taskの推測Hold、Task Dependencies projectionの自動書換えは、成立済みProject planを暗黙変更します。

## Decision

### Additive reviewed Workflow command

既存`workflow.execute`の順次実行契約は変更せず、CLIの`workflow-reviewed-plan|workflow-reviewed-execute`とHTTPの`workflow.reviewed.execute`をadditiveに提供します。reviewer employee IDはtyped inputとして必須とし、名前やroleから推測しません。planは次Task、reviewer identity／model、全Task後Review、`Request Changes`時Revisionという条件付き効果をread-onlyで示します。

1回の明示承認は、指定Project、reviewer、時刻、Provider設定、最大Task数に拘束されたreviewed Workflow全体を承認します。承認前はCommand claim、Task変更、Provider、artifact、Eventを開始しません。承認後はouter Command claimを副作用より先にcommitし、既存の各child commandへ承認済みであることを明示して渡します。

### Branch ordering

成功経路は次の既存processを順に構成します。

```text
ExecuteTask
→ ExecuteReview
→ Approve: dependency readinessを再plan
→ Request Changes: ExecuteRevision
→ Revision TaskをExecuteTask
→ Revision TaskをExecuteReview
→ Approveまで同じ分岐を繰り返す
```

ReviewはADR-0010／0011のcanonical JSON commitをReview factとし、RevisionはADR-0012のimmutable intentとTaskService.Createを使います。WorkflowはReview artifact、Revision intent、Task、Event、Auditを直接保存しません。Task状態変更とTask lifecycle Event生成は引き続きTaskServiceだけが所有します。

Review、Revision、Task executionのいずれかがerrorを返した場合、Workflowは停止して完了済みchild resultとcanonical commit状態を`partial_failure`として返します。canonical Review、Revision intent、Task、Deliverable、Event、Auditをrollback、削除、上書きしません。Review JSONがcommit済みでもMarkdownまたはEvent publicationが失敗した場合は、Review factを保持して停止し、次Taskへ進んだ成功として扱いません。

### Revision Task targeted readiness

通常TaskはADR-0023どおり最初の未着手Taskを選択します。Revision orchestrationが同じrun内で返したRevision Taskだけは、targeted readinessを使います。targeted readinessもmanaged Task状態、担当社員ID、社員存在、全dependency graph、対象Taskのdependency完了を検証し、TaskService／ExecutionService／Deliverable orderingを迂回しません。

この選択はRuntimeへ注入するReadinessServiceとして実装し、既存の通常Task／CLI executeの選択規則を変更しません。Revision intentのない任意Taskを飛び越す公開flagは提供しません。Task表の並べ替え、下流TaskのHold、dependency projectionの推測更新は行いません。

### Durable identity and bounds

outer operationは`workflow.reviewed.execute`です。request digestはProject、reviewer ID、注入時刻、approval reference、最大Task数、Provider model／token上限を含み、secret、Vault absolute path、Prompt本文を含みません。

child Command IDはparent Command IDと、`task.execute:<Task ID>`、`review.execute:<Task ID>`、`revision.execute:<source Task ID>`という役割付きidentityから決定的に導出します。同じouter requestのterminal replayは保存済みresultを返し、異payloadは拒否します。outerまたはchildが`running`なら自動resumeせずRecovery境界へ残します。

最大Task数は通常Taskと実行済みRevision Taskの合計で1〜100です。上限到達時にRevision intent／Taskが既にcommit済みなら、そのTaskを明示的なnext actionとして返します。後続の新しい明示承認・新Command IDによるplanは、検証済みimmutable intentと未着手Taskが一致する場合だけ、そのRevision Taskをtargeted nextとして選べます。`metadata_version`を持たないPython legacy metadataは互換APIからは引き続き読めますが、canonical intent commitを証明しないためtargeted executionには採用しません。これは`running` commandの自動resumeや既存artifact adoptionではありません。並列Workflow、Scheduler、自動retry、Event replay、下流dependencyの自動再構成は本ADRに含めません。

## Consequences

- 複数Taskは`Task実行 → Review → 必要ならRevision → 再Review`を完了してから次Taskへ進みます。
- Review／Revisionのcommit ordering、Event／Audit、TaskService ownershipを再実装せず再利用できます。
- 既存Sequential Workflowと`workspace-command.v1`を破壊せず、新operationとして利用できます。
- Revision Taskの優先実行はimmutable Revision結果に限定され、本流Taskの通常選択規則は維持されます。
- crash後の自動継続は提供しません。LedgerとRecoveryから確定済み状態を診断し、人間が次の明示commandを選びます。
