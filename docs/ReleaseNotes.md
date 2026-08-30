# WorkCairn Release Notes

このfileはrelease ownerがGitHub Releaseの本文へそのまま利用できる、tag付き版ごとの下書きです。過去versionをここへ蓄積せず、公開直前にrelease ownerが最終確認して使用します。

## v1.0.0-beta.1（準備中）

### WorkCairnとは

WorkCairnは、自分専用のAI会社へ自然言語で仕事を依頼するlocal-firstな製品です。AI社員が計画、実行、独立Review、必要なRevisionを進め、人間には本当に必要な質問と重要な承認だけを返します。会社simulationではなく、誰が作り、誰がReviewし、必要なら誰が直しているかを見せる製品です。

### できること

- 自然言語依頼からtyped Planを生成し、質問回答後の明示承認でProject／Taskへ適用
- Task実行、別AI社員によるReview、Request Changes時のRevisionと再Review
- PC／iPad（既定）とiPhone（任意）のWeb UIで、会社の状態、担当、Maker → Reviewer → Revisionの流れを確認
- Workflow承認時に、今回AI会社へ任せる範囲をAutonomy Contractとして確認
- 成果物、独立Review、Revision、承認、外部Actionを保存済みの確定記録から確認
- 対応が必要な質問・承認・Routineの健全性をCompany Attention Feedで一覧表示
- 継続的な担当領域（Responsibility）と達成目標（Goal）、daily／weekly Routineによる定期Plan生成
- 承認前副作用ゼロ、Task Version／CAS、Command Ledger、partial failureの明示

### 対応環境

Public Betaの正式サポート対象は**macOS／arm64のみ**です。macOS／amd64、Linux／amd64、Linux／arm64はcross-buildで動作確認していますが、native smokeが未実施のためPublic Beta配布対象には含みません。Windowsはサポート対象外です（Vault file lock未対応）。

必要環境はGo 1.23以上、`make`、POSIX shell、`tar`です。配布archiveを使う場合、Go toolchainは不要です。

### 既知の制限

- remote authentication、TLS、internet公開、Push通知は未実装です。`--local-network`は信頼できる同一LAN専用です。
- daemonは既定でloopback bindのみを受け付けます。
- 外部secret manager、credential rotationの自動化は未実装です。credentialは対話的macOSでは通常Keychainから読み込み、明示`environment` source、unattended運用向けの明示`headless-local` sourceも利用できます。明示したsource間の暗黙fallbackはなく、`.env`は自動読込しません。
- Routineの実行頻度はdaily／weeklyのみで、cron形式の指定はできません。
- Company Attention Feedはv1として、質問・承認・Routine健全性など一部の情報源に限定されています。
- Scheduler、WordPress連携は運用者向け機能として存在しますが、Public Betaの一般UIでは非表示です。外部連携は今後段階的に追加します。
- 自然言語依頼の生成結果は品質評価を継続していますが、文章としての完成度そのものを保証するものではありません。

### セキュリティ

脆弱性の疑いがある場合は、公開Issueへ詳細を書かず、GitHub Private Vulnerability Reportingから報告してください。詳細は[SECURITY.md](https://github.com/AkiraShimizu0/WorkCairn/blob/v1.0.0-beta.1/SECURITY.md)を参照してください。

### サポート

- バグ報告・機能要望: GitHub Issues
- 質問: GitHub Discussions
- セキュリティ報告: GitHub Private Vulnerability Reporting（[SECURITY.md](https://github.com/AkiraShimizu0/WorkCairn/blob/v1.0.0-beta.1/SECURITY.md)）
- 上記以外の一般的な問い合わせも、当面はGitHub上に集約します。

### データの安全性

WorkCairn自身はbackup製品ではありません。実運用前に空の一時的なデータフォルダで確認し、別の方法でbackupを用意してください。詳細は[Operator Guide](https://github.com/AkiraShimizu0/WorkCairn/blob/v1.0.0-beta.1/docs/OperatorGuide.md)と[Recovery Guide](https://github.com/AkiraShimizu0/WorkCairn/blob/v1.0.0-beta.1/docs/Recovery.md)を参照してください。
