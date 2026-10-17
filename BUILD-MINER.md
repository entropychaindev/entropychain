# Building EntropyChainMiner.exe

This document explains how to compile the Python source into a single
distributable `EntropyChainMiner.exe` for Windows.

## Prerequisites

You need to do this on a **Windows machine**. PyInstaller produces a
binary for the OS it runs on — building on Linux would give you a Linux
binary, not a `.exe`.

Install on the build machine:

1. **Python 3.11** (specifically — newer versions sometimes have
   PySide6 / PyInstaller compatibility issues).
   Download from <https://www.python.org/downloads/> and check
   "Add Python to PATH" during install.

2. **Git** (optional, for moving the source code around).

## Source layout

After copying the chain source to your Windows build machine:

```
EntropyChain\
├── EntropyChainMiner.py        ← top-level entry script
├── EntropyChainMiner.spec      ← PyInstaller build config
├── BUILD-MINER.md              ← (this file)
├── miner_app\
│   ├── __init__.py
│   ├── main.py                 ← Qt main window
│   ├── worker.py               ← background mining thread
│   ├── wallet_setup.py         ← first-run dialog
│   ├── widgets.py              ← stat tile widget
│   ├── theme.py                ← Qt stylesheet
│   └── paths.py                ← appdata path helpers
└── (chain code: block.py, blockchain.py, ...)
```

## Build steps

Open **PowerShell** in the `EntropyChain` folder.

### 1. Create a clean virtualenv

```powershell
py -3.11 -m venv build-venv
.\build-venv\Scripts\Activate.ps1
```

If activation fails with a security policy error, run once:
```powershell
Set-ExecutionPolicy -ExecutionPolicy RemoteSigned -Scope CurrentUser
```
then reopen PowerShell and try again.

### 2. Install dependencies

```powershell
pip install --upgrade pip
pip install pyside6 argon2-cffi ecdsa base58 websockets flask cryptography click pyinstaller
```

This will take 1-2 minutes. PySide6 is the largest download (~120 MB).

### 3. Test the app runs from source

Before packaging, make sure it works as a regular Python script:

```powershell
python EntropyChainMiner.py
```

You should see the wallet setup dialog. Create or import a wallet,
then verify the main window opens and "Start mining" connects to the
public testnet. **If it doesn't work in source form, packaging won't
fix it.** Stop the miner (close the window) before continuing.

### 4. Build the binary

```powershell
pyinstaller EntropyChainMiner.spec
```

This takes 1-3 minutes. PyInstaller will:
- Analyze imports
- Bundle Python + all dependencies + your chain code
- Compress with UPX
- Output `dist\EntropyChainMiner.exe` (~80-100 MB)

### 5. Test the binary

```powershell
.\dist\EntropyChainMiner.exe
```

It should behave identically to running from source. Note that:
- First launch on Windows will trigger a SmartScreen warning ("Windows
  protected your PC"). Click "More info" → "Run anyway". This is
  unavoidable for unsigned binaries. See "Code signing" below.
- The app will create `%LOCALAPPDATA%\EntropyChain\` for chain data.

### 6. Distribute

Upload `EntropyChainMiner.exe` somewhere downloadable. Options:

- **Your droplet**: drop it at
  `~/static-site/downloads/EntropyChainMiner.exe`, then link to
  `https://entropychain.io/downloads/EntropyChainMiner.exe` from the
  landing page.
- **GitHub Releases**: free, version-tracked, gets you a download
  count. Create a release and attach the `.exe`.

## Common build issues

**"argon2-cffi" build fails on Windows.**
You need the Microsoft C++ Build Tools. Download Visual Studio Build
Tools from <https://visualstudio.microsoft.com/visual-cpp-build-tools/>
and install with the "Desktop development with C++" workload checked.

**App runs in source but the .exe fails to start.**
PyInstaller missed an import. Run the .exe from a console:
```powershell
.\dist\EntropyChainMiner.exe
```
to see the traceback. Usually you need to add the missing module name
to the `hiddenimports` list in `EntropyChainMiner.spec` and rebuild.

**.exe is huge (>100 MB).**
That's normal for PySide6 + chain dependencies. UPX compression helps.
If you really want it smaller, switch to a `--onedir` build (creates a
folder with the .exe + DLLs instead of a single file) — gives ~70 MB
and starts faster.

## Code signing (optional but recommended)

Unsigned executables trigger a Windows SmartScreen warning that scares
non-technical users. To eliminate it, you'd need:

- A code-signing certificate from DigiCert, Sectigo, or similar
  (~$200-500/year).
- Or build a reputation by getting many users to "Run anyway" and
  reporting it as safe over time (months).

For testnet, just document the warning and tell users to click
"Run anyway." For mainnet, budget for a cert.

## Updating

Each release: increment a version string somewhere (e.g., in
`miner_app/main.py`'s window title), rebuild, redistribute.

There is no auto-update built in. Users have to download new versions
manually. That's fine for v1.
