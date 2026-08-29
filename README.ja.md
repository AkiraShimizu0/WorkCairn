[English](README.md) | 日本語

# WorkCairn

**あなたのAI会社。必要な判断だけ、あなたがする。**

候補versionは`v1.0.0-beta.1`です。製品のRuntime、build、release、配布のすべてをGoだけで実装しています（Go Only）。実際のbrowserを使って検証する独立したAcceptance harnessだけは、test専用のNode／Playwright（ADR-0043）を使いますが、製品のarchiveには含まれません。

## 1. WorkCairnとは

WorkCairnは、自分専用のAI会社へ自然言語で仕事を依頼できる製品です。ネットワーク越しのサーバーではなく、自分のMac上で動作します。AI社員が計画を立て、実行し、別のAI社員が独立してレビューし、必要なら修正します。人間には、本当に必要な質問と重要な承認だけが返ってきます。

WorkCairnは会社シミュレーションではありません。給与や機嫌を管理するような機能はなく、代わりに「誰が作ったか」「誰がレビューしたか」「必要なら誰が直しているか」を見せます。普段は`Your company is working. No action needed.`（会社は働いています。対応は不要です）とだけ表示され、CEOである利用者に細かな管理を求めません。

## 2. 何ができるか

- 自然言語の依頼から作業計画を作成し、確認したい点への回答後、明示的な承認でProject／Taskとして反映
- Task実行、別のAI社員による独立したレビュー、修正が必要な場合の再修正と再レビュー
- `Company View`で、AI社員、担当、作成担当 → レビュー担当 → 修正の流れを確認
- Workflowを承認するとき、今回AI会社に任せる範囲（Autonomy Contract）をその場で確認
- 成果物、独立したレビュー結果、修正内容、承認、外部への公開を、保存済みの実行記録から確認
- 会社が自律的に進めた作業と、人間の判断を仰いだ作業を、CEO Attentionとして一覧で確認
- 承認前は一切の副作用がないこと、途中で失敗した場合は隠さず明示されることを保証
- 成果物、レビュー結果、修正の意図、実行記録をすべてローカルに保存
- 読み取り専用の診断と、確定した記録に基づく限定的な明示的Recovery（復旧）

## 3. 基本的な考え方

```text
自然言語で依頼
→ 必要なら確認の質問に回答
→ 「このように進めます」という説明を確認して承認
→ Project / Taskを作成
→ 作業内容（Workflow）を承認
→ Task実行 → レビュー
→ 問題なければ次のTaskへ
→ 修正が必要なら再修正 → 再レビュー
→ 完了した成果物と実行記録を確認
```

画面（UI）はこの流れをそのまま実装しているわけではなく、内部の状態管理（Interaction Session）が示す「次にすべきこと」を表示するだけの薄いclientです。Taskの状態と、その変化の記録は、内部の単一の仕組み（TaskService）だけが管理します。

Public Betaの一般利用者向けdaemonでは、この一連の流れに必要な操作だけが実行できます。個別のTask操作、レビュー、修正、Scheduler、外部公開などは運用者向けのCLIや内部処理として存在しますが、一般のWeb UIからは実行できません。

## 4. 現在の対応環境

初期Public Betaの対応対象は**macOS／arm64（Apple Silicon）**です。

| OS / architecture | 状態 | 検証範囲 |
|---|---|---|
| macOS / arm64 | Beta Tier 1 | build、全test、raceテスト、実機でのCLI／daemon動作確認まで完了 |
| macOS / amd64 | Release candidate | cross-buildは完了。配布前にIntel Macでの実機確認が必要 |
| Linux / amd64 | Release candidate | cross-buildは完了。配布前に実機でのfilesystem／daemon確認が必要 |
| Linux / arm64 | Release candidate | cross-buildは完了。配布前に実機でのfilesystem／daemon確認が必要 |
| Windows | 非対応 | Vaultのfile lockに未対応のため、書き込み操作をサポートしません |

必要な環境はGo 1.23以上、`make`、POSIX shell、`tar`です。配布パッケージを使う場合はGo toolchainは不要です。

## 5. インストール

最初は実際のVaultではなく、空の一時的なディレクトリで試してください。

