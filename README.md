# WorkCairn

**あなたのAI会社。必要な判断だけ、あなたがする。**

候補versionは`v1.0.0-beta.1`です。製品Runtime、build、release、distributionはすべてGo Onlyです。actual browserを検証する独立Acceptance harnessだけはtest-only Node／Playwright（ADR-0043）を使い、製品archiveには含めません。

## 1. WorkCairnとは

WorkCairnは、自分専用のAI会社へ自然言語で仕事を依頼するlocal-firstな製品です。AI社員が計画、実行、独立レビュー、必要な修正を進め、人間には本当に必要な質問と重要な承認だけを返します。

WorkCairnは会社シミュレーションではありません。給与や機嫌を管理する代わりに、誰が作り、誰がレビューし、必要なら誰が直しているかを見せます。通常時は`Your company is working. No action needed.`と表示し、CEOである利用者へ細かな管理を要求しません。

## 2. 何ができるか

- 自然言語の依頼からtyped Planを生成し、質問への回答後、明示承認でProject／Taskへ適用
- Task実行、別のAI社員による独立レビュー、Request Changes時の修正と再レビュー
- `Company View`で、AI社員、担当、Maker → Reviewer → 修正の流れを確認
- Workflow承認時に、今回AI会社へ任せる範囲をAutonomy Contractとして確認
- 成果物、独立レビュー、修正、承認、外部公開を保存済みの確定記録から確認
- 会社が自律的に進めたstepと、人間の判断を仰いだstepをCEO Attentionとして確認
- 承認前の副作用ゼロ、Task Version／CAS、Command Ledger、部分失敗の明示
- 成果物、レビューの証跡、修正の意図、実行記録をローカル保存
- read-onlyの診断と、確定した証跡だけに基づく限定的な明示Recovery

## 3. 基本的な考え方

```text
自然言語で依頼
→ 必要なら質問へ回答
→ 一般向けの「進め方」を確認して承認（内部digestは詳細から確認）
→ Project / Taskを作成
→ Reviewed Workflowを承認
→ Task実行 → レビュー
→ 問題なければ次のTaskへ
→ 修正が必要なら再修正 → 再レビュー
→ 完了した成果物と実行記録を確認
```

UIはこのフローを実装せず、Go Interaction Sessionの`Next Action`を表示する薄いclientです。Task状態とTask lifecycle Eventの変更はTaskServiceだけが行います。

Public Betaの一般daemonは、この経路に必要な`workspace.setup`、`interaction.start`、`interaction.plan.generate`、`interaction.answer`、`interaction.plan.apply`、`interaction.workflow.execute`だけを実行できます。個別のTask／レビュー／修正操作、Scheduler、外部公開などはoperator用CLI／内部処理として維持しますが、一般Web UIからは実行できません。

## 4. 現在の対応環境

初期Public Betaの対応対象は**macOS／arm64**です。

| OS / architecture | 状態 | 検証範囲 |
|---|---|---|
| macOS / arm64 | Beta Tier 1 | build、全test、race、native CLI／daemon smoke対象 |
| macOS / amd64 | Release candidate | cross-build済み。配布前にIntel Mac native smokeが必要 |
| Linux / amd64 | Release candidate | cross-build済み。配布前にnative filesystem／daemon smokeが必要 |
| Linux / arm64 | Release candidate | cross-build済み。配布前にnative filesystem／daemon smokeが必要 |
| Windows | 非対応 | Vault file lockが未対応のためwriterをsupportしない |

必要環境はGo 1.23以上、`make`、POSIX shell、`tar`です。配布archiveを使う場合、Go toolchainは不要です。

## 5. インストール

最初は実Vaultではなく、空のtemporary directoryを使ってください。

### Sourceからbuild

