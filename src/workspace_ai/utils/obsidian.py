from pathlib import Path
import os


def get_vault_path() -> Path:
    """Obsidian Vaultの場所を返す"""
    vault = os.getenv("WORKSPACE_VAULT_PATH")
    if not vault:
        raise RuntimeError("WORKSPACE_VAULT_PATH が設定されていません。")
    return Path(vault)


def workspace_state_path() -> Path:
    """会社状態ファイル"""
    return get_vault_path() / "会社" / "Workspace State.md"


def projects_path() -> Path:
    """プロジェクト一覧の保存先"""
    return get_vault_path() / "プロジェクト"
    
