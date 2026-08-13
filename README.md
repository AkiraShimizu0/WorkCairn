# WorkCairn

**Your AI company that manages itself.**

WorkCairnは、自分専用のAI会社へ自然言語で仕事を依頼するlocal-firstな製品です。AI社員が計画、実行、独立Review、必要なRevisionを進め、人間には本当に必要な質問と重要な承認だけを返します。

**あなたのAI会社。必要な判断だけ、あなたがする。** 会社は見える。仕事も見える。でも管理しなくていい。

現在の候補versionは`v1.0.0-beta.1`です。repository、build、test、release、distributionはGo Onlyです。

## 自分のAI会社を持つ

- 自然言語依頼からtyped Planを生成し、質問回答後の明示承認でProject／Taskへ適用
- Task実行、別AI社員によるReview、Request Changes時のRevisionと再Review
- iPhoneの`My Actions`で、本当に必要な質問・承認・Recoveryだけを案内
- PC／iPadの`Company View`で、AI社員、担当、Maker → Reviewer → Revisionの流れを確認
- Workflow承認時に、今回AI会社へ任せる範囲をAutonomy Contractとして確認
- 成果物、独立Review、Revision、承認、外部Actionを保存済みの確定記録から確認
- 会社が進めたstepと、人間を呼ばずに進めたstepをCEO Attentionとして確認
- 承認前副作用ゼロ、Task Version/CAS、Command Ledger、partial failureの明示
- Deliverable、canonical Review JSON、Revision intent、Event／Auditをローカル保存
- read-only診断と、確定証拠に拘束された限定的な明示Recovery

WorkCairnは会社simulationではありません。給与や機嫌を管理する代わりに、誰が作り、誰がReviewし、必要なら誰が直しているかを見せます。通常時は`Your company is working. No action needed.`と表示し、CEOである利用者へ細かな管理を要求しません。

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
git clone <repository-url> workcairn
cd workcairn
make go-build
bin/workcairn version
```

### 配布archiveからinstall

macOSでは`shasum`、Linuxでは`sha256sum`でchecksumを確認します。

```bash
shasum -a 256 -c workcairn_v1.0.0-beta.1_darwin_arm64.tar.gz.sha256
tar -xzf workcairn_v1.0.0-beta.1_darwin_arm64.tar.gz
cd workcairn_v1.0.0-beta.1_darwin_arm64
bin/workcairn version
```

### macOSで会社を始める

```bash
bin/workcairn-daemon
```

初回だけmacOSのfolder pickerが開きます。推奨のiCloud Drive内に空の`WorkCairn`専用folderを新規作成して選ぶと、Local Web UIが開きます。保存先はApplication Supportへ記録され、再起動後も同じ専用Vaultを使います。既存の個人Obsidian Vault、home、iCloud rootは選択できません。

GUI WizardでStarter Organizationを明示承認し、Macのnative画面からClaudeをKeychainへ接続します。Model IDやRouteを入力する必要はありません。`会社を始める`から最初の依頼へ進めます。Obsidianは任意で、Finderに表示した専用folderを`Open folder as vault`で開けます。

## iPhoneから開く最短手順

1. MacとiPhoneを同じ信頼できるWi-Fiへ接続する。
2. mobile modeを起動する。初回はMacで上記First-runを完了する。

```bash
bin/workcairn-daemon --mobile
```

3. terminalに表示された`WorkCairn mobile UI`のURLをiPhone Safariで開く。
4. 同じterminalのpairing codeを入力する。

mobile modeはprivate／link-local addressだけを許可します。TLS、remote authentication、internet公開には対応していません。共有Wi-Fi、port forwarding、reverse proxyでは使用しないでください。

## 自然言語依頼を試す前の設定

UIへ到達するだけならcredentialは不要です。Plan生成やTask実行では、Macの`Settings → AI Connections → MacでClaudeを接続`を使います。native hidden-inputへ入力したcredentialはmacOS Keychainだけに保存され、browser、Vault、Command、logへ渡りません。WorkCairnは`.env`を自動読込せず、Provider model IDの設定も不要です。論理Route `workcairn-auto`をsupported-model policyで解決し、接続不足ならProvider送信前に停止して別Providerへ無断で切り替えません。

temporary／iCloud Drive専用Vault、First-run Wizard、初回Operator確認は[Public Beta Quickstart](docs/PublicBetaQuickstart.md)を参照してください。
実機の一巡手順は[macOS First-run Acceptance](docs/PublicBetaFirstRunAcceptance.md)を参照してください。

## 基本フロー

```text
自然言語で依頼
→ 必要なら質問へ回答
→ 一般向けの「進め方」を確認して承認（内部digestは詳細から確認）
→ Project / Taskを作成
→ Reviewed Workflowを承認
→ Task実行 → Review
→ Acceptなら次Task
→ Request ChangesならRevision → 再Review
→ 完了した成果物と監査証跡を確認
```

UIはこのフローを実装せず、Go Interaction Sessionの`Next Action`を表示する薄いclientです。Task状態とTask lifecycle EventはTaskServiceだけが変更します。

## 安全に任せられる理由

- 変更前に内容を確認でき、重要な副作用は明示承認まで開始しない
- 同じ依頼が届いても仕事を重複実行せず、異なる依頼の取り違えを拒否する
- 「どこまで完了したか」「何が未確認か」を成立済み記録から説明する
- 任せたEmployee、Review必須、Revision、実行上限を承認対象digestへ固定する
- 外部公開後や成果物保存後の失敗を隠さず、完了済み部分を勝手に削除しない
- 状態が曖昧なときは勝手に再実行せず、人間へRecovery確認を返す
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

- `workcairn`: plan、approval、execute、inspect、recoveryのCLI
- `workcairn-daemon`: HTTP Command APIとmobile-first Local Web UI
- `workcairn-core`: JSON Contract v1の外部process boundary

## Documentation

- [Public Beta Quickstart](docs/PublicBetaQuickstart.md)
- [Public Beta Release Checklist](docs/PublicReleaseChecklist.md)
- [Product Naming](docs/ProductNaming.md)
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