```bash
git clone https://github.com/AkiraShimizu0/WorkCairn.git workcairn
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

## 6. 起動方法

```bash
bin/workcairn-daemon
```

初回だけmacOSのfolder pickerが開きます。推奨のiCloud Drive内に空の`WorkCairn`専用folderを新規作成して選ぶと、Local Web UIが開きます（iCloud Driveは推奨であって必須ではなく、任意のローカルfolderも選択できます）。保存先はApplication Supportへ記録され、再起動後も同じ専用Vaultを使います。既存の個人Obsidian Vault、home、iCloud rootは選択できません。

GUI WizardでStarter Organizationを明示承認し、Macのnative画面からClaudeをKeychainへ接続します。Model IDやRouteを入力する必要はありません。`会社を始める`から最初の依頼へ進めます。

自然言語依頼を試す前の設定について詳しくは[9. 安全性 / approval](#9-安全性--approval)を、temporary Vault、First-run Wizard、初回Operator確認の一巡手順は[Public Beta Quickstart](docs/PublicBetaQuickstart.md)と[macOS First-run Acceptance](docs/PublicBetaFirstRunAcceptance.md)を参照してください。

## 7. 主要CLI

- `workcairn-daemon`: Public Beta一般利用者向けのInteraction Reviewed WorkflowとLocal Web UIを提供するdaemon
- `workcairn`: plan、approval、execute、inspect、recoveryを明示的に扱うoperator CLI
- `workcairn-core`: JSON Contract v1を公開する外部process境界

`workcairn`はoperator向けの詳細なsubcommand（Organization参照、Project／Task作成、Recoveryなど）を持ちます。一般利用ではdaemonのWeb UIだけで完結し、operator subcommandは通常は不要です。詳細は[Operator Guide](docs/OperatorGuide.md)を参照してください。

## 8. daemonの引数

Public Betaの一般利用で使う可能性がある引数は次のとおりです。

| 引数 | 既定値 | 説明 |
|---|---|---|
| `--vault` | （空。native pickerで解決） | Vault rootを明示指定します。上級者向けです。 |
| `--listen` | `127.0.0.1:8787` | daemonがlistenするaddressを明示指定します。上級者向けです。 |
| `--local-network` | `false` | trusted local network上の別デバイス（iPhone等）から接続できるよう、private addressを自動選択してbindし、pairingを要求します。 |
| `--claude-credential-source` | `automatic` | Claude credentialの取得元（`automatic`／`environment`／`keychain`／`headless-local`）。通常は既定のままで構いません。 |

`--listen`と`--local-network`を両方指定した場合は、`--listen`で明示したaddressがそのまま使われます（自動選択より優先）。`--local-network`だけを指定した場合は、private／link-localなIPv4addressを自動選択します。既定（どちらも未指定）ではloopback（`127.0.0.1`）だけを受け付けます。

`--local-network`はinternet公開のためのものではありません。TLS、remote authentication、port forwardingには対応しておらず、信頼できる同一LAN上の別デバイスから接続する用途に限定されます。上記以外の運用者向け引数（`--provider-timeout`など）は[Operator Guide](docs/OperatorGuide.md)を参照してください。

## 9. 安全性 / approval

- 変更前に内容を確認でき、重要な副作用は明示承認まで開始しない
- 同じ依頼が届いても仕事を重複実行せず、異なる依頼の取り違えを拒否する
- 「どこまで完了したか」「何が未確認か」を成立済みの記録から説明する
- 任せたEmployee、レビュー必須、修正、実行上限を承認対象のdigestへ固定する
- 外部公開後や成果物保存後の失敗を隠さず、完了済み部分を勝手に削除しない
- 状態が曖昧なときは勝手に再実行せず、人間へRecovery確認を返す
- 実運用前にtemporary Vaultと外部backupを要求する

UIへ到達するだけならcredentialは不要です。Plan生成やTask実行では、Macの`Settings → AI Connections → MacでClaudeを接続`を使います。native hidden-inputへ入力したcredentialはmacOS Keychainだけに保存され、browser、Vault、Command、logへは渡りません。WorkCairnは`.env`を自動読込せず、Provider model IDの設定も不要です。論理Route`workcairn-auto`をsupported-model policyで解決し、接続不足ならProvider送信前に停止して別Providerへ無断で切り替えません。

異常終了や`attention_required`では、推測で再実行せず[Recovery Guide](docs/Recovery.md)を参照してください。

## 10. データ保存

WorkCairn自身がVault内へ、成果物、独立レビューのcanonical JSON、修正の意図、実行記録／監査証跡を永続化することがprimary behaviorです。Obsidianは**任意のviewer**であり、必須のdependencyではありません。Finderに表示した専用folderをObsidianの`Open folder as vault`で開けば、同じ成果物と人間可読の履歴を閲覧できます。Obsidianを一度も開かなくても、WorkCairnは通常どおり動作します。

WorkCairn自身はVault backup製品ではありません。実運用前にtemporary Vaultで確認し、外部backupを別途用意してください。詳しくは[Operator Guide](docs/OperatorGuide.md)と[Recovery Guide](docs/Recovery.md)を参照してください。

## 11. リポジトリ構成

```text
go/            WorkCairn本体のGoコード
go/cmd/        CLI / daemon / core binaryのentry point
go/internal/   Domain / Service / Adapter / Runtime
docs/          設計・運用・Release docs
docs/adr/      Architecture Decision Records
fixtures/      JSON Contract / Providerなどのfixture
tests/         browser / integration tests
scripts/       release / verification scripts
.ai/           AI開発agent向けcontext
AGENTS.md      AI開発ルール
README.md      利用者向け概要（このfile）
CHANGELOG.md   変更履歴
SECURITY.md    脆弱性報告
VERSION        release versionの正
Makefile       build/test/release commands
```

## 12. 現在の制限

- remote authentication、TLS、internet公開、Push通知は未実装
- durable queue、自動resume、Event replay、automatic reconciliationは未実装
- Schedulerと単一WordPress外部公開はoperator向け機能として残るが、Public Beta一般UIでは非表示
- WindowsはVault writer非対応
- iPhone等の別デバイスからのLocal Web UI接続（`--local-network`）はavailableな任意機能であり、Public Beta必須の対応対象ではない

## 13. 詳細ドキュメント

- [Public Beta Quickstart](docs/PublicBetaQuickstart.md)
- [Release Notes](docs/ReleaseNotes.md)
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

## 開発・検証

```bash
make public-beta-smoke
make public-beta-browser-setup # 初回のみ。test-only Node / Chromium / WebKit
make public-beta-browser-gate
make v1-release-gate
```

Browser Gateはactual daemonとembedded UIを操作する独立Acceptanceです。Node／Playwrightはtest-onlyで、Go製品Runtimeやrelease archiveには含まれません。

`public-beta-smoke`はtemporary VaultとMock Providerだけで、Task execution、成果物／監査証跡、レビュー／修正の分岐、Interaction経由の依頼完了までを検証します。`v1-release-gate`は3 binary、4 target cross-build、全Go test、race、vet、gofmt、repository asset guardを確認します。

release archiveは`VERSION`を既定値として作成できます。

```bash
make release-package RELEASE_GOOS=darwin RELEASE_GOARCH=arm64 \
  BUILD_DATE=2026-08-10T00:00:00Z
```

## License

[MIT License](LICENSE)