### Sourceからbuild

```bash
git clone https://github.com/AkiraShimizu0/WorkCairn.git workcairn
cd workcairn
make go-build
bin/workcairn version
```

### 配布パッケージからinstall

macOSでは`shasum`、Linuxでは`sha256sum`でチェックサムを確認します。

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

初回だけmacOSのフォルダ選択画面が開きます。推奨のiCloud Drive内に空の`WorkCairn`専用フォルダを新規作成して選ぶと、Local Web UIが開きます（iCloud Driveは推奨であって必須ではなく、任意のローカルフォルダも選択できます）。保存先はApplication Supportへ記録され、再起動後も同じ専用Vaultを使い続けます。既存の個人用Obsidian Vault、ホームディレクトリ、iCloudのルートフォルダは選択できません。

画面上のWizardでStarter Organization（最初のAIチーム）を明示的に承認し、Macのネイティブ画面からClaudeをKeychainへ接続します。Model IDや接続先を自分で入力する必要はありません。`会社を始める`から最初の依頼へ進めます。

自然言語で依頼を送る前の準備については[9. 安全性と承認](#9-安全性と承認)を、temporary Vaultや初回セットアップの一巡手順は[Public Beta Quickstart](docs/PublicBetaQuickstart.md)と[macOS First-run Acceptance](docs/PublicBetaFirstRunAcceptance.md)を参照してください。

## 7. 主要CLI

- `workcairn-daemon`: Public Betaの一般利用者が使う、依頼の受け付けとLocal Web UIを提供するdaemon
- `workcairn`: 計画、承認、実行、状態確認、復旧を明示的に扱う、運用者向けCLI
- `workcairn-core`: 決まった形式（JSON Contract v1）でやり取りする、外部processとの境界

`workcairn`には運用者向けの詳細なsubcommand（Organizationの参照、Project／Taskの作成、Recoveryなど）があります。一般的な利用ではdaemonのWeb UIだけで完結し、これらのsubcommandは通常は使いません。詳しくは[Operator Guide](docs/OperatorGuide.md)を参照してください。

## 8. daemonの引数

Public Betaの一般利用で使う可能性がある引数は次のとおりです。

| 引数 | 既定値 | 説明 |
|---|---|---|
| `--vault` | （空。フォルダ選択画面で決定） | Vaultの保存場所を明示的に指定します。上級者向けです。 |
| `--listen` | `127.0.0.1:8787` | daemonが待ち受けるIPアドレスとポートを明示的に指定します。上級者向けです。 |
| `--local-network` | `false` | ローカルネットワーク上の別のデバイスからWorkCairnへ接続できるようにします。ローカルネットワーク内のIPアドレスを自動で選択し、接続には別途ペアリングが必要です。 |
| `--claude-credential-source` | `automatic` | Claudeの認証情報の取得元（`automatic`／`environment`／`keychain`／`headless-local`）。通常は既定のままで構いません。 |

`--listen`と`--local-network`を両方指定した場合は、`--listen`で明示したアドレスがそのまま使われます（自動選択より優先されます）。`--local-network`だけを指定した場合は、ローカルネットワーク内のIPアドレスを自動で選択します。どちらも指定しない既定の状態では、同じMac上（`127.0.0.1`）からの接続だけを受け付けます。

`--local-network`はインターネットへの公開を目的としたものではありません。暗号化通信（TLS）やリモート認証には対応しておらず、信頼できる同じネットワーク上の別デバイスから接続する用途に限定されます。ここに記載のない運用者向けの引数（`--provider-timeout`など）については[Operator Guide](docs/OperatorGuide.md)を参照してください。

## 9. 安全性と承認

- 変更前に内容を確認でき、外部への公開やデータ変更など副作用のある操作は人による明示的な承認が必要です
- 同じ依頼が重複して届いても仕事を二重に実行せず、別の依頼と取り違えることもありません
- 「どこまで完了したか」「何がまだ確認できていないか」を、確定した記録だけをもとに説明します
- 任せるAI社員、レビューの必須化、修正の回数上限などは、承認の対象として画面にはっきり示されます
- 外部公開後や成果物を保存した後に失敗しても、それを隠したり、完了済みの部分を勝手に削除したりしません
- 状態がはっきりしないときは推測で処理を再実行せず、人間に確認を求めます
- 実運用の前に、一時的なVaultでの試用と、外部で取得したバックアップの用意を推奨します

画面に到達するだけなら認証情報は不要です。作業計画の生成やTaskの実行には、Macの`設定 → AI Connections → MacでClaudeを接続`を使います。入力した認証情報はmacOSのKeychainだけに保存され、browser、Vault、Command、logへは一切渡りません。WorkCairnは`.env`ファイルを自動では読み込まず、AIプロバイダーのModel IDを自分で設定する必要もありません。接続先は自動的に解決され、接続が確認できない場合は送信前に停止し、無断で別のAIプロバイダーへ切り替えることもありません。

処理が異常終了した場合や「対応が必要です」と表示された場合は、推測で再実行せず[Recovery Guide](docs/Recovery.md)を参照してください。

## 10. データ保存

WorkCairn自身が、Vault内に成果物、レビューの正式な記録、修正の意図、実行記録・監査証跡を保存します。これがWorkCairnの基本的な動作方針です。Obsidianは**任意のviewer**であり、必須のアプリケーションではありません。Finderに表示される専用フォルダをObsidianの`Open folder as vault`で開けば、同じ成果物と人間が読める形式の履歴を閲覧できます。Obsidianを一度も開かなくても、WorkCairnは通常どおり動作します。

WorkCairn自身はバックアップ製品ではありません。実運用の前に一時的なVaultで試し、WorkCairnとは別の方法で取得したバックアップを用意してください。詳しくは[Operator Guide](docs/OperatorGuide.md)と[Recovery Guide](docs/Recovery.md)を参照してください。

## 11. リポジトリ構成

```text
go/            WorkCairn本体のGoコード
go/cmd/        CLI、daemon、coreバイナリのエントリポイント
go/internal/   WorkCairn内部のドメイン、サービス、アダプター、Runtime
docs/          設計・運用・リリースに関するドキュメント
docs/adr/      重要な設計判断の記録（Architecture Decision Records）
fixtures/      テストで使用する入力例や固定データ
tests/         Browserテストなどの統合テスト
scripts/       Build・Release・検証用スクリプト
.ai/           AI開発エージェント向けの作業コンテキスト
AGENTS.md      AIエージェントが作業するときのルール
README.md      英語版README
README.ja.md   日本語版README（このfile）
CHANGELOG.md   変更履歴
SECURITY.md    脆弱性報告について
VERSION        releaseバージョンの正
Makefile       build・test・release用コマンド
```

## 12. 現在の制限

- リモート認証、暗号化通信（TLS）、インターネットへの公開、Push通知は未対応です
- 途中で中断した処理の自動再開や、通信の永続的なキューイングは未対応です
- Schedulerと単一のWordPress外部公開は運用者向け機能として存在しますが、Public Betaの一般UIには表示されません
- WindowsはVaultへの書き込みに未対応です
- 別デバイス（iPhoneなど）からのLocal Web UI接続（`--local-network`）は利用できる機能として存在しますが、Public Betaで必須の対応環境ではありません

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
make public-beta-browser-setup # 初回のみ。test専用のNode / Chromium / WebKit
make public-beta-browser-gate
make v1-release-gate
```

Browser Gateは、実際のdaemonとembedしたUIを操作して確認する、独立したAcceptanceです。Node／Playwrightはtest専用で、製品のRuntimeやrelease archiveには含まれません。

`public-beta-smoke`は一時的なVaultとMock（模擬）AIプロバイダーだけを使い、Task実行、成果物・監査証跡、レビュー・修正の分岐、依頼の完了までを確認します。`v1-release-gate`は3つのbinary、4つのtarget向けcross-build、全Go test、raceテスト、静的解析（vet）、フォーマット確認（gofmt）、リポジトリ内容の確認を行います。

release archiveは`VERSION`を既定値として作成できます。

```bash
make release-package RELEASE_GOOS=darwin RELEASE_GOARCH=arm64 \
  BUILD_DATE=2026-08-10T00:00:00Z
```

## License

[MIT License](LICENSE)
