# -*- mode: python ; coding: utf-8 -*-
"""
PyInstaller build spec for EntropyChainMiner.

Build with:
    pyinstaller EntropyChainMiner.spec

Produces:
    dist/EntropyChainMiner.exe   (~80MB single file)
"""

block_cipher = None

a = Analysis(
    ['EntropyChainMiner.py'],
    pathex=['.'],
    binaries=[],
    datas=[
        # Bundle the chain Python files so they're importable at runtime
        ('block.py',         '.'),
        ('blockchain.py',    '.'),
        ('consensus.py',     '.'),
        ('crypto_utils.py',  '.'),
        ('genesis_config.py','.'),
        ('miner.py',         '.'),
        ('p2p.py',           '.'),
        ('protocol.py',      '.'),
        ('rpc_server.py',    '.'),
        ('transactions.py',  '.'),
        ('wallet.py',        '.'),
    ],
    hiddenimports=[
        # Force-include packages PyInstaller might miss via dynamic imports
        'argon2',
        'argon2.low_level',
        'ecdsa',
        'ecdsa.curves',
        'base58',
        'websockets',
        'websockets.asyncio.server',
        'websockets.asyncio.client',
        'flask',
        'cryptography',
        'cryptography.hazmat.primitives',
    ],
    hookspath=[],
    hooksconfig={},
    runtime_hooks=[],
    excludes=[
        # Ship-size: drop things we don't use
        'tkinter',
        'unittest',
        'test',
        'pydoc_data',
    ],
    win_no_prefer_redirects=False,
    win_private_assemblies=False,
    cipher=block_cipher,
    noarchive=False,
)

pyz = PYZ(a.pure, a.zipped_data, cipher=block_cipher)

exe = EXE(
    pyz,
    a.scripts,
    a.binaries,
    a.zipfiles,
    a.datas,
    [],
    name='EntropyChainMiner',
    debug=False,
    bootloader_ignore_signals=False,
    strip=False,
    upx=True,
    upx_exclude=[],
    runtime_tmpdir=None,
    console=False,                # Windowed app, no console window
    disable_windowed_traceback=False,
    argv_emulation=False,
    target_arch=None,
    codesign_identity=None,
    entitlements_file=None,
    icon='miner_app/icon.ico' if __import__('os').path.exists('miner_app/icon.ico') else None,
)
