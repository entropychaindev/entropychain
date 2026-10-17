"""Cross-platform appdata path resolution for EntropyChain miner."""
import os
import sys
from pathlib import Path


def app_data_dir() -> Path:
    """
    Returns the per-user data directory for EntropyChain.

    Windows: %LOCALAPPDATA%\\EntropyChain
    macOS:   ~/Library/Application Support/EntropyChain
    Linux:   $XDG_DATA_HOME/EntropyChain or ~/.local/share/EntropyChain
    """
    if sys.platform == "win32":
        base = os.environ.get("LOCALAPPDATA") or os.path.expanduser("~\\AppData\\Local")
        path = Path(base) / "EntropyChain"
    elif sys.platform == "darwin":
        path = Path.home() / "Library" / "Application Support" / "EntropyChain"
    else:
        base = os.environ.get("XDG_DATA_HOME") or os.path.expanduser("~/.local/share")
        path = Path(base) / "EntropyChain"

    path.mkdir(parents=True, exist_ok=True)
    return path


def chain_data_dir() -> Path:
    """Where chain.json and the wallet live."""
    p = app_data_dir() / "data"
    p.mkdir(parents=True, exist_ok=True)
    return p


def log_file() -> Path:
    return app_data_dir() / "miner.log"


def wallet_file() -> Path:
    return chain_data_dir() / "wallet.json"


def chain_file() -> Path:
    return chain_data_dir() / "chain.json"
