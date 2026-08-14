# ADR-0043: actual daemonとChromium／WebKitによるPublic Beta Browser Acceptance Gate

## Status

Accepted

## Context

`httptest`とGo package testはHTTP contract、Process composition、temporary Vault、Mock Providerを高速かつ決定的に検証します。しかし実機Acceptanceでは、embedded JavaScriptの初期化、実DOM event、polling再描画、browser storage、pairing cookie、secure-context API、page reload、daemon process再起動という境界で、package testが検出できない不具合が見つかりました。

Public Betaの正式product pathを、製品Runtimeへ別言語依存を戻さずactual browserまで自動検証する独立Gateが必要です。

## Decision

### Playwright Testをtest-onlyで採用する

Browser Acceptance harnessにはPlaywright Testを使い、Chromium desktopとPlaywright WebKitのiPhone相当viewportを直列実行します。Node.js、npm、Playwright、browser binaryはこのharnessだけのtest-only dependencyです。

- Go module、Domain、Service、Adapter、Kernel、製品RuntimeへNode依存を追加しない
- `workcairn*` binaryとrelease archiveへNode、`node_modules`、test resultを含めない
- Pythonを再導入しない
- 通常のGo test／`make v1-release-gate`とは分離する
- browser dependencyは`make public-beta-browser-setup`、Gateは`make public-beta-browser-gate`で明示実行する

### actual daemon境界

harnessはbuild済みの実`workcairn-daemon`を空きportでsubprocess起動します。毎scenarioで新しいtemporary Vaultとtest-only Anthropic互換HTTP serverを用意し、fake credentialとmock URLだけをprocess environmentへ渡します。実Vault、Keychain、`.env`、実Providerへ接続しません。readinessをHTTPで確認し、終了時はSIGTERMによるgraceful shutdownを待ちます。

happy pathでは同じtemporary Vaultでdaemonを再起動し、Interaction、Completion、Timeline、Proof of Work、canonical artifactがbrowser memoryではなくdurable stateから再表示されることを確認します。

### fixed Provider fixture

Provider responseは`fixtures/provider/browser_acceptance_v1.json`のsanitized固定payloadを使います。fixtureはAnthropic互換HTTP status／headers／bodyを丸ごと保持し、test codeやGo parserから期待responseを生成しません。CEO Intent、Task execution、Request Changes、Revision、再Review Approve、typed Provider failureを独立したprovider-boundary inputとして固定します。

### Gateの分離

- `make v1-release-gate`: Go build、Go test、race、vet、format、release matrix
- `make public-beta-smoke`: Go handler／Process中心の高速system smoke
- `make public-beta-browser-gate`: actual daemon、embedded UI、DOM、polling、reload、restartを通すbrowser/system acceptance

Browser Gateは通常のRelease Gateへ暗黙追加しません。test-only Node／browser環境を必要とし、失敗時の診断時間と実行時間が異なるためです。Public Beta candidate判定ではRelease GateとBrowser Gateの両方を明示的に実行します。

### pairingとsecure-contextの範囲

同一hostのbrowserはdaemonからMac-localと判定されるため、harnessは最初のaccess statusだけをremote相当へpresentation-levelで置き換え、実DOM formから実pair endpointへcodeを送信します。Origin／`X-Workspace-Intent`検証、HttpOnly／SameSite pairing cookie、以後のactual API通信は実daemonを通します。

これは別deviceのsource address、private-LAN HTTPの`window.isSecureContext`差、Universal Clipboardを再現しません。Clipboard API不在時のfallback UIはbrowserで確認しますが、実Mac Safari／実iPhone SafariのLAN挙動はhuman Device Acceptanceとして残します。

## Consequences

- Public Betaの正式Interaction Reviewed Workflowを、clarification、single-flight、Request Changes、Revision、再Review、Completionまで実DOMから検証できます。
- polling中のdraft／focus、terminal遷移、page reload、daemon restart、FailureEnvelopeのdurable再投影を回帰検知できます。
- Playwright WebKitはSafariに近いengine coverageですが、実Safari／iOS Safariそのものではありません。
- 固定fixtureは実Providerの最新behavior、billing、permission、network qualityを保証しません。実Provider acceptanceは人間が明示credentialで1回実施します。
- browser testでCore意味論の欠陥が見つかった場合はtestを固定し、別のArchitecture変更として扱います。Gateのためのtest-only business pathは製品コードへ追加しません。
