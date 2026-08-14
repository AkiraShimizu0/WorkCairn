# ADR-0045: bounded Provider request timeoutをRuntime compositionで一本化する

## Status

Accepted

## Context

Public Betaの実Provider Acceptanceで、credential、Automatic model resolution、endpoint、request構築は成功した一方、Anthropicからresponse headerを受け取る前に60秒のWorkCairn timeoutへ到達しました。これはHTTP error responseではなく、Runtimeが構成した`http.Client.Timeout`による`provider_timeout`でした。

Go Process／Service／Adapterは同じ注入済みHTTP clientを利用していましたが、default値とclient構築は`workcairn` CLIと`workcairn-daemon`に別々に記述されていました。非streaming Provider requestはネットワーク状態や生成量によって60秒を超え得るため、Public Betaの正常処理を早期にfailureへする一方、無制限化や自動retryは既存のApproval、Command Ledger、Recovery方針に反します。

## Decision

Provider request timeoutはRuntime composition edgeが所有し、Public Beta defaultを5分にします。`internal/runtime`がdefaultとProvider HTTP client構築を一つだけ定義し、CLIとdaemonは同じpolicyを使用します。

- `workcairn-daemon --provider-timeout`と`workcairn --timeout`の明示overrideは維持し、正のdurationだけを受け付ける
- composition rootは一つのbounded HTTP clientをCEO Intent、Task Execution、Review、Reviewed Workflowへ注入する
- Revision intent作成自体はProviderを呼ばず、Revision Taskの実行と再Reviewは同じReviewed Workflow clientを使う
- Process、Domain、Kernel、Employee Contextは独自Provider timeoutや具体値を持たない
- request contextはAdapterの`http.NewRequestWithContext`まで伝播し、caller cancellationとHTTP client timeoutのどちらもtransportを終了する
- timeoutはtyped `provider_timeout`として既存FailureEnvelope、Command Ledger、HTTP、UIへ伝播し、成功やHTTP Provider errorへ変換しない
- timeout時も自動retry、別Provider／model fallback、既存Commandの推測再実行を行わない

5分は従来の60秒より長い生成を許容しつつ、Anthropic SDKのより長いnon-streaming defaultより短いbounded product policyです。UIはCommandがrunningの間、既存のin-flight表示とsingle-flight制御を維持します。terminal timeout後だけpersistent failureを表示し、同じ操作controlを自動復活・再送しません。

Streamingは長時間requestの進行観測と接続維持に有効ですが、response contract、partial stream、cancellation、durable diagnosticsの追加設計が必要なため、この変更には含めません。

## Consequences

- 一般利用でModelごとのtimeoutを設定せず、全Provider呼出しが同じRuntime policyに従います。
- operatorは既存flagで環境に合わせた短縮／延長ができますが、無制限値は指定できません。
- timeoutまでUIが長く待つ可能性はありますが、Commandはsingle-flightで、終了時はLedgerへtyped terminal outcomeを記録します。
- testは短いdurationを注入して、成功、timeout、context cancellation、no retryを実時間数分なしで検証します。
- 実Provider latencyとstreaming導入判断はhuman Acceptanceと後続Phaseへ残します。
