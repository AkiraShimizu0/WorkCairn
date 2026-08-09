# Changelog

このファイルはConventional Commitsの履歴から生成しています。変更内容は[Keep a Changelog](https://keepachangelog.com/ja/1.1.0/)の形式を参考に分類しています。

## [Unreleased]

### Added

- Go Only RuntimeへTask execution、Review、Revision、Project／Task、Organization／Identity、CEO planの通常製品経路を移行。
- immutable Deliverable／Review／Revision evidence、Event／Audit、明示Recovery、Command Ledgerを追加。
- version付きloopback HTTP daemon、one-shot Scheduler、redacted Notification／Metricsを追加。
- dependency順のReviewed Multi-task Workflowと、承認済みWordPress Action Adapterを追加。
- Operator Guide、binary version metadata、Go Only release archive／checksumを追加。
- 自然言語依頼、CEO質問回答、再plan、digest承認、Project／Task適用を継続するInteraction Sessionを追加。
- Interaction Sessionから既存Reviewed Workflowを決定的child Commandとして実行し、Review／Revision結果をdigest付きtyped summaryで記録。
- completed Interaction Sessionから明示Deliverableを既存WordPress Actionへ別承認で引き渡す任意handoffを追加。
- Interaction stateから次の質問、承認、operation、Recovery参照を導出するread-only next-action projectionを追加。
- `workspace-daemon`へiPhone基準のLocal Web UI、trusted-LAN pairing、Reviewer inventory、Task／Deliverable／Review evidence inspectionを追加。
- mobile Interaction commandへbounded acceptanceとLedger pollingを追加し、受理後の実行をSafari接続から分離。

### Security

- daemonは既定で非loopback bindを拒否し、明示mobile modeだけprivate／link-local IP、process-local pairing、same-origin effect requestを許可。
- Provider／Action credentialをRuntime environmentへ限定し、Command／Schedule／evidenceから除外。

## [0.1.0] - 2026-08-07

### Added

- Workspace OSのPythonパッケージ、uv設定、テスト基盤を追加（`5e80558`）。
- Obsidian連携によるOrganization、Employee、Recruiterと社員ID重複防止を追加（`b6eef13`）。
- Project.md、Tasks.md、Decisions.md、Progress.mdを管理するProjectManagerを追加（`82f0d9a`）。
- Worker、ModelRouter、TaskExecutor、Fake Runner対応の実行パイプラインを追加（`d49ad05`）。
- ClaudeRunnerの入出力トークン数と実行統計を記録（`e6a485b`）。
- 別AI社員が成果物を評価するReviewerWorkerを追加（`32e9f60`）。
- レビュー実行のバックアップ、Audit Log、失敗記録を追加（`8a58956`）。
- 構造化レビューJSONとRevisionTaskServiceによる修正フローを追加（`bfb914c`）。
- 既存レビューを保持するバージョン付き構造化レビューを追加（`bf1cd27`）。
- 構造化レビューから重複なく修正タスクを正式作成する機能を追加（`b688489`）。
- 1タスク分の実行・レビュー・修正を調整するWorkflowEngineを追加（`6af4879`）。
- 社員名の重複・類似・形式を検査するIdentityPolicyを追加（`f2958f8`）。
- バックアップ、原子的更新、ロールバックを備えたEmployeeRenameServiceを追加（`629f082`）。

### Changed

- Worker内のプロンプト生成をPromptBuilderへ分離し、会社・社員・プロジェクト・タスクのコンテキストを統合（`64dc3b9`）。

### Fixed

- `claude-sonnet-5`へ未対応の`temperature`を送信しないモデル別リクエスト設定へ修正（`f8e2919`）。
- Identity診断と社員生成PromptへWorkspace Managerおよび予約済み組織IDを含めるよう修正（`af58ad4`）。
