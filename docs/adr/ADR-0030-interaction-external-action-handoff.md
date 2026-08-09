# ADR-0030: Interactionは明示Deliverableを既存External Actionへ引き渡す

## Status

Accepted

## Context

ADR-0029でInteraction SessionはReviewed Workflow完了まで到達できます。既存ADR-0027のWordPress Actionは、Deliverable digest承認、immutable request／result evidence、remote publication、Event／Audit、partial failureを既に所有します。

Workflow完了後の自然な次手として外部公開を扱う一方、自然言語依頼やDeliverable内容から「公開すべき」と推測して自動実行してはいけません。InteractionへWordPress transportやcredentialを入れず、既存Actionのcommit orderingを再利用する必要があります。

## Decision

### Explicit handoff only

Action不要のSessionは`completed`で終了します。Action handoffは`completed` Sessionに対して、利用者が明示したTask ID、logical target ID、prospective outer Command ID、expected Session Version、時刻だけを受けます。

Task IDは同じSessionのcompleted Reviewed Workflow evidenceに含まれるTaskだけを許可します。名前、最新Deliverable、最終Task、自然言語から対象を推測しません。

read-only `interaction-action-wordpress-plan`／HTTP planは、outer Command IDからproject-scoped Action child Command IDを決定的に導出し、既存`PlanExternalAction`でDeliverableを読みます。Session／Project／Task／target／時刻／child identity／source SHA-256／既存Action planをcanonical `action_plan_digest`へ固定します。

### Commit ordering

```text
explicit approval of action_plan_digest
→ workspace-scoped interaction.action.wordpress.publish claim
→ project-scoped action.wordpress.publish child Command
→ existing immutable intent / WordPress / outcome / action.completed flow
→ append bounded Action evidence to Session with expected Version CAS
→ terminal outer Command outcome
```

InteractionはAction Service、Store、Publisher、Event、Auditを再実装しません。credential、Base URL、username、application passwordはRuntime edgeだけから既存WordPress Adapterへ渡し、Session、plan、HTTP Command、Ledger requestへ含めません。

同一outer Command ID／requestのterminal replayはSessionが終端後でも保存済みResultを返します。異request、`running`、stale Session Version、source digest変更は既存Ledger／plan境界で拒否します。

### Session evidence and failure

Sessionには完全Action Resultのdigest、outer／child Command ID、Project／Task／target、source digest、immutable evidence reference、allow-list済みpublication、Event publication、typed failureだけをappendします。Deliverable本文、Provider生response、credentialは保存しません。

- Action `published`: Sessionを`action_completed`へ進める。
- Action `failed`／`partial_failure`: evidenceを可能な限りcommitし、`action_attention_required`で停止する。
- Action成功後のSession CAS失敗: remote post、intent、outcome、Event／Audit、child Ledgerをrollback・削除せずouter partial failureとする。
- Sessionへfailure evidenceを保存できてもAction errorを成功に丸めない。

同じSessionからの複数Action、automatic approval、content変換、remote adoption／reconciliation、retry／deleteは含めません。

## Consequences

- 自然言語依頼からReviewed Workflow完了後、必要な場合だけ既存Deliverableを別承認でWordPressへ公開できます。
- 公開対象とsource digestは明示され、Interactionは外部副作用を推測しません。
- ADR-0027のimmutable evidenceとpartial publication semanticsをそのまま維持します。
- Action不要の通常Sessionに追加副作用や追加状態変更はありません。
