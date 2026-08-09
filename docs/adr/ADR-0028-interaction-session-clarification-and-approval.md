# ADR-0028: Interaction Sessionは質問回答と承認対象digestをappend-only turnで保持する

## Status

Accepted

## Context

Go Only Runtimeは自然言語CEO依頼からtyped plan、Project／Task、Reviewed Workflow、External Actionまで実行できます。しかし各CLI／HTTP commandは独立しており、利用者は「次に質問へ答えるのか、planを承認するのか、Recoveryを確認するのか」を自分で組み立てる必要があります。

既存CEO Planには`ceo_questions`がありますが、公開互換の直接`ceo-plan-apply`は質問の有無をblocking stateとして扱いません。この既存operationの意味を破壊せず、新しい製品体験では未回答質問を残したままWorkspaceへ適用しない継続状態が必要です。一方、汎用chat history、automatic resume、parallel agent、long-running queueを同時に導入すると、現在のApproval、Ledger、Recovery境界を先取りします。

## Decision

### Bounded typed session

`workspace-interaction.v1`はsingle-user、single active stepのInteraction Sessionです。closed stateは次の4つだけから開始します。

- `plan_generation_approval_required`
- `clarification_required`
- `plan_approval_required`
- `ready_to_execute`

Sessionは自然言語request、logical model、作成時刻、request digestをimmutable headerとして持ち、Provider plan、CEO回答、適用済みProjectをappend-only turnで保持します。turn削除、並替え、過去plan／回答の上書きを禁止し、Versionは1 turnにつき1増加します。

`ready_to_execute`以後のReviewed Workflow結果、追加state、outer／child Command orderingは[ADR-0029](ADR-0029-interaction-reviewed-workflow-composition.md)で本ADRを拡張します。

Vault AdapterはVault直下`.workspace-os/interactions/<Session ID SHA-256>.json`へstrict JSONを保存します。createはatomic create、turn追加はfile lock、expected Version、append-only prefix検証、atomic replacementを使います。Session IDをpathへ直接使いません。unknown field、未知schema、破損、history rewrite、stale Versionを拒否し、自動修復しません。

SessionはTask、Project、Reviewの正本ではありません。request／clarification／approvalを既存commandへ接続するcoordination evidenceです。Task状態とTask lifecycle Eventは引き続きTaskServiceだけが変更・生成します。

### Clarification and approval binding

read-onlyな`interaction-start-plan`／HTTP `interaction-plans`は、Session ID、request、model、時刻のcanonical SHA-256を表示します。`interaction.start`は明示承認された同じdigestだけをcommitします。

plan生成はSession expected Versionへの明示承認後だけ、workspace-scoped Command Ledgerをclaimして既存CEO Plan Service／Claude Adapterを呼びます。logical modelに加えProvider model／max tokensをrequest digestへ含め、API key、Base URL、Vault pathは含めません。validated planをSessionへcommitしてからcommandをterminalにします。

`ceo_questions`が1件でもあればstateは`clarification_required`です。全質問へのtyped回答をexact question identityで1回ずつ受け、plan内の質問順へcanonicalizeしてappendします。その後は新しい承認・新しいCommand IDで再planし、質問がゼロになったplanだけを`plan_approval_required`とします。回答済みplanをcaller側で直接書き換えません。

plan applyは最新plan SHA-256、expected Session Version、正式Project IDへの明示承認を要求します。outer Interaction Command Ledgerが一連の操作を所有し、内側では既存ADR-0018／0019のCEO apply orderingをCommand IDなしで再利用します。適用成功後だけ`plan_applied` turnをappendし、`ready_to_execute`へ進みます。

### Commit ordering and partial failure

```text
explicit approval of exact digest / expected Version
→ workspace Command Ledger claim
→ existing Provider or CEO apply process
→ append one Session turn with CAS
→ Command Ledger terminal outcome
```

- Provider失敗ではplan turnを作らない。
- Provider成功後のSession commit失敗はProvider効果をrollbackせずpartial failureとする。
- Project／Task commit後のSession commit失敗は既存Project／Task／Event／Auditを削除せずpartial failureとする。
- atomic replacement後のdurability確認失敗は再読込でcommit状態を観測しても成功へ丸めない。
- terminal同一Command ID／同一requestは保存Resultを返し、異requestは拒否する。
- `running`、partial failure、SessionとProjectのreconciliationを自動resume／adoptしない。Ledger、Session、既存Recovery evidenceから人間が判断する。

### Product and compatibility boundary

CLIはstart plan／start／inspect／plan generate／answer／plan applyを提供します。loopback HTTPは同じProcess／Serviceを利用し、read-only plan／inspectionとversion付きeffect Commandを分離します。Interaction commandは人間応答を必要とするためone-shot Schedulerの対象にしません。

公開互換の直接`ceo-plan-generate`／`ceo-plan-apply`は破壊せず残します。新しい通常製品体験だけがInteraction Session経路で未回答質問をblockします。Python fallbackやPythonビジネスルールは追加しません。

## Consequences

- 自然言語依頼、質問、再plan、digest承認、Project／Task作成を1つのtyped継続状態で案内できます。
- Provider、Vault、HTTP、credentialはDomain／Serviceへ入らず、既存CEO Plan／writerを再実装しません。
- InteractionからReviewed Workflowを開始してterminal状態まで記録するcompositionはADR-0029で追加済みです。External Action handoffは後続です。
- chat UI、free-form message history、automatic approval、automatic resume、parallel session、recurring Scheduler、Event replay、Session reconciliationは未実装です。
