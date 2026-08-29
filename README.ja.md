[English](README.md) | 日本語

# WorkCairn

WorkCairnは、自然言語で依頼した内容をもとに作業の進め方を考え、実際に作業を進め、完了前に別の担当が内容を確認します。修正が必要なら直して、もう一度確認します。利用者に求められるのは、本当に必要な質問への回答と、重要な場面での承認だけです。細かい進み具合を追いかける必要はありません。

現在Public Beta中の製品です（`v1.0.0-beta.1`）。自分のMac上で動作します。

## 1. WorkCairnとは

WorkCairnが作業の進行を管理するため、利用者が細かな進捗を追い続ける必要はありません。確認や承認が必要なときだけ対応します。

## 2. 何ができるか

- 依頼から作業の進め方を作成し、確認したい点への回答後、承認して作業を開始する
- 作業を進め、別の担当が内容を独立して確認し、修正が必要ならもう一度直して確認する
- 誰が何を担当していて、どう引き継がれているかを`Company View`で確認する
- 何を任せることになるかを、承認する前にその場で確認する
- 完了した内容、確認結果、修正内容、承認、外部への公開を、保存された記録からあとで確認する
- 自動で進んだ部分と、判断が必要だった部分を確認する
- 承認前には何も実行されないこと、問題が起きても隠されないことを信頼できる
- 成果物、確認の記録、実行の記録をローカルに保存する
- 問題が起きた場合、確定している内容だけをもとにした、限定的な復旧手段を使う

## 3. 使い方

### インストール

最初は実際のVaultではなく、空の一時的なフォルダで試してください。

**Sourceからbuild：**

```bash
git clone https://github.com/AkiraShimizu0/WorkCairn.git workcairn
cd workcairn
make go-build
bin/workcairn version
```

**または配布パッケージからinstall**（macOSでは`shasum`、Linuxでは`sha256sum`でチェックサムを確認）：

```bash
shasum -a 256 -c workcairn_v1.0.0-beta.1_darwin_arm64.tar.gz.sha256
tar -xzf workcairn_v1.0.0-beta.1_darwin_arm64.tar.gz
cd workcairn_v1.0.0-beta.1_darwin_arm64
bin/workcairn version
```

### 起動する

```bash
bin/workcairn-daemon
```

初回だけmacOSのフォルダ選択画面が開きます。推奨のiCloud Drive内に空の`WorkCairn`専用フォルダを新規作成して選ぶと画面が開きます（iCloud Driveは推奨であって必須ではなく、任意のローカルフォルダも選択できます）。選んだ場所は次回以降も使われます。既存の個人用Obsidian Vault、ホームディレクトリ、iCloudのルートフォルダは選択できません。

画面の案内に沿って最初のAIチームを承認し、Claudeとの接続を行います。Model IDや接続先を自分で入力する必要はありません。`会社を始める`から最初の依頼へ進めます。

初回セットアップの一巡手順は[Public Beta Quickstart](docs/PublicBetaQuickstart.md)を参照してください。

## 4. 人が行う判断

- 提案された作業の進め方を承認するかどうか
- 内容を確認したうえで、実際に作業を進めることを承認するかどうか
- 確認の結果、修正が必要になったときにどうするか（WorkCairnは修正と再確認を自動で進めますが、追加の指示が必要な場合は止まって確認します）
- 外部への公開を行うかどうか（これは常に別の承認が必要です）

## 5. 安全性

- 副作用のある操作は、すべて承認してから実行されます
- 同じ依頼が重複して届いても、作業を二重に実行しません
- 「どこまで完了したか」「何がまだ確認できていないか」は、保存された記録だけをもとに説明します。推測はしません
- 何を任せるか、確認が必須かどうか、上限などは、承認の対象としてはっきり示されます
- 公開後や保存後に問題が起きても隠さず、完了済みの内容を勝手に削除することもありません
- 状況がはっきりしないときは、推測で処理をやり直さず、何が起きたかを確認するよう案内します
- 実運用の前に、一時的なVaultでの試用と、バックアップの用意をおすすめします

