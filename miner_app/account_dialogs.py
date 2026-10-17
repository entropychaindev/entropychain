"""
Account-management modal dialogs.

- AddAccountDialog: tabbed dialog (Generate / Import)
- ExportKeyDialog: re-auth + reveal-on-click + copy
- Backup is a file save dialog (not a class, just a function)
"""
from __future__ import annotations

import json
import re
import sys
from datetime import datetime
from pathlib import Path
from typing import Optional

from PySide6.QtCore import Qt, QTimer
from PySide6.QtGui import QGuiApplication
from PySide6.QtWidgets import (
    QDialog, QVBoxLayout, QHBoxLayout, QLabel, QLineEdit, QPushButton,
    QTabWidget, QWidget, QMessageBox, QFrame, QFileDialog, QApplication,
)

_CHAIN_DIR = Path(__file__).resolve().parent.parent
if str(_CHAIN_DIR) not in sys.path:
    sys.path.insert(0, str(_CHAIN_DIR))

from wallet import Wallet  # noqa: E402


# ----------------------------------------------------------------------
# Add account
# ----------------------------------------------------------------------

class AddAccountDialog(QDialog):
    """Tabbed dialog with Generate-new and Import-existing modes."""

    def __init__(self, wallet: Wallet, parent=None):
        super().__init__(parent)
        self.wallet = wallet
        self.created_address: Optional[str] = None

        self.setWindowTitle("Add account")
        self.setModal(True)
        self.setMinimumWidth(480)

        root = QVBoxLayout(self)
        root.setContentsMargins(24, 22, 24, 22)
        root.setSpacing(14)

        title = QLabel("Add account")
        title.setProperty("role", "header")
        root.addWidget(title)

        sub = QLabel(
            "Add another account to your wallet. The new account will be "
            "saved alongside your existing accounts in the same wallet file."
        )
        sub.setWordWrap(True)
        sub.setStyleSheet("color: #6c7990; font-size: 12px;")
        root.addWidget(sub)

        self.tabs = QTabWidget()
        self.tabs.addTab(self._build_generate(), "Generate new")
        self.tabs.addTab(self._build_import(), "Import existing")
        root.addWidget(self.tabs)

        self.error_label = QLabel("")
        self.error_label.setStyleSheet("color: #ff5959; font-size: 12px;")
        self.error_label.setWordWrap(True)
        root.addWidget(self.error_label)

        actions = QHBoxLayout()
        actions.addStretch(1)
        cancel = QPushButton("Cancel")
        cancel.setProperty("role", "secondary")
        cancel.clicked.connect(self.reject)
        actions.addWidget(cancel)
        confirm = QPushButton("Add account")
        confirm.clicked.connect(self._on_confirm)
        actions.addWidget(confirm)
        root.addLayout(actions)

    def _build_generate(self) -> QWidget:
        w = QWidget()
        v = QVBoxLayout(w)
        v.setContentsMargins(8, 14, 8, 8)
        info = QLabel(
            "Generate a fresh keypair. The private key is stored in your "
            "wallet file alongside your existing accounts."
        )
        info.setWordWrap(True)
        info.setStyleSheet("color: #d2dae6; font-size: 12px;")
        v.addWidget(info)

        v.addWidget(QLabel("Account label"))
        self.gen_label = QLineEdit()
        self.gen_label.setPlaceholderText("e.g. trading, savings")
        v.addWidget(self.gen_label)
        v.addStretch(1)
        return w

    def _build_import(self) -> QWidget:
        w = QWidget()
        v = QVBoxLayout(w)
        v.setContentsMargins(8, 14, 8, 8)
        info = QLabel(
            "Import an existing private key (64-character hex). The key is "
            "encrypted with your wallet's storage and never leaves this machine."
        )
        info.setWordWrap(True)
        info.setStyleSheet("color: #d2dae6; font-size: 12px;")
        v.addWidget(info)

        v.addWidget(QLabel("Private key (hex)"))
        self.imp_key = QLineEdit()
        self.imp_key.setEchoMode(QLineEdit.Password)
        self.imp_key.setPlaceholderText("64 hexadecimal characters")
        v.addWidget(self.imp_key)

        v.addWidget(QLabel("Account label"))
        self.imp_label = QLineEdit()
        self.imp_label.setPlaceholderText("e.g. imported, mining")
        v.addWidget(self.imp_label)
        v.addStretch(1)
        return w

    def _on_confirm(self):
        self.error_label.setText("")
        try:
            mode = "generate" if self.tabs.currentIndex() == 0 else "import"
            if mode == "generate":
                label = self.gen_label.text().strip() or "account"
                addr = self.wallet.create_address(label)
            else:
                priv = self.imp_key.text().strip().lower()
                label = self.imp_label.text().strip() or "imported"
                if not re.fullmatch(r"[0-9a-f]{64}", priv):
                    self.error_label.setText(
                        "Private key must be exactly 64 hex characters."
                    )
                    return
                if priv in {info["private_key"] for info in self.wallet._keys.values()}:
                    self.error_label.setText("This account is already in the wallet.")
                    return
                addr = self.wallet.import_private_key(priv, label=label)
            self.created_address = addr
            self.accept()
        except Exception as e:
            self.error_label.setText(str(e))


