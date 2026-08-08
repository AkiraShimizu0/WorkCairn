# Python Runtime Inventory

## 目的

Go Onlyへの移行で、Pythonを改善するのではなく製品実行経路から到達不能にし、最後にpackageを削除するための現状表です。正本の製品入口はGo `workspace-run`です。

Pythonの既存module path、class、関数、`workspace-ai` entry pointはv0.1公開互換として維持します。directory移動は行わず、`workspace_ai.compatibility`のmanifest、module docstring、import contract testでcompatibility境界を固定します。新機能や新しいビジネスルールをこのsurfaceへ追加しません。

## 公開surface分類

| 分類 | module | 方針 |
|---|---|---|
| Go process Adapter | `workspace_run_gateway`、`go_core_client` | 公開callerをGo JSON v1 processへ接続する。legacy implementationをimportせず、fallbackしない |
| Compatibility orchestration | `workflow_engine` | Go gatewayを調停する。公開互換の明示的legacy aliasも受け付けるが、暗黙fallbackせず製品Runtimeには含めない |
| Legacy implementation | `task_executor`、`worker`、`prompt_builder`、`model_router`、`runners`、`reviewer`、`revision_task_service`、`project_manager`、`organization`、`employee`、`recruiter`、`employee_rename_service`、`ceo_command_service`、`manager`、`project_workflow_service`、`identity_policy`、`utils.obsidian` | import pathと既存挙動を公開互換用に凍結する。通常製品callerから利用しない |
| Reference contract | `review_result`と共有fixture | Go parity／Markdown互換の期待値だけを保持する |
| Package／CLI export | `workspace_ai:main`、`workspace-ai` | v0.1 compatibility placeholderとして維持する。製品入口にはしない |

正確なmachine-readable一覧は`src/workspace_ai/compatibility.py`を参照します。既存importを壊さないため、現時点で`workspace_ai.legacy.*`等への物理移動やre-export wrapper化は行いません。

## 製品経路

| 製品責務 | 正本 | Pythonの位置付け |
|---|---|---|
| CEO plan生成／適用 | `workspace-run ceo-plan-generate|ceo-plan-apply-*` | `WorkspaceRunCEOPlanGateway`／`WorkspaceRunCEOApplyGateway`は公開caller互換。legacy `CEOCommandService`のPrompt／writerへfallbackしない |
| 通常Task execution | `workspace-run plan|execute` | `TaskExecutor`、`Worker`、`PromptBuilder`、`ModelRouter`、`ClaudeRunner`は公開互換／reference |
| Review／Revision | `workspace-run review-*|revision-*` | `ReviewerWorker`、`RevisionTaskService`は公開互換／reference |
| Project／Task writer | `workspace-run project-bootstrap-*|task-create-*|project-dependencies-*` | `ProjectManager` writerは公開互換／reference |
| Organization writer | `workspace-run employee-*|organization-sync-*` | `Organization`、`Employee`、`Recruiter`、`EmployeeRenameService` writerは公開互換／reference |

`workspace-run`のGo packageはPython interpreter、Python module、CrewAI、Python SDKを起動・importしません。Provider通信はGo Claude Adapterだけがprocess edgeで行います。

## 残存Python caller

- `WorkflowEngine`は公開互換の薄い調停で、通常構成ではExecution／Review／RevisionのWorkspaceRun gatewayだけを呼びます。
- `CEOCommandService`は公開互換APIです。WorkspaceRun CEO gatewayを注入した通常構成ではGo validated planを返し、Go applyへ委譲します。
- `manager.py`の`ask_manager`と直接実行blockはv0.1 legacy sampleです。Go CEO plan製品入口からは到達しません。
- `recruiter.py`、`organization.py`の直接実行blockはlegacy sampleです。Go Organization製品入口からは到達しません。
- installed `workspace-ai` console scriptはv0.1 compatibility placeholderで、製品運用入口ではありません。

## Provider／Prompt依存

- `anthropic`: `manager.py`と`runners/claude_runner.py`の公開互換Provider実装だけが使用します。Go製品binaryには含まれません。
- `python-dotenv`: `manager.py`、`employee.py`、`organization.py`のv0.1 environment互換だけが使用します。Go製品binaryは`.env`を読みません。
- `crewai`: source importも公開API利用もないため削除済みです。
- Python `PromptBuilder`／`ReviewerWorker`の本文は共有fixtureのreferenceとしてのみ残します。新しいPromptルールは追加しません。

