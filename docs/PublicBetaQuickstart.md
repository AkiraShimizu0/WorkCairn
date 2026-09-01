# Public Beta Quickstart

## 目的

第三者が実Vaultや実Providerを誤って使用せず、install、first-run、Local Web UI、Mock Provider E2Eを確認するための手順です。

[GitHub Releases](https://github.com/AkiraShimizu0/WorkCairn/releases)から`workcairn_<version>_darwin_arm64.dmg`と対応する`.sha256`を選んでください。最初の正式サポート対象はmacOS／arm64です。このDMGはDeveloper ID署名済み・notarization済み・staple済みです。

## Clean environmentからinstall

### 配布DMG（macOS）

1. `.dmg`と対応する`.sha256`を同じdirectoryへ置く。
2. checksumを検証する。
3. FinderでDMGを通常のdouble-click openでmountする（`xattr`削除や右クリックoverrideは不要、Gatekeeperのセキュリティ設定を恒久的に緩和する必要もない）。
4. mountされたvolume配下の3 binaryのversionが`.dmg`のversion、`VERSION`、Release noteと一致することを確認する。

```bash
shasum -a 256 -c workcairn_<version>_darwin_arm64.dmg.sha256
open workcairn_<version>_darwin_arm64.dmg
/Volumes/workcairn_<version>_darwin_arm64/bin/workcairn version
/Volumes/workcairn_<version>_darwin_arm64/bin/workcairn-daemon --version
/Volumes/workcairn_<version>_darwin_arm64/bin/workcairn-core --version
```

tar.gz形式の配布archiveは、macOS Public Betaのcanonical assetではありません（Linux向けrelease engineeringでは引き続き使用します）。

### Source checkout

```bash
git clone https://github.com/AkiraShimizu0/WorkCairn.git workcairn
cd workcairn
make v1-release-gate
```

Go 1.23以上だけを使用します。別言語runtimeやpackage managerは不要です。

## 一般ユーザーのmacOS First-run

通常利用ではterminalでデータフォルダのpath、Employee ID、Role、Model IDを設定しません。

```bash
bin/workcairn-daemon
```

初回だけnative folder pickerが開きます。通常のローカル保存場所に空の新しい`WorkCairn`専用データフォルダを作成して選び、Web WizardでStarter Organizationを承認します。iCloud Driveは希望する場合だけ選べる任意の保存先で、推奨はしません。`AI Connections`はMacのnative hidden-inputからClaudeをmacOS Keychainへ接続し、RoutingはAutomaticのまま使用します。`会社を始める`から最初の依頼へ進みます。別デバイスから接続したい場合は[iPhone Local Web UI](#iphone-local-web-ui)を参照してください。

選択したpathはmacOS Application Supportへprivate local configとして保存され、再起動後に再検証されます。既存の個人Obsidian Vault、home、iCloud Drive root、別用途の非空directoryは受け入れません。同じデータフォルダへ書くdaemonは1つだけです。

## Temporary Vaultを準備する（開発・自動test）

Acceptanceでは空の試用専用directoryを選びます。実Vaultのpathへ置き換えないでください。

```bash
beta_vault=$(mktemp -d)
```

daemon起動後、First-run Wizardが保存場所、最初のAIチーム、AI Connectionを順番に表示します。`最初のAIチームを確認`から明示承認すると、選択済みdirectoryだけへWorkCairn layoutとProduct Manager、Content Writer、QA Engineerを既存Organization writerで作成します。承認前はfileを作らず、既存fileを上書きしません。

`--vault`はdevelopment Acceptance／automated test用の明示overrideです。選択済みの一般利用pathを変更せず、保存もしません。

## Providerなしでfirst-run

```bash
bin/workcairn-daemon --vault "$beta_vault"
```

ブラウザで`http://127.0.0.1:8787/`を開き、First-run Wizard、`/healthz`、`/readyz`を確認します。credentialがなくてもStarter Organizationの明示セットアップとread-only inspectionは可能です。Providerが必要な操作は安全に拒否されます。

## iPhone Local Web UI

これは任意機能です。実施しなくても、Mac loopbackだけで一般UIの正式経路が完結し、Public Beta GOの前提ではありません。MacとiPhoneを同じ信頼できるWi-Fiへ接続します。

```bash
bin/workcairn-daemon --vault "$beta_vault" --local-network
```

terminalのURLをiPhone Safariで開き、pairing codeを入力します。URLやcodeは公開せず、process終了後に再利用しません。iPhoneでは`My Actions`が既定で、必要な質問・承認・Recoveryだけを表示します。対応不要なら、対応が必要な項目がないことが画面から分かります。承認済み処理の実行中は小さなindicatorだけを表示し、failureはMy ActionsとTimelineから消えません。UIへ到達するだけならProvider設定は不要です。

Mac／iPadでは`Company View`が既定です。AI社員、Maker、Reviewer、Revision、担当中の仕事とhandoffをread-onlyで確認できます。表示はOrganization／Interaction／evidenceのprojectionであり、画面からTask状態を推測変更しません。

一般UIの正式経路は`First Run → 自然言語依頼 → 質問回答 → 進め方承認 → Reviewed Workflow承認 → Task／Deliverable → Typed Review → 必要ならRevision／再Review → 完了 → Timeline／Proof of Work`の1本です。direct Task／Review／Revision、plain Workflow、Scheduler、WordPress等のoperator機能は通常画面に表示されません。

## Mock Provider Operator Checklist

source checkoutで次を実行します。

```bash
make public-beta-smoke
```

このtargetは実APIと実Vaultを使わず、次を自動確認します。

- temporary Vaultから通常Taskを実行し、DeliverableとAuditをcommit
- Request Changes、Revision Task、再Review、Accept、Command replay
- mobile HTTP Interactionで依頼、質問、回答、Plan承認、Workflow完了
- 一般daemonがInteraction経路以外の副作用Commandをdefault denyすること
- Provider呼出しがMock HTTP serverだけへ向くこと

actual daemonとbrowserを含むAcceptanceは初回だけ`make public-beta-browser-setup`でtest-only browserを用意し、`make public-beta-browser-gate`を実行します。ChromiumとWebKit iPhone viewportでpairing、polling、Request Changes／Revision、reload、daemon restart、FailureEnvelope表示を確認します。詳細は[PublicBetaBrowserAcceptance.md](PublicBetaBrowserAcceptance.md)を参照してください。

## 実Providerを使う前の確認

実Provider確認はPublic Beta公開者が別途行います。このrepository作業では実行しません。

- temporary Vaultとtest用Provider credentialだけを使う
- 一般利用ではMac native dialogからKeychainへ接続する。process環境はOperator testの明示overrideだけに使う
- `.env`へ保存しない
- Web UIでModel名を選ぶ必要はない。daemonはKeychain／起動時overrideを値を出さず検査し、未接続ならProviderを呼ぶ前にMy Actionsへ案内する
- read-only plan、request digest、Command ID、承認対象を確認する
- Plan生成1回、通常Task1件、Review1回に上限を設け、usageをProvider側でも確認する
- 終了後にcredentialを失効またはrotationし、terminal historyへ値が残っていないことを確認する

## Cleanup

daemonを`Ctrl-C`でgraceful shutdownし、必要な検証記録だけを残します。temporary Vaultは実運用へ昇格させず、不要ならOSの安全な方法で削除します。`running` Commandやpartial failureがあれば削除前に`recovery-inspect`で状態を記録してください。
