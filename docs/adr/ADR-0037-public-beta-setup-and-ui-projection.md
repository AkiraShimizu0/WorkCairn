# ADR-0037: First-run setupを明示CommandとしPublic Beta UIをread-only projectionに保つ

## Status

Accepted

## Context

Public Beta Acceptanceでは、空のtemporary directoryや専用Vaultを利用者がterminalから手作業で初期化し、Organization Markdownも個別に用意する必要がありました。またmobile clientは同じInteraction stateを5秒ごとに取得するたびにフォームや詳細を再生成し、未送信入力、選択、focus、開いている詳細を失うことがありました。長時間Commandの全画面表示と一時Toastだけのfailure表示も、「必要なときだけCEOを呼ぶ」という製品体験に反します。

一方、browserへVault path、Provider credential、Task lifecycleやReview規則を持たせること、既存の個人Obsidian Vaultを自動探索・変更すること、新しい履歴StoreをUIのために作ることは、既存のAdapter／Approval／canonical evidence境界を壊します。

## Decision

### First-run setup

daemonは、process起動時に明示された既存directoryだけをWorkCairn専用rootとして扱います。Web UIやHTTP payloadからabsolute pathを受け取らず、既存のObsidian Vault、iCloud Drive、home directoryを探索・変更しません。ADR-0038によりmacOSのnative pickerで空の専用directoryまたは既存WorkCairn rootを明示選択できます。

`GET /v1/workspace-status`は、rootがtemporary、dedicated local、iCloud Driveのどれか、WorkCairn layoutとStarter Organizationが準備済みかだけをredacted projectionとして返します。absolute path、Employee ID、logical model route、Provider設定を返しません。

Starter OrganizationはRuntime bootstrap dataとしてProduct Manager、Content Writer、QA Engineerの最小3 roleを提供します。これはOrganization Domainの既定社員ではありません。`workspace.setup`は通常の`workspace-command.v1`としてCommand IDと明示承認を必須にし、ADR-0021のworkspace Command Ledgerへ副作用前にclaimします。

commit順は次の通りです。

1. 選択済みroot直下のWorkCairn managed directoryと`会社/Workspace State.md`をatomic createする
2. 既存Organization inventoryを検証する
3. 不足するStarter Employeeを既存Employee hire commandの決定的child Commandとして順次commitする
4. terminal resultをLedgerへcommitする

既存fileは上書きしません。既存Workspace Stateの必要section欠落、Starter ID衝突、Employee hire失敗は推測修復せず拒否します。layoutまたは一部Employee commit後の失敗はpartial failureとして成立済みfileを保持し、自動rollbackしません。同一Command ID・同一requestは保存済みresultをreplayし、異なるrequestは拒否します。

iCloud Driveは同期transportであってwriter coordinationではありません。atomic replacement、file lock、Version／CASは各Macの既存Vault Adapter境界を維持しますが、同じVaultへ複数daemonが書く構成はsupportしません。Obsidianは人間可読Markdownを開くclientであり、canonical metadataの所有者にはなりません。

### Thin client render policy

Local Web UIはInteraction Session／Next Action、Organization、Work Report、Task evidenceをread-only投影します。一般向けの「進め方」と内部Plan、TimelineとSession turn、Company Viewとcanonical evidenceは別のsource of truthを作らず同じ保存済み事実から生成します。

同一Session ID、Version、Next Action kindのpollingではaction formを再生成しません。詳細とTimelineも入力となるevidence keyが変わらない限り再生成せず、入力中text、select、focus、開いている`details`を保持します。Session Versionまたはstageが進んだ場合だけ古い操作UIを閉じます。draftをserver、Vault、`localStorage`へ保存しません。

長時間Commandは受理後に小さいbackground indicatorへ切り替え、Ledger statusだけをpollします。clarification、approval、failure、partial failure、Recovery、daemon connection lossだけをMy Actionsの前面へ出します。

failure表示はToastだけにしません。Sessionのattention turn／Ledger failureを正とし、clientは現在取得済みのsanitized error code、stage、Command ID、Provider request IDだけをSession単位でbrowser storageへ表示cacheとして保持します。raw Provider response、message、credential、pathは保存しません。cacheはCommandやRecoveryの正本ではなく、Sessionが正常に次Versionへ進んだときだけ破棄できます。TimelineはSession／Workflow／Action evidenceとこの表示cacheを投影し、自動retryやrepairを開始しません。

## Consequences

- 空の選択済みdirectoryから、GUIの明示承認だけでWorkCairn layoutと最小Organizationを準備できます。
- directory選択とAI credential接続はADR-0038のMac側明示操作であり、trusted LANへpath／secret値を受けるAPIを増やしません。
- Starter OrganizationはCoreのbusiness ruleや既存Organizationの上書きにはなりません。
- pollingは表示状態を保ちますが、Session stage遷移時はserver stateを優先して古いdraftを閉じます。
- Background実行とpersistent errorは「必要なときだけCEOを呼ぶ」形になり、canonical evidence、Approval、Command Ledger、Recoveryの意味を変えません。
- folder pickerとOS Keychain接続はADR-0038で追加します。複数Mac writer coordinationは引き続き含めません。
