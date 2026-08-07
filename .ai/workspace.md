# Workspace OS — AI Workspace Context

このファイルは、Workspace OSへ参加するAIエージェント向けの短い入口です。詳細な規範は`AGENTS.md`、`docs/CONSTITUTION.md`、設計は`docs/Architecture.md`と`docs/adr/`を参照してください。

## Mission

Workspace OSは、会社のProject、Task、AI社員、Workflow、Eventを管理するWorkspace Kernelを構築するプロジェクトです。最終形はGoのみで動作し、Pythonは移行期間中のlegacy/reference、Adapter、LLM連携としてのみ維持します。

## Current State

- リリース済みのPython v0.1.0は、Obsidian Vault、AI社員、Project、Task実行、Review、Revisionを扱います。
- Go Core v0.3系は、Workspace Kernelを中心にProject、Workflow readiness、Event、Task lifecycle、Worker/Runner境界、Approval/Execution Policyを実装済みです。
- Kernelの標準ライフサイクル順は`Event → Task → Worker → Execution`、停止時は逆順です。
- PythonとGoの移行境界はJSON Contract v1です。対応operationは`project.next_task_id`、`project.validate_task`、`project.can_transition`、`workflow.readiness`です。
- 共有fixtureは`fixtures/go_core/`、`fixtures/project/`、`fixtures/workflow/`にあります。
- 現在の詳細と次の移行対象は`docs/Architecture.md`と`docs/ROADMAP.md`を正とします。

## Architecture at a Glance

```text
Runtime / CLI / Adapter
        ↓
Workspace Kernel
    ├── ProjectService   → Project Domain
    ├── WorkflowService  → Workflow Domain
    ├── ExecutionService → ApprovalPolicy / ExecutionPolicy
    ├── TaskService      → Task Domain / TaskStore / EventService
    ├── WorkerService    → PromptBuilder / RunnerRegistry / Runner
    └── EventService     → in-process Event Bus
```

Go CoreはObsidian、Python runtime、CrewAI、LLM SDK、`.env`、APIキーへ依存しません。Provider設定、Vault I/O、CLI入力などはRuntime／Adapterの責務です。

## Repository Map

- `go/internal/*`: Go Domain、Service、Kernel、Adapter境界。新しい中核ルールの実装先
- `go/cmd/workspace-core`: JSON Contract v1を公開するCLI
- `src/workspace_ai/*`: 移行期間中のPython legacy/reference、Vault Adapter、Prompt、Provider Runner
- `tests/`: Python互換性・Adapterテスト
- `fixtures/`: Python／Go共有契約データ
- `docs/adr/`: Acceptedな設計判断
- `docs/CONSTITUTION.md`: 変更時に守る不変条件
- `docs/ROADMAP.md`: 移行状況と次の優先順位

## Standard Validation

```bash
make go-build
cd go && go test ./...
cd go && go test -race ./...
cd go && go vet ./...
PYTHONDONTWRITEBYTECODE=1 .venv/bin/python -m unittest discover -s tests
PYTHONPYCACHEPREFIX=/tmp/workspace-os-pycache .venv/bin/python -m compileall -q src tests
```

ドキュメントだけの変更では、リンク、差分、機密ファイル非変更を確認し、必要性に応じてテスト範囲を調整します。

## Safety Defaults

- 実Vault、`.env`、APIキー、社員・実Projectデータは、依頼に明記がない限り変更しません。
- 実LLM APIは明示的な許可なしに呼びません。
- 状態変更、外部公開、push、採用、タスク実行には明示的な承認を要求します。
- 既存の未コミット変更を消去・上書きしません。
- Pythonへの新しい中核ビジネスルール追加と、暗黙fallbackは禁止です。

## Definition of Done

1. 変更前の状態と関連ADRを確認した。
2. Domain、Service、Kernel、Adapterの境界を維持した。
3. 失敗、partial failure、並行実行、context cancellationを必要に応じて検証した。
4. Go、Python、fixture、JSON Contractの互換性を変更リスクに応じて確認した。
5. 実Vault、秘密情報、無関係なファイルに差分がない。
6. `git diff --check`と`git status`で最終差分を確認した。
