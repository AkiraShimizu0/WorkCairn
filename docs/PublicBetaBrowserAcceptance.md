# Public Beta Browser Acceptance

Public Beta Browser Gateは、actual `workcairn-daemon`、embedded Web UI、temporary Vault、sanitized固定Provider fixtureをChromiumとPlaywright WebKitから操作します。Go Release Gateを置き換えるものではありません。

## 初回セットアップ

Node.js 20以上をtest harness用に用意し、repository rootで実行します。

```bash
make public-beta-browser-setup
```

Linux CIではPlaywright公式のsystem dependencyが別途必要です。CI imageで依存を事前導入するか、管理された環境で`npx playwright install --with-deps chromium webkit`を実行してください。Node／browserはtest-onlyで、release archiveへ含まれません。

## Gate

```bash
make public-beta-browser-gate
```

Gateは`workcairn-daemon`をbuildし、次をChromium desktopとWebKit iPhone viewportで検証します。

- pairing form、Origin／intent header、HttpOnly cookie
- First Run、Starter Organization、自然言語request
- clarification draft／focusのpolling中保持
- submit直後のin-flight表示とsingle-flight
- Canonical Plan承認、Reviewed Workflow
- Deliverable、Request Changes、Revision、再Review Approve
- Completion、Timeline、Proof of Work
- reloadと同じtemporary Vaultでのdaemon restart
- typed Provider failureとFailureEnvelopeのfresh browser復元
- Clipboard API不在時の安全なcopy fallback

Provider mockは[固定fixture](../fixtures/provider/browser_acceptance_v1.json)を順に返します。test codeはGo parserからresponseを生成しません。

## Gateが保証しないこと

- Playwright WebKitと実Mac Safari／実iPhone Safariの完全一致
- private-LAN source addressとHTTP secure-context差
- iCloud同期、複数device、実Obsidian表示
- 実Anthropic credential、model permission、billing、rate limit、実network
- Push通知、remote公開、TLS

これらは[Public Beta Quickstart](PublicBetaQuickstart.md)のhuman Device Acceptanceで最後に確認します。