画面を見るだけなら認証情報は不要です。実際に作業計画を作ったり作業を進めたりするには、`設定 → AI Connections`からClaudeを接続します。入力した内容はmacOSのKeychainだけに保存され、ブラウザやファイル、記録には残りません。Model IDを自分で設定する必要もなく、接続が確認できない場合は、何もせず処理を止めます。

処理が異常終了した場合や、想定外の確認が表示された場合は、推測で再実行せず[Recovery Guide](docs/Recovery.md)を参照してください。

## 6. データ保存

WorkCairnは、成果物、確認の結果、作業の記録をVault内に直接保存します。これはあとから追加した機能ではなく、基本的な動作です。Obsidianは同じフォルダを見るための**任意の**手段で、`Open folder as vault`でいつでも開けますが、Obsidianを使わなくてもWorkCairnは問題なく動作します。

WorkCairnはバックアップのための製品ではありません。実運用の前に一時的なVaultで試し、別の方法でバックアップを用意してください。詳しくは[Operator Guide](docs/OperatorGuide.md)と[Recovery Guide](docs/Recovery.md)を参照してください。

## 7. 対応環境

初期Public Betaの対応対象は**macOS／arm64（Apple Silicon）**です。

| OS / architecture | 状態 |
|---|---|
| macOS / arm64 | 対応済み |
| macOS / amd64、Linux / amd64、Linux / arm64 | buildは成功するが実機での確認は未実施 |
| Windows | 非対応 |

Sourceからbuildする場合はGo 1.23以上、`make`、POSIX shell、`tar`が必要です。配布パッケージを使う場合、Go toolchainは不要です。

## 8. 現在の制限

- インターネット越しの利用や、暗号化された接続（TLS）には対応していません
- 別のデバイス（スマートフォンなど）からの接続は、同じローカルネットワーク内でのみ可能です。通常の利用には不要です
- 自動的なバックアップは行いません（[6. データ保存](#6-データ保存)を参照）
- 定期実行や、外部公開（WordPress）の一部機能は存在しますが、まだ主要な流れには含まれていません
- Windowsには対応していません

## 9. 開発者向け情報

WorkCairnの製品コード、build、releaseの仕組みはすべてGoで実装しています。test専用のbrowserテストだけがNode／Playwrightを使いますが、製品には含まれません。

```text
go/            WorkCairn本体のGoコード
go/cmd/        CLI、daemon、coreバイナリのエントリポイント
go/internal/   内部のドメイン、サービス、アダプター、Runtime
docs/          設計・運用・リリースに関するドキュメント
docs/adr/      設計判断の記録
fixtures/      テストで使用する入力例や固定データ
tests/         Browserテストなどの統合テスト
scripts/       Build・Release・検証用スクリプト
```

主なbinaryは3つです：`workcairn-daemon`（日常的に使うもの）、`workcairn`（高度な操作を明示的に行うためのCLI）、`workcairn-core`（他のプログラムとやり取りするための決まった形式のインターフェース）。daemonはVaultの場所を指定する`--vault <path>`と、同じネットワーク上の別デバイスからの接続を許可する`--local-network`を受け付けます。それ以外の詳細は[Operator Guide](docs/OperatorGuide.md)を参照してください。

```bash
make v1-release-gate     # build + 全testと各種チェック
make public-beta-smoke   # 一時的なVaultを使った簡易な動作確認
```

## 10. 詳細ドキュメント

- [Public Beta Quickstart](docs/PublicBetaQuickstart.md)
- [Operator Guide](docs/OperatorGuide.md)
- [Architecture](docs/Architecture.md)と[設計判断の記録](docs/adr/)
- [Release Notes](docs/ReleaseNotes.md)
- [Security Policy](SECURITY.md)
- [Contributing](CONTRIBUTING.md)

## License

[MIT License](LICENSE)
