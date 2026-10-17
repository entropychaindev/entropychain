"""
First-run wallet setup dialog.

Shown when no wallet.json exists in the appdata directory.
User picks: Create new keypair, or Import existing private key.
"""
import re
import sys
from pathlib import Path

from PySide6.QtCore import Qt
from PySide6.QtWidgets import (
    QDialog, QVBoxLayout, QHBoxLayout, QLabel, QPushButton, QLineEdit,
    QTabWidget, QWidget, QMessageBox, QFrame
)

# Import chain code for keygen
_CHAIN_DIR = Path(__file__).resolve().parent.parent
if str(_CHAIN_DIR) not in sys.path:
    sys.path.insert(0, str(_CHAIN_DIR))

from wallet import Wallet  # noqa: E402

from .paths import wallet_file


class WalletSetupDialog(QDialog):
    def __init__(self, parent=None):
        super().__init__(parent)
        self.setWindowTitle("EntropyChain Miner — First-time setup")
        self.setModal(True)
        self.setMinimumWidth(520)

        # Result
        self.created_address: str = ""

        root = QVBoxLayout(self)
        root.setContentsMargins(28, 24, 28, 24)
        root.setSpacing(14)

        title = QLabel("Welcome to EntropyChain")
        title.setProperty("role", "header")
        root.addWidget(title)

        sub = QLabel(
            "Before mining, you need a wallet. Your wallet is just a key file. "
            "Mining rewards are paid to its address."
        )
        sub.setWordWrap(True)
        sub.setStyleSheet("color: #6c7990; font-size: 12px;")
        root.addWidget(sub)

        sep = QFrame()
        sep.setProperty("role", "separator")
        sep.setFrameShape(QFrame.HLine)
        root.addWidget(sep)

        tabs = QTabWidget()
        tabs.addTab(self._build_create_tab(), "Create new wallet")
        tabs.addTab(self._build_import_tab(), "Import existing key")
        root.addWidget(tabs)

        note = QLabel(
            "Your wallet file is stored locally on this computer. Back it up — "
            "if you lose it, any ENTRO mined to this address is gone forever."
        )
        note.setWordWrap(True)
        note.setStyleSheet("color: #ffb454; font-size: 11px; padding: 8px 0;")
        root.addWidget(note)

    # ---- "Create new" tab ------------------------------------------------

    def _build_create_tab(self) -> QWidget:
        w = QWidget()
        v = QVBoxLayout(w)
        v.setContentsMargins(8, 16, 8, 8)
        v.setSpacing(12)

        info = QLabel(
            "Generate a fresh keypair. The wallet file will be stored unencrypted "
            "in your local app data folder. You can export the private key later."
        )
        info.setWordWrap(True)
        info.setStyleSheet("color: #d2dae6;")
        v.addWidget(info)

        btn_row = QHBoxLayout()
        btn_row.addStretch(1)
        create_btn = QPushButton("Create wallet")
        create_btn.setProperty("role", "big")
        create_btn.clicked.connect(self._on_create)
        btn_row.addWidget(create_btn)
        v.addLayout(btn_row)
        v.addStretch(1)
        return w

    def _on_create(self):
        try:
            w = Wallet(path=str(wallet_file()))
            addr = w.create_address("main")
            self.created_address = addr
            QMessageBox.information(
                self,
                "Wallet created",
                f"Your address:\n\n{addr}\n\n"
                f"Wallet file:\n{wallet_file()}\n\n"
                "Click OK to start mining."
            )
            self.accept()
        except Exception as e:
            QMessageBox.critical(self, "Error", f"Could not create wallet:\n{e}")

    # ---- "Import existing" tab -------------------------------------------

    def _build_import_tab(self) -> QWidget:
        w = QWidget()
        v = QVBoxLayout(w)
        v.setContentsMargins(8, 16, 8, 8)
        v.setSpacing(12)

        info = QLabel(
            "Paste a 64-character hex private key from another EntropyChain wallet "
            "(browser, CLI, etc.). The miner will pay rewards to its address."
        )
        info.setWordWrap(True)
        info.setStyleSheet("color: #d2dae6;")
        v.addWidget(info)

        label = QLabel("Private key (hex)")
        label.setProperty("role", "muted")
        v.addWidget(label)

        self.key_input = QLineEdit()
        self.key_input.setEchoMode(QLineEdit.Password)
        self.key_input.setPlaceholderText("64 hexadecimal characters")
        v.addWidget(self.key_input)

        btn_row = QHBoxLayout()
        btn_row.addStretch(1)
        import_btn = QPushButton("Import wallet")
        import_btn.setProperty("role", "big")
        import_btn.clicked.connect(self._on_import)
        btn_row.addWidget(import_btn)
        v.addLayout(btn_row)
        v.addStretch(1)
        return w

    def _on_import(self):
        priv_hex = self.key_input.text().strip()
        if not re.fullmatch(r"[0-9a-fA-F]{64}", priv_hex):
            QMessageBox.warning(
                self, "Invalid key",
                "Private key must be exactly 64 hex characters (no spaces, no 0x prefix)."
            )
            return
        try:
            w = Wallet(path=str(wallet_file()))
            addr = w.import_private_key(priv_hex.lower(), label="imported")
            self.created_address = addr
            QMessageBox.information(
                self,
                "Wallet imported",
                f"Your address:\n\n{addr}\n\nClick OK to start mining."
            )
            self.accept()
        except Exception as e:
            QMessageBox.critical(self, "Error", f"Could not import key:\n{e}")
