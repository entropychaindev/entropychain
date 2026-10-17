"""
EntropyChain Miner — desktop GUI entry point.

Multi-tab interface (Mine / Wallet / Tokens) with multi-account support.
Full wallet experience for the active account; mining is sticky to whatever
address was active when mining started.
"""
import sys
import webbrowser
from pathlib import Path

from PySide6.QtCore import Qt
from PySide6.QtGui import QGuiApplication, QIcon, QAction
from PySide6.QtWidgets import (
    QApplication, QMainWindow, QWidget, QVBoxLayout, QHBoxLayout, QLabel,
    QPushButton, QFrame, QMessageBox, QTabWidget, QMenu, QToolButton,
)

# Path bootstrapping for chain code
_HERE = Path(__file__).resolve().parent
_CHAIN_DIR = _HERE.parent
if str(_CHAIN_DIR) not in sys.path:
    sys.path.insert(0, str(_CHAIN_DIR))

from wallet import Wallet                          # noqa: E402

from .theme import STYLESHEET                       # noqa: E402
from .paths import wallet_file, app_data_dir        # noqa: E402
from .wallet_setup import WalletSetupDialog         # noqa: E402
from .rpc_client import RpcClient                   # noqa: E402
from .tabs.mine_tab import MineTab                  # noqa: E402
from .tabs.wallet_tab import WalletTab              # noqa: E402
from .tabs.tokens_tab import TokensTab              # noqa: E402
from .account_dialogs import (                       # noqa: E402
    AddAccountDialog, ExportKeyDialog, save_wallet_backup,
)


EXPLORER_URL = "https://explorer.entropychain.io"


