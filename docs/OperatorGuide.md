# WorkCairn Operator Guide

## 目的と安全境界

このGuideは、Go Only製品Runtimeを初めて動かすoperatorが、意図しないVault変更や外部公開を避けながらplan、approval、execute、inspect、recoveryを行うための手順です。

WorkCairnのread-only操作とwriter操作は分離されています。`*-plan`、`*-inspect`、`identity-validate`は承認やProvider credentialを必要としません。`*-execute`、`*-create`、`*-apply`、`action-wordpress-publish`は対象とplanを人間が確認した後にだけ明示承認します。

次を運用上の不変条件とします。

- 最初は実運用Vaultのcopyまたは新しいtemporary Vaultを使う。
- writer実行前にVault全体をWorkCairnとは別の方法でsnapshotする。
- planでVault root、Project／Task／Employee ID、source digest、Command IDを確認する。
- 同じ論理操作の再送には同じCommand IDと同じrequestを使う。内容が変わる場合は新しいCommand IDを使う。
- `running`、partial failure、stale Versionを推測で再実行しない。
- `.env`をGo Runtimeへ読ませず、credentialは承認済みprocessの環境へだけ注入する。
- canonical artifact、Deliverable、Review JSON、Revision intent、Action evidenceを自動削除・上書きしない。

## 配布物の確認

配布archiveはallow-listされたGo Only artifactです。展開前に同梱checksumを検証し、実行binaryのversionを確認します。初回導入だけを短く確認する場合は[PublicBetaQuickstart.md](PublicBetaQuickstart.md)を先に参照してください。

```bash
shasum -a 256 -c workcairn_v1.0.0-beta.1_darwin_arm64.tar.gz.sha256
tar -xzf workcairn_v1.0.0-beta.1_darwin_arm64.tar.gz
workcairn_v1.0.0-beta.1_darwin_arm64/bin/workcairn version
workcairn_v1.0.0-beta.1_darwin_arm64/bin/workcairn-daemon --version
workcairn_v1.0.0-beta.1_darwin_arm64/bin/workcairn-core --version
```

Linuxでは`sha256sum -c`を使用できます。`version`結果のrelease versionとcommitをRelease noteに記録します。sourceからbuildする場合はGo 1.23以上で`make go-build`を使用します。別言語runtimeやpackage managerは不要です。

## Temporary Vaultでの初回確認

1. 実Vaultとは別の空directoryを作る。
2. `会社`、`社員`、`プロジェクト`directoryと、最小の`会社/Workspace State.md`を用意する。
3. Employee Markdownを追加し、`organization-inspect`と`identity-validate`を実行する。
4. `project-bootstrap-plan`で作成対象を確認する。
5. temporary Vaultだけを対象に、同じ入力で`project-bootstrap-execute --approved --command-id ...`を実行する。
6. `Tasks.md`の5列表、managed metadata、Audit Log、Command Ledgerを確認する。

実運用Vaultをapproved Vaultへ昇格する条件は、identity validationが成功し、既存5列表を使うProjectではADR-0008 migrationが完了し、外部backupから復元できることをoperatorが確認した状態です。WorkCairn自身はVault backup製品ではありません。

## 通常運用

安全な基本順序は次のとおりです。

```text
read-only plan
→ 対象identity・Version・source digest・予定副作用を確認
→ 明示承認と一意なCommand ID
→ execute
→ terminal resultとcanonical evidenceを確認
→ Notification／Auditを確認
```

自然言語依頼を継続して扱う場合はInteraction Sessionを使います。`interaction-start-plan`のrequest digestを確認してstartし、Provider呼出し前にSession Versionを承認します。`clarification_required`では表示された全質問へ`interaction-answer`で回答し、再度`interaction-plan-generate`します。`plan_approval_required`になった最新plan digestだけを`interaction-plan-apply`へ渡します。

各段階では`interaction-next`をread-onlyで実行すると、次のoperation、expected Version、必要field、質問、承認要否が得られます。attention stateではouter／child Command参照を返しますが、Recoveryや再実行を自動開始しません。

