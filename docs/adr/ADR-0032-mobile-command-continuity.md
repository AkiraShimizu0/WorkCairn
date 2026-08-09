# ADR-0032: mobile Interaction Commandをclient接続から切り離して追跡する

## Status

Accepted

## Context

ADR-0031のLocal Web UIは、ADR-0022の同期`workspace-command.v1`をそのまま利用していました。iPhoneのSafariがlock／backgroundへ移るとHTTP接続が切れ、request context cancellationによって、利用者が承認済みのProvider／Workflow commandを中断し得ます。Command Ledgerは結果を診断できますが、client接続を実行lifetimeとして扱う限り「承認後はMacで進み、必要なときだけiPhoneへ戻る」体験になりません。

一方、durable queue、crash後の自動resume、automatic retry、artifact adoptionを導入すると、ADR-0020／0021の明示Recoveryと`running`を推測しない原則を先取りします。

## Decision

### Additive HTTP preference

既存`POST /v1/commands`は同期動作を維持します。Local Web UIは同じCommandへ`Prefer: respond-async`を付け、daemonが受理した場合は`202 Accepted`、`Preference-Applied`、Command ID、workspace Ledgerのstatus URLを受け取ります。JSON Command version、必須Command ID、typed payload、`approved: true`、same-origin mobile accessは変更しません。

この受理方式は現在、workspace scope Ledgerを持つclosedな`interaction.*` operationだけに限定します。一般commandのscope推測をHTTP Adapterへ追加せず、Project command、CLI、既存同期callerを変更しません。

### Validate before acceptance, claim before effects

daemonはJSON、approval、operation固有typed payloadを副作用と`202`より前に検証します。受理後の実行は既存Process Executorだけを呼び、既存の順序を維持します。

```text
explicit approval + typed validation
→ process-local bounded acceptance
→ durable Command claim
→ existing Process / Service / Adapter effects
→ terminal Ledger outcome
```

HTTP AdapterはTask状態、Review／Revision分岐、approval rule、resultの正本を持ちません。結果は既存Command LedgerとInteraction Sessionだけへ保存し、process-local runnerに第2のresult storeを作りません。

`202`はdaemon processが実行を受理したことを示し、durable queueへのcommitではありません。processがdurable claim前にcrashしてLedgerが存在しない場合、実行済みと推測せず明示確認へ止めます。

### Bounded process lifetime

同時受理数をboundedにし、空きがなければ副作用前に拒否します。受理済みcommandはHTTP request contextから切り離しますが、daemon lifetimeには従います。graceful shutdownは新規受付を停止し、猶予内で受理済みcommandを待ちます。猶予切れでは実行contextをcancelし、既存Ledger／Recovery semanticsへ委ねます。

process crash、再起動後の`running`、claim前crashを自動resumeしません。durable queue、Scheduler変換、Event replay、automatic retry、artifact adoptionは導入しません。

### Read-only continuation

UIは同じCommand IDのworkspace Ledgerをread-only pollingします。`running`は待機、terminal successはSession再取得、failure／partial failureは自動再送せずattention表示にします。送信済みCommandは同一tabの`sessionStorage`にだけ保持し、再読込後もstatus確認だけを再開します。API key、pairing code、Deliverable本文は保存しません。

## Consequences

- iPhoneの接続が受理応答後に切れても、daemonが動作する限り承認済みInteraction commandはMac上で継続します。
- CLIと既存同期HTTP API、Command Ledger、Task／Event ownership、canonical evidenceのcommit pointは変わりません。
- daemon停止／crash後の自動継続保証はありません。`running`、partial、Ledger欠落を推測で修復しません。
- 汎用background job APIではなく、Local Interaction体験に必要な最小transport境界です。
