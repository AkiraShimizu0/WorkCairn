# Security Policy

## Supported release

Public Betaでは、`VERSION`に記載した最新のpre-releaseだけをsecurity update対象とします。過去のbeta、source snapshot、未commit buildはsupport対象外です。

## Reporting a vulnerability

credential漏えい、承認回避、Vault root外への書込み、remote access、任意command実行などの疑いは、公開Issueへ詳細を書かないでください。repository公開時にGitHub Private Vulnerability Reportingを有効化し、Security tabから報告してください。有効化前はPublic Betaを開始しません。

報告には、秘密情報や実Vaultデータを含めず、影響するversion、再現条件、期待する安全境界、temporary fixtureでの最小再現を含めてください。

## Current security boundary

- `workcairn-daemon`は既定loopbackです。`--mobile`は信頼できる同一LANとprocess-local pairing専用で、TLSやremote authenticationはありません。
- port forwarding、public IP、reverse proxy、internet公開はsupportしません。
- `.env`は自動読込しません。Provider／External Action credentialは承認済みprocessの環境からだけ注入します。
- 実Vaultでの初回試用は推奨しません。temporary Vaultで確認後、外部backupを用意してください。
- automatic retry、artifact adoption、remote Action reconciliationは行いません。partial failureはRecovery手順で確認します。

詳細は`docs/OperatorGuide.md`と`docs/Recovery.md`を参照してください。