`anthropic`と`python-dotenv`は公開Python APIをそのまま動かすためPython compatibility distributionの依存に残します。Go製品のbuild、test、`workspace-run`実行にはインストール不要です。依存先moduleはcompatibility manifestとcontract testでallow-list固定し、Go process Adapterからimportしません。

## Test caller分類

- `test_workspace_run_gateway`、`test_workflow_engine`のgateway tests: 公開Python callerからGoへ到達する移行契約
- `test_python_compatibility_boundary`: import path、console script、legacy marker、Provider依存allow-list、legacy非importを固定
- Prompt／Review／Identity／Vault fixture tests: Go移行結果のreference契約
- `test_task_executor`、`test_worker`、`test_claude_runner`、`test_reviewer`等: 公開v0.1 API互換だけを確認するlegacy tests
- Go `go_only_release_gate_test`: 製品operationの存在と、全Go製品sourceがPython processを起動できないことをPythonなしで確認

## 契約資産として残すもの

- `fixtures/prompt/`: 通常Task／Review PromptのPython・Go parity
- `fixtures/ceo/`: CEO canonical Promptとtyped planのmigration parity
- `fixtures/organization/`: Identity／Organization policy parity
- `fixtures/vault/`: Markdown projectionとmanaged metadata互換
- Python reference tests: Prompt、Review result、Identity、legacy Markdown読取の期待値を固定するもの
- `workspace_run_gateway.py`: 公開Python callerからGo v1 responseへ変換する移行Adapter

## Python packageを削除できる条件

1. 公開Python APIと`workspace-ai` console scriptについて、廃止または別compatibility package化の方針を確定する。
2. Pythonでしか検証していないreference期待値を共有fixtureまたはGo contract testへ固定する。
3. `WorkflowEngine`／`CEOCommandService`を利用する外部callerがGo CLI／将来のGo APIへ移行済みであることを確認する。
4. `anthropic`／`python-dotenv`を必要とするlegacy moduleをmain packageから除去する。
5. Python testを削除してもGoだけでJSON Contract v1、Markdown compatibility、Prompt parityを検証できる。

物理削除対象は`LEGACY_IMPLEMENTATION_MODULES`全体、Python Go process Adapter、`workspace-ai` entry point、`anthropic`／`python-dotenv`、Python build backend、最後に`src/workspace_ai`とPython testsです。削除順は、reference期待値のGo fixture化、外部公開caller移行、Provider legacy、writer legacy、process Adapter、package metadataの順とします。

公開Python APIを壊さない現段階では、legacy moduleの即時削除や依存のoptional化は行いません。次の削除効果がある作業は、Python reference testの期待値を共有fixture／Go contractへ移し、公開互換packageを製品配布物から分離することです。

## 物理削除Releaseの確認手順

Python削除を行うReleaseでは、次を順番に確認します。

1. repository外を含む公開caller inventoryを取り、`workspace_ai.*` import、`workspace-ai` command、Python gateway利用がゼロ、または別compatibility packageへ移管済みであることを記録する。
2. Prompt、Review、CEO plan、Organization、Vault projection、JSON v1の期待値が共有fixtureとGo testだけで検知できることを確認する。
3. `workspace-run`の全capability E2Eと`make go-only-release-gate`を、Python環境をPATHへ置かずに成功させる。
4. Python source、tests、`pyproject.toml`、`uv.lock`を除いたtreeでGo build、通常test、race test、vet、fixture contract testを成功させる。
5. README、配布物、CI、運用手順、sampleからPython install／commandを除去する。
6. JSON Contract v1を継続するか、Python専用process contractとして同時廃止するかを別途明示する。破壊的変更を伴う場合は新versionとADRを用意する。

## 削除時に消える依存

| 削除対象 | 消える依存／資産 |
|---|---|
| legacy Provider modules | `anthropic`、`python-dotenv` |
| Python package build | `uv_build`、Python package metadata、`workspace-ai` console script |
| Python Go process Adapter | Python subprocess／JSON envelope変換コード。ただしGo側JSON v1 contractは別判断まで維持 |
| Python compatibility tests | Python interpreter、`.venv`、Python test／compile gate |
| `src/workspace_ai`全体 | 公開v0.1 import surfaceとlegacy/reference実装 |

`crewai`は既に依存から削除済みです。Go製品依存は`golang.org/x/text`だけであり、Python物理削除によるGo Core／Runtimeの変更は不要です。
