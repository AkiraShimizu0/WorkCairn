# ADR-0001: GoをWorkspace OSの中核実装とする

## Status

Accepted

## Context

Workspace OSはPythonで開発を開始し、Obsidian連携、AI Runner、プロジェクト管理、ワークフローを実装してきました。長期運用では、ドメインルールの一貫性、配布の容易さ、実行時依存の削減、並行処理と常駐プロセスへの適性が必要です。PythonとGoに同じビジネスルールを持たせ続けると、実装差異と保守コストが増加します。

## Decision

Workspace OSの中核実装はGoとします。Project、Task、Workflow、Organization、Scheduler、監査イベントなどのドメインモデルとビジネスルールを段階的にGo Coreへ移行します。

Pythonは移行期間中のObsidian Adapter、LLM連携、比較用legacy実装としてのみ維持します。各機能のGo移行と代替Adapterの整備が完了した時点で、PythonランタイムとPython実装をWorkspace OSから完全に廃止します。新しいビジネスルールは原則としてGoへ実装します。

## Consequences

- ドメインルールの正本がGoへ一本化されます。
- PythonからGoへの移行期間中はJSON契約と共有fixtureによる同等性確認が必要です。
- Prompt、Runner、LLM SDK、Markdown I/Oにも最終的なGo実装またはGoから利用できる代替境界が必要です。
- Python fallbackは移行のための一時的な仕組みであり、暗黙的には使用しません。
- 単一バイナリ配布、型安全なドメインモデル、並行処理への移行が容易になります。