# ----------------------------------------------------------------------
# Export private key
# ----------------------------------------------------------------------

class ExportKeyDialog(QDialog):
    """
    Reveal a single account's private key after re-confirmation.

    For unencrypted local wallets we can't actually re-auth (no passphrase),
    so we use a simple "type CONFIRM to proceed" pattern instead.
    Reveal-on-click + copy button + auto-hide on window blur.
    """

    def __init__(self, wallet: Wallet, address: str, label: str, parent=None):
        super().__init__(parent)
        self.wallet = wallet
        self.address = address
        self.label = label
        self._revealed = False

        self.setWindowTitle("Export private key")
        self.setModal(True)
        self.setMinimumWidth(560)

        root = QVBoxLayout(self)
        root.setContentsMargins(24, 22, 24, 22)
        root.setSpacing(14)

        title = QLabel("Export private key")
        title.setProperty("role", "header")
        root.addWidget(title)

        warn = QLabel(
            "<b>Anyone with this key can drain this account.</b> "
            "Never paste it in a chat, email, or screenshot. "
            "Anyone asking you to share it is trying to steal from you."
        )
        warn.setWordWrap(True)
        warn.setStyleSheet(
            "color: #ffb454; background: rgba(255,180,84,0.06); "
            "border: 1px solid #ffb454; border-radius: 4px; padding: 10px 12px; "
            "font-size: 12px;"
        )
        root.addWidget(warn)

        meta = QLabel(
            f"<span style='color:#6c7990'>Account:</span> "
            f"<span style='color:#00d4aa;font-weight:500'>{label}</span>  "
            f"<span style='color:#6c7990'>·</span>  "
            f"<span style='color:#ff6b9d;font-family:Consolas,monospace;font-size:11px'>"
            f"{address}</span>"
        )
        meta.setTextFormat(Qt.RichText)
        meta.setWordWrap(True)
        root.addWidget(meta)

        # Confirmation gate (typing CONFIRM to proceed)
        self.confirm_gate = QWidget()
        gate_v = QVBoxLayout(self.confirm_gate)
        gate_v.setContentsMargins(0, 8, 0, 0)
        gate_label = QLabel("Type <b>CONFIRM</b> to reveal the key:")
        gate_label.setTextFormat(Qt.RichText)
        gate_label.setStyleSheet("color: #d2dae6; font-size: 12px;")
        gate_v.addWidget(gate_label)
        self.confirm_input = QLineEdit()
        self.confirm_input.setPlaceholderText("CONFIRM")
        self.confirm_input.textChanged.connect(self._on_confirm_changed)
        gate_v.addWidget(self.confirm_input)
        root.addWidget(self.confirm_gate)

        # Key display (initially hidden)
        self.key_box = QLabel()
        self.key_box.setVisible(False)
        self.key_box.setWordWrap(True)
        self.key_box.setTextInteractionFlags(Qt.TextSelectableByMouse)
        self.key_box.setStyleSheet(
            "background: #07090d; border: 1px solid #1a2433; border-radius: 4px; "
            "padding: 14px; color: #ff6b9d; font-family: Consolas, monospace; "
            "font-size: 12px;"
        )
        root.addWidget(self.key_box)

        actions = QHBoxLayout()
        self.copy_btn = QPushButton("Copy to clipboard")
        self.copy_btn.setEnabled(False)
        self.copy_btn.clicked.connect(self._on_copy)
        actions.addWidget(self.copy_btn)
        actions.addStretch(1)
        done = QPushButton("Done")
        done.setProperty("role", "secondary")
        done.clicked.connect(self.accept)
        actions.addWidget(done)
        root.addLayout(actions)

        # Reveal button (created hidden, becomes visible after CONFIRM typed)
        self.reveal_btn = QPushButton("Click to reveal")
        self.reveal_btn.setVisible(False)
        self.reveal_btn.clicked.connect(self._reveal)
        root.insertWidget(root.indexOf(self.key_box), self.reveal_btn)

    def _on_confirm_changed(self, text: str):
        if text.strip().upper() == "CONFIRM":
            self.confirm_gate.setVisible(False)
            self.reveal_btn.setVisible(True)

    def _reveal(self):
        try:
            priv = self.wallet.export_private_key(self.address)
        except Exception as e:
            QMessageBox.critical(self, "Error", f"Cannot export key: {e}")
            self.reject()
            return
        self.reveal_btn.setVisible(False)
        self.key_box.setText(priv)
        self.key_box.setVisible(True)
        self.copy_btn.setEnabled(True)
        self._revealed = True
        # Auto-hide a minute later for safety
        QTimer.singleShot(60_000, self._auto_hide)

    def _auto_hide(self):
        if self._revealed and self.isVisible():
            self.key_box.setText("(auto-hidden — close and reopen to reveal again)")
            self.copy_btn.setEnabled(False)

    def _on_copy(self):
        priv = self.wallet.export_private_key(self.address)
        QGuiApplication.clipboard().setText(priv)
        QMessageBox.information(
            self, "Copied",
            "Private key copied to clipboard.\n\n"
            "Paste it where you need it, then clear your clipboard."
        )

    def changeEvent(self, ev):
        # Hide the key when the window loses focus (alt-tab, etc.)
        from PySide6.QtCore import QEvent
        if ev.type() == QEvent.ActivationChange:
            if not self.isActiveWindow() and self._revealed:
                self.key_box.setText("(hidden — window lost focus)")
                self.copy_btn.setEnabled(False)
        super().changeEvent(ev)


