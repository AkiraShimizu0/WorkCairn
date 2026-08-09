# Workspace OS

Workspace OSは、自然言語の依頼を検証可能なPlan、Project、Task、実行、Review、Revisionへ変換するlocal-firstなAI work orchestratorです。人間が必要な質問と承認だけに応答し、確定した成果物と監査証跡をローカルのMarkdown Vaultへ残します。

現在の候補versionは`v1.0.0-beta.1`です。repository、build、test、release、distributionはGo Onlyです。

## できること

- 自然言語依頼からtyped Planを生成し、質問回答後の明示承認でProject／Taskへ適用
- Task実行、別AI社員によるReview、Request Changes時のRevisionと再Review
- iPhone向けLocal Web UIで「次にすること」だけを案内
- 承認前副作用ゼロ、Task Version/CAS、Command Ledger、partial failureの明示
- Deliverable、canonical Review JSON、Revision intent、Event／Auditをローカル保存
- read-only診断と、確定証拠に拘束された限定的な明示Recovery

## Public Betaの対応環境

| OS / architecture | 状態 | 検証範囲 |
|---|---|---|
| macOS / arm64 | Beta Tier 1 | build、全test、race、native CLI／daemon smoke、iPhone実機確認対象 |
| macOS / amd64 | Release candidate | cross-build済み。Public Beta配布前にIntel Mac native smokeが必要 |
| Linux / amd64 | Release candidate | cross-build済み。Public Beta配布前にnative filesystem／daemon smokeが必要 |
| Linux / arm64 | Release candidate | cross-build済み。Public Beta配布前にnative filesystem／daemon smokeが必要 |
| Windows | 非対応 | Vault file lockが未対応のためwriterをsupportしない |

必要環境はGo 1.23以上、`make`、POSIX shell、`tar`です。配布archiveを使う場合、Go toolchainは不要です。

## 5分で安全に起動する

最初は実Vaultではなく、空のtemporary directoryを使ってください。

### Sourceからbuild

```bash
git clone <repository-url> workspace-os
cd workspace-os
make go-build
bin/workspace-run version
```

### 配布archiveからinstall

macOSでは`shasum`、Linuxでは`sha256sum`でchecksumを確認します。

```bash
shasum -a 256 -c workspace-os_v1.0.0-beta.1_darwin_arm64.tar.gz.sha256
tar -xzf workspace-os_v1.0.0-beta.1_darwin_arm64.tar.gz
cd workspace-os_v1.0.0-beta.1_darwin_arm64
bin/workspace-run version
```

### Loopback Web UIを開く

```bash
beta_vault=$(mktemp -d)
bin/workspace-daemon --vault "$beta_vault"
```

Macのブラウザで`http://127.0.0.1:8787/`を開きます。この段階ではProvider credentialは不要で、実Vaultも変更しません。終了はterminalで`Ctrl-C`です。

## iPhoneから開く最短手順

1. MacとiPhoneを同じ信頼できるWi-Fiへ接続する。
2. temporary Vaultを指定してmobile modeを起動する。

```bash
beta_vault=$(mktemp -d)
bin/workspace-daemon --vault "$beta_vault" --mobile
```

3. terminalに表示された`Workspace OS mobile UI`のURLをiPhone Safariで開く。
4. 同じterminalのpairing codeを入力する。

mobile modeはprivate／link-local addressだけを許可します。TLS、remote authentication、internet公開には対応していません。共有Wi-Fi、port forwarding、reverse proxyでは使用しないでください。

## 自然言語依頼を試す前の設定

UIへ到達するだけならcredentialは不要です。Plan生成やTask実行では、temporary Vaultに社員Markdownを用意し、起動processへ次を注入します。

```bash
ANTHROPIC_API_KEY='<provider key>' \
WORKSPACE_CLAUDE_PROVIDER_MODEL='<supported provider model id>' \
bin/workspace-daemon --vault "$beta_vault" --mobile
```

Workspace OSは`.env`を自動読込しません。API key、Provider response、pairing codeをVaultやCommandへ保存しません。External Action用WordPress設定は任意で、通常のTask／Reviewには不要です。

社員Markdownとtemporary Vaultの準備、初回Operator確認は[Public Beta Quickstart](docs/PublicBetaQuickstart.md)を参照してください。

## 基本フロー

```text
自然言語で依頼
→ 必要なら質問へ回答
→ Plan digestを確認して承認
→ Project / Taskを作成
→ Reviewed Workflowを承認
→ Task実行 → Review
→ Acceptなら次Task
→ Request ChangesならRevision → 再Review
→ 完了した成果物と監査証跡を確認
```

UIはこのフローを実装せず、Go Interaction Sessionの`Next Action`を表示する薄いclientです。Task状態とTask lifecycle EventはTaskServiceだけが変更します。

## 安全モデル

- read-only plan／inspectとwriterを分離
- Provider呼出しとVault変更前に明示承認
- 同一Command ID／requestの安全なreplayと、異requestでのID再利用拒否
- canonical evidence commit後の失敗をrollbackせずpartial failureとして返す
- automatic retry、artifact adoption、Event replayを行わない
- 実運用前にtemporary Vaultと外部backupを要求

異常終了や`attention_required`では、推測で再実行せず[Recovery Guide](docs/Recovery.md)を参照してください。

## 開発・検証

```bash
make public-beta-smoke
make v1-release-gate
```

`public-beta-smoke`はtemporary VaultとMock Providerだけで、Task execution、Deliverable／Audit、Review／Revision分岐、mobile Interaction完了までを検証します。`v1-release-gate`は3 binary、4 target cross-build、全Go test、race、vet、gofmt、repository asset guardを確認します。

release archiveは`VERSION`を既定値として作成できます。

```bash
make release-package RELEASE_GOOS=darwin RELEASE_GOARCH=arm64 \
  BUILD_DATE=2026-08-10T00:00:00Z
```

## 正式な製品surface

- `workspace-run`: plan、approval、execute、inspect、recoveryのCLI
- `workspace-daemon`: HTTP Command APIとmobile-first Local Web UI
- `workspace-core`: JSON Contract v1の外部process boundary

## Documentation

- [Public Beta Quickstart](docs/PublicBetaQuickstart.md)
- [Public Beta Release Checklist](docs/PublicReleaseChecklist.md)
- [Product Naming Review](docs/ProductNaming.md)
- [Operator Guide](docs/OperatorGuide.md)
- [System Overview](docs/SystemOverview.md)
- [Architecture](docs/Architecture.md)
- [HTTP Command API](docs/HTTPAPI.md)
- [Go Only Release Gate](docs/GoOnlyReleaseGate.md)
- [Security Policy](SECURITY.md)
- [Contributing](CONTRIBUTING.md)
- [Migration History](docs/MigrationHistory.md)

## 現在の限界

- remote authentication、TLS、internet公開、Push通知は未実装
- durable queue、自動resume、Event replay、automatic reconciliationは未実装
- Schedulerはone-shot、External Actionは単一WordPress post publishだけ
- WindowsはVault writer非対応

## License

[MIT License](LICENSE)