Sessionは回答と承認を調停するevidenceであり、Project／Taskの正本ではありません。Provider成功後またはProject／Task commit後にSession CASが失敗した場合、既存効果を削除せずworkspace Command Ledgerと`interaction-inspect`を確認します。同じCommand IDで推測再実行せず、自動adoptionもしません。

Project適用後は`interaction-workflow-plan`でreviewer ID、Task上限、Session Version、次stepに拘束されたdigestを確認し、`interaction-workflow-execute`へ渡します。実行は既存Reviewed Workflowを再利用します。SessionのWorkflow turnは完全Resultのdigestとchild Command IDを保持するため、詳細はproject scopeのWorkflow Command Ledgerと各Deliverable／Review／Revision evidenceで確認します。`blocked`／`limit_reached`は最新Versionで再planし、`workflow_attention_required`は自動再開せずRecoveryを先に確認します。

外部公開が必要な場合だけ、completed Sessionの`interaction-action-wordpress-plan`へWorkflow内のTask ID、logical target、prospective Command IDを渡します。表示されたsource digestとAction plan digestを別承認し、publishします。`action_attention_required`ではremote公開の有無を推測せず、outer／child LedgerとAction request／result evidenceを確認します。

自然言語の依頼は`ceo-plan-generate`でtyped planへ変換します。生成は適用ではありません。未知社員、循環dependency、未知fieldをGo validationで拒否した後、別の`ceo-plan-apply-plan`と明示承認でProject／Task writerへ渡します。

複数Taskでは`workflow-reviewed-plan`を先に実行します。実行経路はTask実行後にReviewし、Acceptなら次Taskへ、Request ChangesならRevision Taskを作成・実行して再Reviewします。上限、blocked、partial resultで停止した場合は自動resumeせず、outer／child Command Ledgerとcanonical evidenceを確認します。

## Loopback daemon

```bash
bin/workcairn-daemon \
  --vault /absolute/path/to/temporary-or-approved-vault \
  --listen 127.0.0.1:8787
```

既定daemonはloopback addressだけを受け付けます。`0.0.0.0`、空host、非loopback IPは拒否します。reverse proxyを置いてremote公開する構成も現在のsupport対象ではありません。

### iPhone Local Web UI

iPhoneとMacを同じtrusted Wi-Fiへ接続し、temporary／approved Vaultを明示して起動します。

```bash
bin/workcairn-daemon --vault /absolute/path/to/temporary-or-approved-vault --mobile
```

1. terminalに表示された`WorkCairn mobile UI`のURLをiPhone Safariで開く。
2. 同じterminalのpairing codeを入力する。Universal Clipboardでcopyしてもよい。
3. 自然言語の依頼を入力し、request digestを確認してSession開始を承認する。
4. 表示された質問へ回答し、Provider Plan生成、Plan適用、Reviewed Workflowをそれぞれ明示承認する。
5. iPhoneの`My Actions`では必要な判断だけ、Mac／iPadの`Company View`ではAI社員、Maker、Reviewer、Revisionのhandoffを確認する。
6. 完了後は`Project・Task・Reviewの詳細`からTask、Deliverable本文、canonical Reviewをread-onlyで確認する。
7. `確認が必要`では自動再送せず、表示されたouter／child Command IDを確認してRecovery手順へ進む。

承認後にSafariをbackgroundへ移しても、`202 Accepted`済みのInteraction commandはMac上で継続します。画面へ戻ると同じCommand IDのLedger状態を再取得します。daemon自体を終了した場合やMacがsleep／crashした場合は自動resumeせず、`running`／partial stateをRecovery手順で確認してください。

mobile modeは自動検出したprivate IPv4だけへbindします。複数network interfaceで違うaddressを選んだ場合は`--listen 192.168.x.x:8787`を明示してください。pairing code／cookieはprocess終了で無効になり、Vault、`.env`、Interaction Sessionには保存されません。