# ----------------------------------------------------------------------
# Backup wallet to file
# ----------------------------------------------------------------------

def save_wallet_backup(wallet: Wallet, parent=None) -> bool:
    """
    Open a file-save dialog and write the wallet file's contents to the
    chosen path. Returns True on success.
    """
    ts = datetime.now().strftime("%Y-%m-%d-%H%M%S")
    suggested = f"entropy-wallet-backup-{ts}.json"
    path, _ = QFileDialog.getSaveFileName(
        parent,
        "Save wallet backup",
        suggested,
        "JSON (*.json);;All files (*.*)",
    )
    if not path:
        return False
    try:
        # Read the wallet file and copy with a timestamp marker
        with open(wallet.path, "r", encoding="utf-8") as f:
            content = json.load(f)
        if isinstance(content, dict):
            content["backup_format"] = "entropy_wallet_v1"
            content["exported_at"] = datetime.now().isoformat()
        with open(path, "w", encoding="utf-8") as f:
            json.dump(content, f, indent=2)
        QMessageBox.information(
            parent, "Backup saved",
            f"Wallet backup written to:\n\n{path}\n\n"
            "Store it somewhere safe and offline. Anyone with this file "
            "(if unencrypted) can spend the funds in these accounts."
        )
        return True
    except Exception as e:
        QMessageBox.critical(parent, "Backup failed", str(e))
        return False
