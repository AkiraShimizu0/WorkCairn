# Public Beta Quickstart

## 目的

第三者が実Vaultや実Providerを誤って使用せず、install、first-run、Local Web UI、Mock Provider E2Eを確認するための手順です。

候補versionは`v1.0.0-beta.1`です。最初の正式サポート対象はmacOS／arm64です。

## Clean environmentからinstall

### 配布archive

1. OS／architectureに一致するarchiveと`.sha256`を同じdirectoryへ置く。
2. checksumを検証する。
3. 新しいdirectoryへ展開する。
4. 3 binaryのversionがarchive名、`VERSION`、Release noteと一致することを確認する。

```bash
shasum -a 256 -c workcairn_v1.0.0-beta.1_darwin_arm64.tar.gz.sha256
tar -xzf workcairn_v1.0.0-beta.1_darwin_arm64.tar.gz
cd workcairn_v1.0.0-beta.1_darwin_arm64
bin/workcairn version
bin/workcairn-daemon --version
bin/workcairn-core --version
```

Linuxではchecksum確認に`sha256sum -c`を使用できます。

### Source checkout

```bash
git clone <repository-url> workcairn
cd workcairn
make v1-release-gate
```

Go 1.23以上だけを使用します。別言語runtimeやpackage managerは不要です。

## 一般ユーザーのmacOS First-run

通常利用ではterminalでVault path、Employee ID、Role、Model IDを設定しません。

```bash
bin/workcairn-daemon --mobile
```

初回だけnative folder pickerが開きます。iCloud Drive内に空の`WorkCairn`専用folderを新規作成して選び、Web WizardでStarter Organizationを承認します。`AI Connections`はMacのnative hidden-inputからClaudeをmacOS Keychainへ接続し、RoutingはAutomaticのまま使用します。iPhoneにはcredential入力面を出しません。`会社を始める`から最初の依頼へ進みます。

選択したpathはmacOS Application Supportへprivate local configとして保存され、再起動後に再検証されます。既存の個人Obsidian Vault、home、iCloud Drive root、別用途の非空directoryは受け入れません。同じVaultへ書くdaemonは1つだけです。

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

MacとiPhoneを同じ信頼できるWi-Fiへ接続します。

```bash
bin/workcairn-daemon --vault "$beta_vault" --mobile
```

terminalのURLをiPhone Safariで開き、pairing codeを入力します。URLやcodeは公開せず、process終了後に再利用しません。iPhoneでは`My Actions`が既定で、必要な質問・承認・Recoveryだけを表示します。対応不要なら`Your company is working. No action needed.`と分かります。承認済み処理の実行中は小さなindicatorだけを表示し、failureはMy ActionsとTimelineから消えません。UIへ到達するだけならProvider設定は不要です。

Mac／iPadでは`Company View`が既定です。AI社員、Maker、Reviewer、Revision、担当中の仕事とhandoffをread-onlyで確認できます。表示はOrganization／Interaction／evidenceのprojectionであり、画面からTask状態を推測変更しません。

## Mock Provider Operator Checklist

source checkoutで次を実行します。

```bash
make public-beta-smoke
```

このtargetは実APIと実Vaultを使わず、次を自動確認します。

- temporary Vaultから通常Taskを実行し、DeliverableとAuditをcommit
- Request Changes、Revision Task、再Review、Accept、Command replay
- mobile HTTP Interactionで依頼、質問、回答、Plan承認、Workflow完了
- Provider呼出しがMock HTTP serverだけへ向くこと

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
