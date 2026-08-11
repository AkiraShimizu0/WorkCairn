# ADR-0035: Autonomy Contractを承認範囲に固定しProof of Workをcanonical evidenceから投影する

## Status

Accepted

## Context

WorkCairnは承認、Execution Policy、Command Ledger、Reviewed Workflow、Recoveryによって安全に処理できます。しかし利用者から見ると「承認後にどこまで会社へ任せたか」と「何が実際に成立したか」が一つの分かりやすい形になっていません。新しいpolicy engineや実績databaseを作ると、既存の安全境界と二重のsource of truthになります。

Public Betaでは広い権限設定機能より、AIが勝手に動くのではなく、利用者が理解して任せた範囲だけを既存Serviceが実行することを示す必要があります。またCEO Attentionはprocess-local counterではなく、現在残っている確定証拠だけから安全に説明できなければなりません。

## Decision

### Autonomy Contract

`workcairn-autonomy.v1`をProvider／Vault非依存のtyped valueとして追加します。これはWorkflow実行を許可する新しいengineではなく、既存の明示Workflow承認が対象とする範囲を人間向けに表す薄いcontractです。Public Betaの標準contractは次へ固定します。

- Task executionは承認されたWorkflow内で委任する
- 各成果物のReviewは必須とする
- Request Changes後のRevisionは同じWorkflow内で委任する
- External publishはWorkflow外の別承認とする
- spendingは許可しない
- 参加可能なEmployee ID、論理model値、実行Task上限をallow-listとして固定する

Workflow plan時に現在の未完了Task担当、Reviewer、Employee inventoryのmodel値から標準contractを決定的に構築します。callerがcontractを明示する場合も同じ固定安全条件、実際の担当／Reviewer／model、`MaxTasks`との一致を検証します。表示名ではなくEmployee IDをidentityとし、配列は重複除去して辞書順へcanonicalizeします。

contractはWorkflow plan digestへ含め、承認後はInteraction Workflow evidenceへ保存します。execute requestも承認した同じcontractを含めるため、範囲変更は同じCommand IDの異なるrequestとして拒否されます。これによりcontractは既存Command Ledger、approval、Workflow Serviceを置き換えず、その上に拘束されます。

現在の契約より自律範囲を広げる任意設定、支出上限、Review省略は実装しません。Task状態とTask lifecycle Eventは引き続きTaskServiceだけが変更します。

### Proof of Work

Work Reportは新しいStoreを持たないread-only projectionとします。Interaction Workflow／Action evidenceを入口に、現在のTask、Deliverable、canonical Review JSON、Revision intent、Command Ledgerを再読込し、担当、成果物、Reviewer、Request Changes、Revision、承認、External Action成立を表示します。

欠落、partial failure、未完了Commandを成功と推測しません。canonical evidenceが不足するTaskは`verified=false`とし、読取不能な破損はinspection failureとして返します。保存済みartifactを修復、adopt、rollback、再実行しません。AuditはEvent subscriberのprojectionでありTaskのcanonical commit pointを変更しませんが、Work Report全体を`fully_verified`と表示するにはAuditがreadableで記録を含むことも確認します。Event／Audit publication failureは既存Command Ledgerのterminal outcomeとInteraction partial evidenceを通じて表示します。

### CEO Attention

CEO Attentionは同じWork Reportから算出するSession単位のread modelです。会社が処理したstep、承認後に人間を呼ばず処理したTask／Review／Revision step、clarification数、承認moment数、Recovery attention数を数えます。process-local metricsや恒久KPI Storeではなく、再起動後も再構成できる既存evidenceだけを使います。証拠がない「user action不要の時間」は推測しないため、今回は計測しません。

### UI boundary

My Actionsは既存Next Actionだけを表示し続け、policy判断を持ちません。Workflow承認時にcontractの要点を短く確認し、Company ViewはWork Reportを「任せている範囲」「実際に完了したこと」「呼ばずに進めたstep」へ翻訳します。Command IDやCAS等の技術語を通常表示へ持ち込まず、Recovery時だけ成立済み部分と未確認部分を明示します。

### Shadow ModeとEmployee Authority

Shadow ModeはPublic Betaでは実装しません。将来はExternal Action intentへ実行modeを明示し、承認・Command claim・dry-run outcome evidenceを先に確定してから、`shadow`ではAdapterを呼ばず、`live`だけ既存Adapterを呼ぶ形で追加できます。modeを推測せず、現在のExternal Actionは常に別承認のlive actionです。

Employee Authorityも新しい永続policy sourceを今回追加しません。将来の実効許可は、platform safety floor ∩ Workflow Autonomy Contract ∩ Employee Authorityのintersectionとし、未知の権限はdefault denyとします。Company／Workflow側の許可だけでEmployee固有の禁止を上書きできない方向を維持します。

## Consequences

- 利用者はWorkflow承認時に「誰へ、どこまで、何を任せるか」を確認できます。
- 完了表示は既存canonical evidenceへ拘束され、AIの自己申告やUI local stateをsource of truthにしません。
- CEO Attentionは再起動後も同じSession evidenceから決定的に再構成できます。
- `workspace-command.v1`と`workspace-interaction.v1`のversionは維持し、optional request fieldとInteraction evidenceのadditive field、read-only endpointだけを追加します。
- Provider transport、Vault path、秘密情報はContractとUIへ入りません。論理model allow-listはEmployee inventory由来であり、RuntimeのProvider model routingとは分離します。
- Shadow Mode、Employee Authority、支出上限、長期KPI、automatic reconciliationは将来の独立判断として残ります。