class MainWindow(QMainWindow):
    def __init__(self):
        super().__init__()
        self.setWindowTitle("EntropyChain Miner")
        self.setMinimumSize(960, 720)

        self.wallet = Wallet(path=str(wallet_file()))
        if not self.wallet.addresses():
            # Should not happen — first-run dialog should have populated this.
            self.wallet.create_address("default")

        self._active_address: str = self.wallet.addresses()[0]

        self.rpc = RpcClient()

        self._build_ui()
        self._refresh_account_pill()

    # ---- UI construction --------------------------------------------------

    def _build_ui(self):
        central = QWidget()
        self.setCentralWidget(central)
        root = QVBoxLayout(central)
        root.setContentsMargins(28, 22, 28, 22)
        root.setSpacing(14)

        root.addLayout(self._build_header())
        root.addLayout(self._build_account_bar())
        root.addWidget(self._build_tabs(), stretch=1)
        root.addLayout(self._build_footer())

    def _build_header(self) -> QHBoxLayout:
        h = QHBoxLayout()
        brand = QLabel(
            '<span style="color:#00d4aa">ENTROPY</span>'
            '<span style="color:#ff6b9d">CHAIN</span> '
            '<span style="color:#6c7990;font-size:11px">MINER</span>'
        )
        brand.setProperty("role", "brand")
        brand.setTextFormat(Qt.RichText)
        h.addWidget(brand)

        badge = QLabel("TESTNET-2")
        badge.setStyleSheet(
            "color: #ffb454; border: 1px solid #ffb454; "
            "padding: 2px 8px; font-size: 10px; letter-spacing: 1px; "
            "border-radius: 3px;"
        )
        h.addWidget(badge)
        h.addStretch(1)

        # Manage menu (gear button on the right)
        self.manage_btn = QToolButton()
        self.manage_btn.setText("⋯")
        self.manage_btn.setStyleSheet(
            "QToolButton { color: #6c7990; background: transparent; "
            "border: 1px solid #1a2433; border-radius: 4px; padding: 2px 12px; "
            "font-size: 18px; }"
            "QToolButton:hover { color: #00d4aa; border-color: #00d4aa; }"
            "QToolButton::menu-indicator { image: none; }"
        )
        self.manage_btn.setPopupMode(QToolButton.InstantPopup)
        self.manage_btn.setMenu(self._build_manage_menu())
        h.addWidget(self.manage_btn)

        return h

    def _build_manage_menu(self) -> QMenu:
        menu = QMenu(self)
        menu.setStyleSheet(
            "QMenu { background: #0f161f; color: #d2dae6; "
            "border: 1px solid #2a3a52; border-radius: 4px; padding: 4px; }"
            "QMenu::item { padding: 8px 18px; font-size: 12px; }"
            "QMenu::item:selected { background: #131b27; color: #00d4aa; }"
            "QMenu::separator { height: 1px; background: #1a2433; margin: 4px 0; }"
        )

        export_action = QAction("Export private key…", self)
        export_action.triggered.connect(self._on_export_key)
        menu.addAction(export_action)

        backup_action = QAction("Download wallet backup…", self)
        backup_action.triggered.connect(self._on_backup)
        menu.addAction(backup_action)

        menu.addSeparator()

        data_action = QAction("Open data folder", self)
        data_action.triggered.connect(self._on_open_data_folder)
        menu.addAction(data_action)

        explorer_action = QAction("View on explorer ↗", self)
        explorer_action.triggered.connect(self._on_open_explorer)
        menu.addAction(explorer_action)

        return menu

    def _build_account_bar(self) -> QHBoxLayout:
        h = QHBoxLayout()
        h.setSpacing(10)

        # Account dropdown (label + caret)
        self.account_pill = QToolButton()
        self.account_pill.setStyleSheet(
            "QToolButton { background: #0c1118; color: #00d4aa; "
            "border: 1px solid #1a2433; border-radius: 4px; "
            "padding: 6px 12px; font-family: Consolas, monospace; "
            "font-size: 11px; }"
            "QToolButton:hover { border-color: #00d4aa; }"
            "QToolButton::menu-indicator { image: none; }"
        )
        self.account_pill.setPopupMode(QToolButton.InstantPopup)
        h.addWidget(self.account_pill)

        # Active address text
        self.address_label = QLabel("")
        self.address_label.setStyleSheet(
            "color: #ff6b9d; font-family: Consolas, monospace; font-size: 11px;"
        )
        self.address_label.setTextInteractionFlags(Qt.TextSelectableByMouse)
        h.addWidget(self.address_label)
        h.addStretch(1)

        copy = QPushButton("Copy")
        copy.setProperty("role", "link")
        copy.clicked.connect(self._copy_address)
        h.addWidget(copy)

        return h

    def _build_tabs(self) -> QTabWidget:
        self.tabs = QTabWidget()

        self.mine_tab = MineTab(
            wallet_path=wallet_file(),
            get_active_address=lambda: self._active_address,
        )
        self.mine_tab.mining_state_changed.connect(self._on_mining_state_changed)
        self.mine_tab.block_mined.connect(self._on_block_mined_from_us)

        self.wallet_tab = WalletTab(
            get_wallet=lambda: self.wallet,
            get_active_address=lambda: self._active_address,
            get_active_label=lambda: self._current_label(),
            rpc=self.rpc,
        )

        self.tokens_tab = TokensTab(
            get_wallet=lambda: self.wallet,
            get_active_address=lambda: self._active_address,
            get_active_label=lambda: self._current_label(),
            rpc=self.rpc,
        )

        self.tabs.addTab(self.mine_tab, "  Mine  ")
        self.tabs.addTab(self.wallet_tab, "  Wallet  ")
        self.tabs.addTab(self.tokens_tab, "  Tokens  ")
        return self.tabs

    def _build_footer(self) -> QHBoxLayout:
        h = QHBoxLayout()
        link = QLabel(
            f'<a href="{EXPLORER_URL}" style="color:#6c7990;text-decoration:none">'
            f'entropychain.io ↗</a>'
        )
        link.setOpenExternalLinks(True)
        link.setStyleSheet("font-size: 11px;")
        h.addWidget(link)
        h.addStretch(1)
        version = QLabel("v0.2 testnet")
        version.setStyleSheet("color: #404a5e; font-size: 11px;")
        h.addWidget(version)
        return h

    # ---- Account management ----------------------------------------------

    def _current_label(self) -> str:
        info = self.wallet._keys.get(self._active_address, {})
        return info.get("label", "account")

    def _refresh_account_pill(self):
        """Rebuild dropdown menu and update displayed address."""
        # Pill text = current label
        self.account_pill.setText(f"{self._current_label()} ▾")
        self.address_label.setText(self._active_address)

        # Build dropdown menu
        menu = QMenu(self)
        menu.setStyleSheet(
            "QMenu { background: #0f161f; color: #d2dae6; "
            "border: 1px solid #2a3a52; border-radius: 4px; padding: 4px; "
            "min-width: 320px; }"
            "QMenu::item { padding: 10px 14px; font-size: 12px; "
            "font-family: Consolas, monospace; }"
            "QMenu::item:selected { background: #131b27; color: #00d4aa; }"
            "QMenu::separator { height: 1px; background: #1a2433; margin: 4px 0; }"
        )

        for addr in self.wallet.addresses():
            info = self.wallet._keys.get(addr, {})
            label = info.get("label", "unnamed")
            marker = "● " if addr == self._active_address else "   "
            short = f"{addr[:10]}…{addr[-6:]}"
            text = f"{marker}{label:<14}  {short}"
            act = QAction(text, self)
            act.setData(addr)
            act.triggered.connect(lambda checked, a=addr: self._switch_account(a))
            menu.addAction(act)

        menu.addSeparator()
        add_act = QAction("+ Add account…", self)
        add_act.triggered.connect(self._on_add_account)
        menu.addAction(add_act)

        self.account_pill.setMenu(menu)

    def _switch_account(self, addr: str):
        if addr == self._active_address:
            return
        self._active_address = addr
        self._refresh_account_pill()
        # Tell tabs the active account changed
        self.wallet_tab.on_active_account_changed()
        self.tokens_tab.on_active_account_changed()
        self.mine_tab.update_address_indicator()

    def _on_add_account(self):
        dlg = AddAccountDialog(self.wallet, self)
        if dlg.exec() and dlg.created_address:
            self._switch_account(dlg.created_address)

    def _on_export_key(self):
        dlg = ExportKeyDialog(
            self.wallet, self._active_address, self._current_label(), self,
        )
        dlg.exec()

    def _on_backup(self):
        save_wallet_backup(self.wallet, parent=self)

    # ---- Header actions --------------------------------------------------

    def _copy_address(self):
        QGuiApplication.clipboard().setText(self._active_address)
        self.statusBar().showMessage("Address copied", 2000)

    def _on_open_explorer(self):
        webbrowser.open(f"{EXPLORER_URL}/address/{self._active_address}")

    def _on_open_data_folder(self):
        webbrowser.open(f"file:///{app_data_dir()}")

    # ---- Mining hooks ----------------------------------------------------

    def _on_mining_state_changed(self, mining: bool):
        # Could disable account switcher while mining; keeping enabled for now
        # because mining is sticky to a starting address (changing while running
        # is intentionally a no-op for that purpose).
        pass

    def _on_block_mined_from_us(self, height: int):
        # Refresh the wallet tab's balance so the user sees the reward arrive.
        self.wallet_tab.refresh()

    # ---- Lifecycle --------------------------------------------------------

    def closeEvent(self, ev):
        if self.mine_tab.mining:
            ans = QMessageBox.question(
                self, "Stop mining?",
                "Mining is active. Stop and quit?",
                QMessageBox.Yes | QMessageBox.No, QMessageBox.No,
            )
            if ans != QMessageBox.Yes:
                ev.ignore()
                return
            self.mine_tab.request_stop()

        # Clean up tab background threads
        try:
            self.wallet_tab.shutdown()
        except Exception:
            pass
        try:
            self.tokens_tab.shutdown()
        except Exception:
            pass
        ev.accept()


def main():
    app = QApplication(sys.argv)
    app.setStyleSheet(STYLESHEET)
    app.setApplicationName("EntropyChain Miner")
    app.setOrganizationName("EntropyChain")

    icon_path = Path(__file__).parent / "icon.png"
    if icon_path.exists():
        app.setWindowIcon(QIcon(str(icon_path)))

    if not wallet_file().exists():
        dlg = WalletSetupDialog()
        if dlg.exec() != dlg.DialogCode.Accepted:
            return 0

    win = MainWindow()
    win.show()
    return app.exec()


if __name__ == "__main__":
    sys.exit(main())
