# ADR-0029: Interactionは既存Reviewed Workflowを決定的child Commandとして実行する

## Status

Accepted

## Context

ADR-0028のInteraction Sessionは自然言語依頼、質問回答、plan承認、Project／Task適用までを継続状態として保持します。適用後の`ready_to_execute`からは、ADR-0024のReviewed Workflowが既にTask実行、Review、Request Changes時のRevision、Revision Task実行、再Reviewを安全に提供しています。

Interactionへ同じ分岐を再実装すると、TaskService ownership、Review／Revisionのcommit ordering、child Command identity、partial failure semanticsが二重化します。一方、Reviewed Workflowの完全ResultにはDeliverable本文も含まれるため、Sessionへそのまま複製するとcoordination evidenceがcanonical artifactの重複保存になります。

## Decision

### Read-only planと承認対象

`interaction-workflow-plan`とread-only HTTP planは、expected Session Version、適用済みProject identity、reviewer ID、注入時刻、1〜100のTask上限、次step、reviewer identity／modelを検証し、そのcanonical SHA-256を`workflow_plan_digest`として返します。

executeは明示承認、同じdigest、Session Version、reviewer ID、Task上限、Command IDを必須とします。承認前はCommand claim、Provider、Task、Review、Revision、Session更新を開始しません。Provider credential、Base URL、Vault pathはplan、Session、Command payloadへ含めません。

### Outer／child Command composition

commit orderingは次とします。

```text
explicit approval of workflow_plan_digest
→ workspace-scoped interaction.workflow.execute claim
→ derive project-scoped workflow.reviewed.execute child Command ID
→ existing ExecuteReviewedWorkflow
→ append Session workflow evidence with expected Version CAS
→ terminal outer Command outcome
```

child Command IDはouter Command IDとSession IDから決定的に導出します。既存Reviewed Workflowが、その配下のTask／Review／Revision child Command ID、TaskService、Review Store、Revision intent、Deliverable、Event／Auditを引き続き所有します。InteractionはTask状態、Task lifecycle Event、Review artifact、Revision intentを直接変更しません。

同じouter ID／同じrequestのterminal replayはSessionが既に終端していても保存済みResultを返します。異なるrequestでのID再利用と`running`はADR-0021どおり拒否し、自動resumeしません。

### Session evidenceと状態

Sessionへは完全Result本文を複製せず、次のbounded typed evidenceをappendします。

- outer／Reviewed Workflow Command ID
- Project／reviewer identity、Task上限
- Workflow statusと完全Resultのcanonical SHA-256
- 各TaskのTask ID、targeted Revision識別、Task／Review／Revision child Command ID、verdict、作成されたRevision Task ID
- next action／blocking reason
- failure code、stage、partial識別

完全Resultの正本はproject-scoped Reviewed Workflow Command Ledger、Deliverable／Review／Revisionの正本は各canonical Storeです。Session evidenceはそれらを推測でadoptせず、結果を特定するcoordination evidenceです。

- `completed`: Sessionを`completed`へ進める。
- `blocked`／`limit_reached`: evidenceをappendし、`ready_to_execute`を維持する。新しいplan、明示承認、新しいCommand IDだけで継続できる。
- `failed`／`partial_failure`: evidenceをappendし、`workflow_attention_required`で停止する。Sessionから自動再実行せず、Ledger／Recovery／canonical evidenceを確認する。

### Partial failure

Reviewed Workflowのcanonical効果はSession CASより先に成立します。Workflow成功後のSession保存失敗、またはWorkflow partial failure後のevidence保存失敗では、Task、Deliverable、Review、Revision、Event、Audit、child Ledgerをrollback・削除・上書きしません。outer Commandはpartial failureとして、Workflow Resultと`session_committed`を返します。

Sessionへfailure evidenceをcommitできた場合も、Workflow errorを成功に丸めません。Session CAS競合時は既存turnを上書きせず拒否します。自動retry、artifact adoption、Event replay、Session reconciliation、Scheduler dispatchは本ADRに含めません。

## Consequences

- 自然言語依頼からProject／Task適用後、`Task実行 → Review → Request ChangesならRevision → 再Review → 次Task`を同じSessionから承認して実行できます。
- 既存Reviewed Workflowと各canonical commit pointを再利用し、Task／Event ownershipを増やしません。
- Sessionはboundedな監査可能summaryに留まり、大きなDeliverable本文や秘密情報を複製しません。
- blocked／limitは明示的に継続できますが、partial／failed Sessionは人間のRecovery判断なしに再開しません。
- External ActionをSessionから開始するcompositionはADR-0030で追加済みです。automatic approval、parallel Workflow、automatic resumeは後続です。
