# Workspace OS Constitution

## Status

- Version: 1.0
- Effective: 2026-08-07
- Scope: Workspace OSの設計、実装、テスト、運用データ変更

この文書はWorkspace OS開発の不変条件を定めます。具体的な設計判断はADR、現在構造は`Architecture.md`、実施順序は`ROADMAP.md`で管理します。矛盾する場合は、明示的に改定されない限り、このConstitutionとAccepted ADRを優先します。

## Article 1 — Go Core Is the Single Source of Business Rules

Workspace OSの最終形はGo 100%、Python 0%です。Project、Workflow、Policy、Execution、Task、Event、Worker、Organization、Scheduler、Auditなどの新しい中核ビジネスルールはGoへ実装します。

Pythonは移行期間中のlegacy/reference、Adapter、Prompt、Provider Runner、LLM SDKとしてのみ維持します。暗黙fallbackやPythonとGoへの新規ルール二重実装は禁止します。

## Article 2 — Kernel Coordinates; Domains Decide

Workspace KernelはService登録、起動・停止、Command受付、実行調停の中心です。Kernel自身に大量のビジネスルール、保存形式、Provider設定を持たせません。

Domainは決定的な型とルール、ServiceはDomainとportの調停、Adapterは外部I/Oと技術固有処理を担当します。この境界を越える変更には明確な理由とテストが必要です。

## Article 3 — Core Must Be Provider- and Storage-Neutral

Go CoreはObsidian、Markdown、CrewAI、Python runtime、Anthropic、OpenAI、Gemini、Ollama、`.env`、APIキーへ依存しません。

Vault、Filesystem、Database、CLI、HTTP、LLM Providerは交換可能なAdapterまたはRuntime dependencyとして接続します。現在のObsidian Vaultは運用データの正ですが、Coreはその保存形式を知りません。

## Article 4 — Stable Typed Contracts

コンポーネント境界は明確なGo型、interface、Event、version付きJSONで表現します。Python移行境界および外部process境界はJSON Contract v1を維持します。

契約変更は後方互換な追加を優先します。破壊的変更には、新しいcontract version、migration plan、共有fixture、ADRが必要です。内部エラー文字列やProviderの生エラーを公開契約にしません。

## Article 5 — Explicit Approval Before Effects

Task実行、外部API呼び出し、採用、状態変更、外部公開などの副作用には明示的承認が必要です。承認なしの場合、Task Start、Worker、Event、永続化を開始しません。

dry-runまたはplan-only APIがある場合は実行前確認に利用します。Approval PolicyはWorkerやRunnerから独立させ、将来の人間承認や設定駆動Policyへ差し替え可能にします。

## Article 6 — Task State Has One Owner

Task状態の正本はGo Task Domain、状態変更の唯一の入口はTaskServiceです。正式状態と遷移はAccepted ADRに従います。

Worker、Runner、Execution、AdapterはTask状態を直接書き換えません。Task lifecycle Eventの唯一の発行元もTaskServiceです。Failという実行事実とHoldというPolicy判断を混同しません。

## Article 7 — Events Represent Facts, Not Log Files

Business Eventは会社内で起きた事実です。Event DomainはAudit形式や永続化先を知りません。Audit、Scheduler、Notification、Metricsはsubscriberとして接続します。

初期配送はin-process、synchronous、at-most-onceです。永続配送、retry、Outboxを追加する場合も、Domain EventとTransport／Store Adapterを分離します。

## Article 8 — Failure Must Remain Observable

失敗や部分成功を成功として隠しません。Stage、ErrorKind、保存済み状態、Event配信結果を型付きResult／Errorで返します。

Store更新後のEvent失敗、Worker失敗後のFail/Hold失敗、timeout、cancellationを区別します。自動retryやrollbackで状態を推測せず、保証できない状態を明示します。

## Article 9 — Concurrency Is a Domain Concern

同一Taskの二重実行はTask VersionとStoreのcompare-and-setで防ぎます。process内だけの偶発的lockを永続的な正しさの根拠にしません。

並行利用するService、Store、Registry、Event Busはrace test可能な設計とします。将来のCommand retryには永続Idempotency／Command Ledgerを導入し、CASと役割を分けます。

## Article 10 — Identity and Auditability

社員名は表示情報、社員IDは永続参照です。Task、Event、Review、Revision、AuditはIDで関連付けます。改名してもIDと過去の監査証跡を維持します。

成果物、Review、Decision、Auditなどの過去記録は、明示されたmigrationを除き無差別に書き換えません。

## Article 11 — Verification Is Part of the Change

変更は、そのリスクに見合う自動テストと検証を伴います。Go Core変更では通常テスト、race test、`go vet`、`gofmt`を基本とします。移行境界変更ではPython互換テストと共有fixtureも維持します。

テストはFake Runner、Mock、InMemory Store、temporary directoryを使用し、実APIや実Vaultへ接続しません。検証不能な変更は、未確認事項と安全な次手を明示します。

## Article 12 — Protect User Data and Secrets

`.env`、APIキー、実Vault、社員・実Projectデータは既定で変更禁止です。必要な変更は対象、バックアップ、rollback、承認を先に確認します。

秘密情報は値だけでなく、一部、長さ、fingerprintも表示しません。生成物、一時ファイル、バックアップをGit管理へ混入させません。

## Amendment Process

Constitutionの原則を変更する場合は、次をすべて満たします。

1. 変更理由、代替案、移行影響を記載したADRを作成する。
2. Constitution、Architecture、Roadmap、関連契約を同じ変更で整合させる。
3. 後方互換性、データmigration、rollback、テスト計画を明示する。
4. 人間の明示承認を得る。

単なる実装詳細はConstitutionを改定せず、ADRまたはArchitectureで扱います。
