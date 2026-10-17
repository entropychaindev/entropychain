"""
EntropyChainMiner.py — top-level entry point for the desktop miner.

PyInstaller turns this single file into EntropyChainMiner.exe.
"""
from miner_app.main import main

if __name__ == "__main__":
    raise SystemExit(main())
