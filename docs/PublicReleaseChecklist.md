# Public Release Checklist

## Release artifact

- `make v1-release-gate`が実Vault、`.env`、実Providerなしで成功する。
- clean checkoutから`make release-package RELEASE_VERSION=vX.Y.Z BUILD_DATE=<RFC3339>`が成功する。
- archive名、`workspace-run version`、Git tag候補、CHANGELOGのversionが一致する。
- `.tar.gz.sha256`を別processで検証する。
- archiveにGo binary、LICENSE、README、CHANGELOG、Operator／HTTP／Recovery docsだけが含まれ、Python package、`.env`、Vault、cache、test outputを含まない。
- supported GOOS／GOARCHごとのtemporary directoryで展開・version smoke testを行う。

## Runtime safety

- temporary Vaultでplanがread-only、writerが明示承認必須であることを確認する。
- Command replay、異request conflict、stale Version／source digest拒否を確認する。
- daemonがloopbackだけへbindし、graceful shutdownすることを確認する。
- Notificationにpayload／secretがなく、Metricsがprocess-localであることを案内する。
- Recoveryが自動retry、Event replay、artifact adoptionを行わないことを案内する。
- WordPress Actionのremote成功後local failureが自動rollbackされないことを案内する。

## Compatibility and upgrade

- JSON Contract v1 shared fixtureとVault managed metadata fixtureが成功する。
- 公開Python v0.1 compatibility import／console script testが成功する。
- Go製品artifactがPython interpreter、Python SDK、python-dotenvを含まない。
- Python compatibilityの終了条件は`PythonRuntimeInventory.md`から変更しない。
- unknown metadata versionを暗黙解釈しない。schema変更があれば別version、migration、fixture、ADRを用意する。
- upgrade前backupと、old binaryへ戻してもcanonical dataを自動downgradeしない手順をRelease noteへ含める。

## Security and support boundary

- secret、`.env`、実Vault、private key、実Provider responseがGit／archive／fixtureにない。
- daemonは認証、TLS、authorizationがないためremote公開しない。
- WordPress credentialはRuntime environmentだけから注入し、Command／Schedule／evidenceへ保存しない。
- one-shot Schedulerだけをsupportし、cron／recurrence／automatic resumeを宣伝しない。
- synchronous HTTP、single-process Metrics、manual Recovery、single WordPress publishという現在の限界をRelease noteへ含める。

## Product name candidates

v1の技術artifact名は既存caller、docs、Go module、Vault運用語との互換性を優先し、当面`Workspace OS`を推奨します。一般公開名称の最終決定前には商標、domain、検索性、対象ユーザーへの理解度を別途確認します。

説明性を比較する候補：

| Candidate | 伝わる価値 | 主な注意点 |
|---|---|---|
| Workspace OS | 既存資産と連続し、AI組織・仕事実行基盤を広く表せる | 一般語に近く、検索性の確認が必要 |
| Work Operator | 承認後に仕事を調停・実行する体験が直接的 | 固有性と商標の確認が必要 |
| Agent Workspace | AI社員と人間可読Workspaceの組合せを表せる | 類似名称が多い可能性がある |

名称変更はcode-only判断では確定せず、決定時にbinary名、module path、CLI、Vault schema、公開Python APIのうち何を維持するかをmigration planとして分離します。
