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
- `workcairn-daemon`へiPhone基準のLocal Web UI、trusted-LAN pairing、Reviewer inventory、Task／Deliverable／Review evidence inspectionを追加。
- mobile Interaction commandへbounded acceptanceとLedger pollingを追加し、受理後の実行をSafari接続から分離。
- `v1.0.0-beta.1`のversion source、macOS／Linux build matrix、temporary Vault + Mock Provider smokeを追加。
- Public Beta Quickstart、Security Policy、Contributing guide、native実機項目を分離したRelease Checklistを追加。
- WorkCairnのLiving Company Dashboardとして、iPhone既定のMy ActionsとPC／iPad既定のCompany Viewを追加。
- Workflow承認範囲を固定するAutonomy Contractと、確定記録から再構成するProof of Work／CEO Attentionを追加。
- Goal（会社の継続的な達成目標）を、Responsibility／Planningから独立した最小domainとして追加。
- Responsibility（継続的な担当領域と成果）を、単一ownerへのBindingと有効化状態つきで追加。
- Responsibilityの明示instructionから既存のCEO Plan生成を再利用するResponsibility Planningを追加。
- Responsibilityへ紐づくdaily／weekly Routineと、既存Scheduler経由の自動Plan生成を追加。
- Routineの次回発火が確実に予約されるよう、冪等なSchedule再構成と手動repair操作を追加。
- 対応が必要な質問・承認・Routine健全性を一覧化するCompany Attention／Decision Feedと、Company View上のAttention表示を追加。
- 無人（headless）daemon起動時にProvider credential sourceを`environment`／`keychain`／`headless-local`から明示選択できるHeadless Credential resolutionを追加。

### Fixed

- Plan生成結果のSummary／Task Title／RationaleがProviderから仮の値（`placeholder`等）で返された場合に、その内容を保存せず明示的に拒否するよう修正。

### Changed

- Public Beta前の正式製品名をWorkCairnとし、binary、Go module、archive、固有環境変数、現行docs／UIをrename。
- Public positioningを「自分専用のAI会社へ仕事を任せ、必要な判断だけ行う」体験へ統一。

### Removed

- Public Beta前に旧compatibility package、console entry point、tests、package metadata、lockfile、Provider SDK依存、専用build／release toolingを撤去し、repositoryとdistributionをGo Onlyへ確定。

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
