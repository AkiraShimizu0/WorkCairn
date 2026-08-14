# ADR-0038: macOS First-runをLocal OS Adapterへ閉じ込める

## Status

Accepted

## Context

ADR-0037で、選択済みの専用directoryを明示Commandで初期化する境界は成立しました。しかしPublic Betaの一般利用では、利用者がFinderでdirectoryを作成し、`--vault`とProvider credentialをterminalからdaemonへ渡す必要が残っていました。trusted LAN上のiPhoneからabsolute pathやsecretを送るendpointを追加すると、平文LANへ機密入力面を広げ、Vault／Provider-neutral CoreへOS固有責務が漏れます。

## Decision

macOSの配布daemonは`--vault`未指定時に、Runtime起動edgeからネイティブfolder pickerを開きます。pickerはiCloud Driveを推奨開始位置としますが、保存先を確定、探索、作成しません。利用者が明示的に作成または選択した、空の専用directoryか既存WorkCairn rootだけを受け入れます。home、filesystem root、iCloud Drive root、WorkCairn markerのない非空directoryはdefault denyで拒否します。既存の個人Obsidian Vaultを探索・変更しません。

選択結果はmacOS Application Support配下のWorkCairn local configへatomic replacement、mode `0600`で保存します。このfileはRuntime composition用のlocal path referenceであり、Vaultのcanonical business evidenceではありません。`--vault`はautomated test、development Acceptance、Linux等の明示overrideとして残し、保存済み選択を更新しません。daemon再起動時は保存済みpathを再検証してから同じVaultをcomposeします。

Claude connectionはADR-0036を拡張し、Mac本体からの明示操作だけがネイティブhidden-inputを起動します。secretはHTTP request、browser、localStorage、Vault、Command、Audit、logへ渡さず、macOS Keychainのgeneric passwordとして保存します。daemonは起動時にprocess environmentの明示overrideを優先し、それがない場合だけKeychainから読みます。接続後はRuntime edgeのProvider configだけを更新し、Core／Interaction／Workflowへcredentialを渡しません。

Keychainへの具体的な永続化はADR-0044に従います。`security` CLIの対話PTYは使用せず、bounded native helperがSecurity.frameworkを直接呼び、anonymous socketだけでsecretを受け取ります。macOS releaseはSecurity.frameworkをlinkするnative cgo buildとします。

local setup endpointはpathやcredentialの値をpayloadに持ちません。daemonを実行しているMac自身からのsame-origin intentだけを許可し、paired iPhone等の別hostは拒否します。iPhoneはredactedなConnected／Setup requiredだけを読み、Macで設定する次Actionを表示します。Finder revealも同じMac-only境界で、明示button操作時だけ行います。

Starter Organizationは引き続きADR-0037の`workspace.setup`、既存Organization writer、明示承認を通します。folder picker、Keychain、FinderはAdapter／Runtime edgeであり、Task状態、Event、Plan、Review、Revision、canonical evidenceを知りません。

iCloud Driveは同期transportであり、multi-writer coordinationではありません。同一Vaultは1つのWorkCairn daemonだけがwriteし、既存atomic replacement、file lock、Version／CASを維持します。Obsidianは同じMarkdown／JSON directoryを人間が閲覧する任意clientで、canonical metadataを所有・修復しません。

## Consequences

- macOS利用者はVault path、社員Markdown、Employee ID、Role、Model IDをterminalで用意せずFirst-runを完了できます。
- iCloud directory作成、Keychain登録、Obsidianで開く操作はいずれも利用者の明示操作であり、testやdaemonが勝手に実行しません。
- secretとabsolute pathをtrusted LANへ公開せず、iPhoneはSetup required時にMacへ誘導されます。
- Application Support configが破損、pathが消失、directoryが別用途へ変化した場合は推測修復せず起動を拒否します。
- Linux等では同じinterfaceの別Adapterを追加できます。現時点ではnative picker／Keychainがないため明示`--vault`を要求します。
- ADR-0037のsetup commit ordering、Approval、partial failure、thin-client projectionは変更しません。ADR-0037で未実装としたfolder picker／Keychainを本ADRが追加します。
