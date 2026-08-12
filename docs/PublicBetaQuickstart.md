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

## Temporary Vaultを準備する

次は試用専用directoryへ最小Organizationを作成します。実Vaultのpathへ置き換えないでください。

```bash
beta_vault=$(mktemp -d)
mkdir -p "$beta_vault/会社" "$beta_vault/社員" "$beta_vault/プロジェクト"
```

`会社/Workspace State.md`:

```markdown
# Workspace State

## Workspace Manager

| ID | Name | Role |
|---|---|---|
| MGR-001 | Beta Operator | Workspace Manager |

## 部署

| Department |
|---|
```

`社員/田中 美咲.md`:

```markdown
---
id: PLAN-001
department: 企画部
role: Product Manager
model: Claude Sonnet 5
status: 待機中
---

# 田中 美咲
```

`社員/伊藤 健太.md`:

```markdown
---
id: QA-001
department: 品質保証部
role: QA Engineer
model: Claude Sonnet 5
status: 待機中
---

# 伊藤 健太
```

最初にread-only検証します。

```bash
bin/workcairn organization-inspect --vault "$beta_vault"
bin/workcairn identity-validate --vault "$beta_vault"
```

## Providerなしでfirst-run

```bash
bin/workcairn-daemon --vault "$beta_vault"
```

ブラウザで`http://127.0.0.1:8787/`を開き、UI、`/healthz`、`/readyz`を確認します。credentialがなくても起動とread-only inspectionは可能です。Providerが必要な操作は安全に拒否されます。

## iPhone Local Web UI

MacとiPhoneを同じ信頼できるWi-Fiへ接続します。

```bash
bin/workcairn-daemon --vault "$beta_vault" --mobile
```

terminalのURLをiPhone Safariで開き、pairing codeを入力します。URLやcodeは公開せず、process終了後に再利用しません。iPhoneでは`My Actions`が既定で、必要な質問・承認・Recoveryだけを表示します。対応不要なら`Your company is working. No action needed.`と分かります。UIへ到達するだけならProvider設定は不要です。

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
- process環境へ`ANTHROPIC_API_KEY`と`WORKCAIRN_CLAUDE_PROVIDER_MODEL`を注入する
- `.env`へ保存しない
- Web UIでModel名を選ぶ必要はない。daemonは起動時設定を値を出さず検査し、未接続ならProviderを呼ぶ前にMy Actionsへ案内する
- read-only plan、request digest、Command ID、承認対象を確認する
- Plan生成1回、通常Task1件、Review1回に上限を設け、usageをProvider側でも確認する
- 終了後にcredentialを失効またはrotationし、terminal historyへ値が残っていないことを確認する

## Cleanup

daemonを`Ctrl-C`でgraceful shutdownし、必要な検証記録だけを残します。temporary Vaultは実運用へ昇格させず、不要ならOSの安全な方法で削除します。`running` Commandやpartial failureがあれば削除前に`recovery-inspect`で状態を記録してください。
