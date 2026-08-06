# Workspace OS

Obsidianをデータストアとして、AI社員・プロジェクト・タスク実行を管理するPythonアプリケーションです。

## セットアップ

Python 3.12と[uv](https://docs.astral.sh/uv/)を使用します。

```bash
uv sync
```

ローカルの`.env`には次の環境変数を設定します。`.env`はGit管理されません。

```text
WORKSPACE_VAULT_PATH=...
ANTHROPIC_API_KEY=...
```

## テスト

```bash
uv run python -m unittest discover -s tests -v
```

テストはFake RunnerまたはMock APIクライアントを使用し、実AI APIや実Vaultのタスクを実行しません。

## 安全なタスク実行

TaskExecutorはdry-runと明示的承認に対応しています。実行前に対象プロジェクトの`Tasks.md`と`Progress.md`をバックアップしてください。
