# ADR-0031: iPhone向けLocal Web UIはdaemon同一originと明示LAN pairingで提供する

## Status

Accepted

## Context

ADR-0028〜0030のInteraction Sessionと`interaction-next`により、自然言語依頼、質問回答、plan承認、Reviewed Workflow、任意External ActionまでをGo Only Runtimeで調停できます。しかし現在の利用者は個別のCLI／HTTP operationとtyped payloadを組み立てる必要があり、iPhoneから「次に自分が何をするか」だけを見る製品体験にはなっていません。

ADR-0022は認証／TLS／authorizationのないdaemonをloopback以外へ公開しないと決めています。一方、iPhoneはMac上のloopbackへ直接接続できません。認証なしで`0.0.0.0`へbindすることや、UIへAPI key／Vault pathを入力させることは、local-firstであっても既存の安全境界を壊します。remote公開用の認証基盤、TLS、native iOS app、Push通知を同時に導入するのも本フェーズには過剰です。

## Decision

### Thin same-origin client

`workspace-daemon`はGo binaryへembedした依存なしのHTML／CSS／JavaScriptを同一originから配信します。UIは次を唯一の状態判断として使います。

- `workspace-interaction.v1`のSession／turn
- `GET /v1/interactions/{session_id}/next`
- read-only Interaction／Workflow／Action plan
- 承認済み`workspace-command.v1`
- attention時のCommand Ledger参照

UIはTask遷移、Review verdict、Revision条件、dependency readiness、approval requirement、partial failure判定を再実装しません。Next Actionが要求したtyped fieldを収集し、read-only planを表示し、利用者が明示承認した同一digest／Versionを既存Command APIへ渡すだけです。承認しない操作はserver stateを変更せず、画面を閉じてもSessionは承認待ちのまま残ります。

### Mobile-first presentation

最初のviewportはiPhoneの狭い画面を正とし、「次にすること」を先頭へ固定します。質問回答と主要操作は44px以上のtouch targetを持ち、承認と非実行を色・文言・配置で区別します。実行中は内部PromptやProvider responseを表示せず、Session stateと安全な進行メッセージだけを表示します。

Project plan、Task／Review／Revision summary、Command reference、External Action publicationはSessionのbounded evidenceから後から展開できます。Deliverable本文やProvider生responseはSessionやbrowser storageへ複製しません。attention stateは隠さず、自動retry／resume／artifact adoptionを行わず、outer／child CommandとRecovery案内を表示します。

### Explicit trusted-LAN mode

既定のdaemonはADR-0022どおりloopbackだけへbindします。iPhone利用は明示`--mobile`時だけ有効にし、listen先をloopback、private address、またはlink-local addressに限定します。unspecified address、public address、hostnameによる曖昧な解決を拒否します。

mobile modeは起動ごとに暗号学的乱数のpairing codeを生成し、operator端末へだけ表示します。UIはcodeをbodyで同一originのpair endpointへ送り、成功時にprocess lifetimeだけ有効なHttpOnly／SameSite=Strict cookieを受け取ります。code／cookieはVault、`.env`、Session、Command Ledger、browser storageへ保存しません。effect requestはsame-originと専用intent headerも要求します。

これはtrusted local network向けの最小device access boundaryです。HTTP通信を暗号化しないため、敵対的LAN、port forwarding、internet公開には使いません。remote利用へ進む場合はTLS、durable identity、authorization、session revocationを別ADRで設計します。

### Request lifecycle

UIのeffect requestは既存の同期Command APIをそのまま利用します。画面は送信中を明示し、network切断時に自動retryせず、同じCommand ID／同じpayloadをsessionStorageに保持して利用者へstatus確認または明示再送を案内します。sessionStorageにはAPI key、pairing code、Deliverable本文を入れません。

SessionとNext Actionのread-only取得だけをbounded pollingできます。自動承認、自動resume、Scheduler変換、background queueは導入しません。daemon crash後の`running`やpartial failureは既存Command Ledger／Recovery境界へ残します。

## Consequences

- iPhone、iPad、Macのbrowserから、自然言語依頼と必要な質問／承認だけで既存Go workflowを利用できます。
- UIはGo Domain／Processのprojectionであり、新しいbusiness ruleの正本になりません。
- 既定loopbackとJSON Contract v1を壊さず、明示mobile modeだけにLAN exposureを限定できます。
- trusted LANの盗聴やactive attackerには耐えません。internet公開、remote authentication、TLS、native app、Push通知は未実装です。
- 同期Command中にclientが切断された場合の自動継続は保証しません。確定済みLedger／canonical evidenceを確認し、推測で再実行しません。