HTTPは暗号化されないため、信頼できない共有Wi-Fi、port forwarding、internet公開では使用しないでください。remote authentication、TLS、durable account、Push通知は未実装です。

- `/healthz`: processが応答できること
- `/readyz`: command handlerが構成済みであること
- `/v1/commands/{command_id}`: Ledger terminal／running／recovery requiredの確認
- `/v1/schedules`: one-shot Scheduleの確認
- `/v1/notifications`: payloadを持たないlocal Inbox
- `/v1/metrics`: process-local Event件数。再起動でreset

SIGINT／SIGTERMでは新規受付を止め、設定したtimeoutまで実行中commandを待ちます。timeoutやcrash後の`running`をdaemonは自動再開しません。

## Scheduler

Schedulerへは、将来の解釈用parameterではなく、承認済みの完全な`workspace-command.v1`を保存します。

1. `schedule-plan`でSchedule ID、絶対時刻、target Command全体を確認する。
2. `schedule-create --approved --command-id ...`でone-shot recordを作成する。
3. daemonの`/v1/schedules`とtarget Command Ledgerを確認する。

cron、recurrence、並列配送、`dispatching`の自動resume、target result adoptionは未実装です。

## NotificationとMetrics

NotificationはEvent type、Event ID、発生時刻などのredacted envelopeだけを保存します。Task本文、Prompt、Deliverable、Provider response、credentialは保存しません。Notificationは事実の正本ではなく、canonical artifactとAuditを見つけるためのlocal signalです。

Metricsはdaemon process内のEvent件数だけです。永続監視、SLA、token／cost計測ではありません。subscriber failureは成立済みcanonical factをrollbackせずpartial publication failureとして返します。

## Recovery

異常終了やpartial failureでは、まずread-only inventoryを取得します。

```bash
bin/workcairn recovery-inspect --vault /path/to/vault --project 'Project名'
```

自動回復できるのは、確定DeliverableとTask Versionに拘束された`complete_task`、またはDeliverableなしの中断Taskを失敗・保留に確定する`fail_and_hold_task`だけです。`recovery-plan`を保存し、直前に証拠とTask Versionを再検証する`recovery-apply --approved`を使います。

Review Markdown再生成、Revision Task adoption、Event replay、Action remote post reconciliation、temporary file削除は診断だけです。詳細は[Recovery.md](Recovery.md)を参照してください。

## WordPress公開

`action-wordpress-plan`は既存Deliverableを読み、source SHA-256とlogical targetを表示します。承認時はそのdigestを`action-wordpress-publish --source-sha256 ...`へ明示的に渡します。実行時にDeliverableが変わっていればProvider呼出し前に拒否します。

WordPress Base URL、username、application passwordは承認済みpublish processまたはdaemonの環境へだけ注入します。Command JSON、Schedule、Vault evidenceへcredentialを入れません。

現在はMarkdown本文をWordPress `content`へそのまま渡す単一post publishだけです。HTML変換、media upload、update、delete、remote lookup、retry、reconciliationはありません。remote publish後にlocal result保存が失敗した場合、postを自動削除せずpartial failureとして手動確認します。

## Upgrade／rollback checklist

1. 現行binaryの`workcairn version`と対象Vaultのbackup revisionを記録する。
2. Release checksum、CHANGELOG、JSON Contract／Vault schema互換性を確認する。
3. temporary copyで`organization-inspect`、`identity-validate`、主要read-only plan、`recovery-inspect`を実行する。
4. `make v1-release-gate`相当のRelease結果を確認する。
5. daemonをgraceful shutdownし、`running` Command／`dispatching` Scheduleがないことを確認する。
6. binaryを置換し、`version`、health、read-only inspectionを確認してからwriterを再開する。

binary rollbackは可能でも、新versionがcommitしたcanonical evidenceを旧versionへ合わせて書き換えてはいけません。未知schemaやmetadata versionを旧binaryが拒否した場合は、旧binaryで運用を続けずRelease migration手順を確認します。
