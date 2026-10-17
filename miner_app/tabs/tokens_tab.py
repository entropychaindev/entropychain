"""
Tokens tab — list tokens you own, create new tokens, mint to existing.

Token transfer happens in the Wallet tab (using the unified Send form).
This tab is for token *issuance*: create a new token, mint more of an
existing one to a recipient.
"""
from __future__ import annotations

from typing import Optional

from PySide6.QtCore import Qt, QThread, Signal, QTimer, QObject, QMetaObject, Q_ARG, Slot
from PySide6.QtWidgets import (
    QWidget, QVBoxLayout, QHBoxLayout, QLabel, QPushButton, QLineEdit,
    QGroupBox, QFrame, QMessageBox, QSpinBox,
)

from ..rpc_client import RpcClient, RpcError
from ..tx_builder import TxBuilder, TxSubmitError, fmt_amount


def _field(label_text: str, input_widget: QWidget) -> QWidget:
    """
    Wrap a label + input as a single widget. Owning the layout in a wrapper
    prevents the label of the next field from sliding up under the input
    above it when the parent layout gets cramped.
    """
    wrap = QWidget()
    wrap.setMinimumHeight(58)
    layout = QVBoxLayout(wrap)
    layout.setContentsMargins(0, 0, 0, 0)
    layout.setSpacing(4)
    label = QLabel(label_text)
    layout.addWidget(label)
    layout.addWidget(input_widget)
    return wrap


class _TokenFetcher(QObject):
    tokens_ready = Signal(dict)  # {symbol: {name, decimals, supply, creator}}

    def __init__(self, rpc: RpcClient):
        super().__init__()
        self.rpc = rpc

    @Slot()
    def fetch(self):
        try:
            t = self.rpc.tokens()
            tokens = t.get("tokens", t) or {}
            # Stringify total_supply on each token to dodge Qt int64 overflow
            # for large-supply tokens (>2^63). Receiver parses back to int.
            safe = {}
            for sym, info in tokens.items():
                if isinstance(info, dict):
                    safe[sym] = {
                        **info,
                        "total_supply": str(info.get("total_supply", 0)),
                    }
                else:
                    safe[sym] = info
            self.tokens_ready.emit(safe)
        except RpcError:
            self.tokens_ready.emit({})


class TokensTab(QWidget):
    def __init__(self, get_wallet, get_active_address, get_active_label,
                 rpc: RpcClient, parent=None):
        super().__init__(parent)
        self._get_wallet = get_wallet
        self._get_active = get_active_address
        self._get_label = get_active_label
        self.rpc = rpc
        self.tx_builder: Optional[TxBuilder] = None

        v = QVBoxLayout(self)
        v.setContentsMargins(0, 8, 0, 0)
        v.setSpacing(14)

        v.addWidget(self._build_my_tokens(), stretch=1)
        v.addWidget(self._build_create())
        v.addWidget(self._build_mint())

        # Fetcher thread
        self._fetcher = _TokenFetcher(rpc)
        self._fthread = QThread(self)
        self._fetcher.moveToThread(self._fthread)
        self._fetcher.tokens_ready.connect(self._on_tokens)
        self._fthread.start()

        self._timer = QTimer(self)
        self._timer.setInterval(20_000)
        self._timer.timeout.connect(self.refresh)
        self._timer.start()

    # ---- UI -----------------------------------------------------------

    def _build_my_tokens(self) -> QGroupBox:
        from PySide6.QtWidgets import QScrollArea
        g = QGroupBox("TOKENS YOU CREATED")
        v = QVBoxLayout(g)
        v.setContentsMargins(14, 18, 14, 14)

        scroll = QScrollArea()
        scroll.setWidgetResizable(True)
        scroll.setStyleSheet("background: transparent; border: none;")
        scroll.setMinimumHeight(140)
        scroll.setMaximumHeight(220)

        self.my_tokens_widget = QWidget()
        self.my_tokens_layout = QVBoxLayout(self.my_tokens_widget)
        self.my_tokens_layout.setContentsMargins(0, 0, 0, 0)
        self.my_tokens_layout.setSpacing(2)
        self.my_tokens_layout.addStretch(1)

        scroll.setWidget(self.my_tokens_widget)
        v.addWidget(scroll, stretch=1)

        self.my_tokens_empty = QLabel("(You haven't created any tokens yet)")
        self.my_tokens_empty.setStyleSheet("color: #404a5e; font-size: 12px;")
        self.my_tokens_empty.setAlignment(Qt.AlignCenter)
        self.my_tokens_layout.addWidget(self.my_tokens_empty)
        return g

    def _build_create(self) -> QGroupBox:
        g = QGroupBox("CREATE TOKEN")
        v = QVBoxLayout(g)
        v.setContentsMargins(14, 18, 14, 14)
        v.setSpacing(12)

        row1 = QHBoxLayout()
        row1.setSpacing(12)
        self.create_symbol = QLineEdit()
        self.create_symbol.setPlaceholderText("3-8 uppercase letters")
        row1.addWidget(_field("Symbol (e.g. CHAOS)", self.create_symbol), 1)
        self.create_name = QLineEdit()
        self.create_name.setPlaceholderText("Display name")
        row1.addWidget(_field("Name", self.create_name), 2)
        v.addLayout(row1)

        row2 = QHBoxLayout()
        row2.setSpacing(12)
        self.create_supply = QLineEdit()
        self.create_supply.setPlaceholderText("1000000  (whole tokens, no commas)")
        row2.addWidget(_field("Initial supply (whole tokens)", self.create_supply), 2)
        self.create_decimals = QSpinBox()
        self.create_decimals.setMinimum(0)
        self.create_decimals.setMaximum(18)
        self.create_decimals.setValue(8)
        self.create_decimals.setMinimumHeight(36)
        row2.addWidget(_field("Decimals", self.create_decimals), 1)
        v.addLayout(row2)

        self.create_status = QLabel("")
        self.create_status.setWordWrap(True)
        self.create_status.setStyleSheet("font-size: 12px; color: #6c7990;")
        v.addWidget(self.create_status)

        action = QHBoxLayout()
        action.addStretch(1)
        self.create_btn = QPushButton("Create token")
        self.create_btn.clicked.connect(self._on_create)
        action.addWidget(self.create_btn)
        v.addLayout(action)
        return g

    def _build_mint(self) -> QGroupBox:
        g = QGroupBox("MINT MORE OF AN EXISTING TOKEN")
        v = QVBoxLayout(g)
        v.setContentsMargins(14, 18, 14, 14)
        v.setSpacing(12)

        row = QHBoxLayout()
        row.setSpacing(12)
        self.mint_symbol = QLineEdit()
        self.mint_symbol.setPlaceholderText("CHAOS")
        row.addWidget(_field("Token symbol", self.mint_symbol), 1)
        self.mint_amount = QLineEdit()
        self.mint_amount.setPlaceholderText("0.0")
        row.addWidget(_field("Amount", self.mint_amount), 1)
        v.addLayout(row)

        self.mint_recipient = QLineEdit()
        self.mint_recipient.setPlaceholderText("E... (defaults to your address if empty)")
        v.addWidget(_field("Recipient address", self.mint_recipient))

        self.mint_status = QLabel("")
        self.mint_status.setWordWrap(True)
        self.mint_status.setStyleSheet("font-size: 12px; color: #6c7990;")
        v.addWidget(self.mint_status)

        action = QHBoxLayout()
        action.addStretch(1)
        self.mint_btn = QPushButton("Mint")
        self.mint_btn.clicked.connect(self._on_mint)
        action.addWidget(self.mint_btn)
        v.addLayout(action)
        return g

    # ---- Refresh / data flow -------------------------------------------

    def refresh(self):
        QMetaObject.invokeMethod(self._fetcher, "fetch", Qt.QueuedConnection)

    def on_active_account_changed(self):
        # The "tokens you created" filter is per-active-account
        self.refresh()

    def _on_tokens(self, tokens: dict):
        # Clear existing
        while self.my_tokens_layout.count():
            item = self.my_tokens_layout.takeAt(0)
            w = item.widget()
            if w:
                w.deleteLater()

        active = self._get_active()
        # tokens dict from RPC: usually {symbol: {name, decimals, total_supply, creator}}
        mine = []
        for sym, info in (tokens or {}).items():
            if not isinstance(info, dict):
                continue
            if info.get("creator") == active:
                mine.append((sym, info))

        if not mine:
            empty = QLabel("(You haven't created any tokens yet)")
            empty.setStyleSheet("color: #404a5e; font-size: 12px;")
            empty.setAlignment(Qt.AlignCenter)
            self.my_tokens_layout.addWidget(empty)
            self.my_tokens_layout.addStretch(1)
            return

        for sym, info in sorted(mine):
            wrap = QWidget()
            # Explicit min-height so rows don't collapse on top of each other
            # when many tokens are listed (same fix pattern as the form fields)
            wrap.setMinimumHeight(40)
            wrap.setStyleSheet(
                "QWidget { background: transparent; border-bottom: 1px solid #131b27; }"
            )
            row = QHBoxLayout(wrap)
            row.setContentsMargins(8, 8, 8, 8)
            row.setSpacing(12)

            sym_lbl = QLabel(sym)
            sym_lbl.setStyleSheet(
                "color: #00d4aa; font-weight: 500; font-family: Consolas, monospace; "
                "font-size: 13px; border: none;"
            )
            sym_lbl.setMinimumWidth(80)
            row.addWidget(sym_lbl)

            name_lbl = QLabel(info.get("name", ""))
            name_lbl.setStyleSheet("color: #d2dae6; font-size: 12px; border: none;")
            row.addWidget(name_lbl)
            row.addStretch(1)

            supply_lbl = QLabel(
                f"supply {fmt_amount(info.get('total_supply', 0), info.get('decimals', 8))}"
            )
            supply_lbl.setStyleSheet(
                "color: #6c7990; font-family: Consolas, monospace; font-size: 11px; border: none;"
            )
            row.addWidget(supply_lbl)

            self.my_tokens_layout.addWidget(wrap)

        self.my_tokens_layout.addStretch(1)

    # ---- Actions --------------------------------------------------------

    def _on_create(self):
        # Disable button immediately to prevent double-submit
        self.create_btn.setEnabled(False)
        try:
            self._do_create()
        finally:
            self.create_btn.setEnabled(True)

    def _do_create(self):
        wallet = self._get_wallet()
        addr = self._get_active()
        if not wallet or not addr:
            return
        symbol = self.create_symbol.text().strip().upper()
        name = self.create_name.text().strip()
        supply = self.create_supply.text().strip()
        decimals = self.create_decimals.value()

        if not symbol or len(symbol) < 2:
            self.create_status.setText("Symbol required (2+ uppercase letters).")
            return
        if not name:
            self.create_status.setText("Name required.")
            return
        try:
            float(supply)
        except ValueError:
            self.create_status.setText("Supply must be a number.")
            return

        # Format the supply with commas so the user can sanity-check the number
        # of zeros before they mint a billion when they meant a million.
        try:
            supply_int = int(supply)
            supply_pretty = f"{supply_int:,}"
        except ValueError:
            supply_pretty = supply

        ans = QMessageBox.question(
            self, "Create token",
            f"Create token {symbol} ({name})\n\n"
            f"Initial supply: {supply_pretty} {symbol}\n"
            f"Decimals:       {decimals}\n\n"
            f"This costs 100 ENTRO and is irreversible. Continue?",
            QMessageBox.Yes | QMessageBox.No, QMessageBox.No,
        )
        if ans != QMessageBox.Yes:
            return

        if not self.tx_builder:
            self.tx_builder = TxBuilder(wallet, self.rpc)
        else:
            self.tx_builder.wallet = wallet

        self.create_status.setText("Submitting...")
        try:
            resp = self.tx_builder.token_create(
                sender=addr, symbol=symbol, name=name,
                total_supply_human=supply, decimals=decimals,
            )
            tx_hash = resp.get("tx_hash", "")
            self.create_status.setText(
                f"Submitted. Token will appear after the next block. "
                f"{('Tx ' + tx_hash[:12] + '…') if tx_hash else ''}"
            )
            self.create_symbol.clear()
            self.create_name.clear()
            self.create_supply.clear()
            QTimer.singleShot(3000, self.refresh)
        except TxSubmitError as e:
            self.create_status.setText(f"Failed: {e}")
        except Exception as e:
            self.create_status.setText(f"Error: {e}")

    def _on_mint(self):
        # Disable button immediately to prevent double-submit
        self.mint_btn.setEnabled(False)
        try:
            self._do_mint()
        finally:
            self.mint_btn.setEnabled(True)

    def _do_mint(self):
        wallet = self._get_wallet()
        addr = self._get_active()
        if not wallet or not addr:
            return
        symbol = self.mint_symbol.text().strip().upper()
        amount = self.mint_amount.text().strip()
        recipient = self.mint_recipient.text().strip() or addr

        if not symbol:
            self.mint_status.setText("Symbol required.")
            return
        if not amount:
            self.mint_status.setText("Amount required.")
            return
        try:
            float(amount)
        except ValueError:
            self.mint_status.setText("Amount must be a number.")
            return

        ans = QMessageBox.question(
            self, "Mint tokens",
            f"Mint {amount} {symbol}\n→ {recipient}\n\n"
            f"You can only mint tokens you created. Continue?",
            QMessageBox.Yes | QMessageBox.No, QMessageBox.No,
        )
        if ans != QMessageBox.Yes:
            return

        if not self.tx_builder:
            self.tx_builder = TxBuilder(wallet, self.rpc)
        else:
            self.tx_builder.wallet = wallet

        # Look up token decimals from cached info if possible
        decimals = 8
        try:
            t = self.rpc.token(symbol)
            decimals = t.get("decimals", 8) if isinstance(t, dict) else 8
        except RpcError:
            pass

        self.mint_status.setText("Submitting...")
        try:
            resp = self.tx_builder.token_mint(
                sender=addr, symbol=symbol, recipient=recipient,
                amount_human=amount, decimals=decimals,
            )
            self.mint_status.setText("Submitted. Recipient balance will update after the next block.")
            self.mint_amount.clear()
            self.mint_recipient.clear()
        except TxSubmitError as e:
            self.mint_status.setText(f"Failed: {e}")
        except Exception as e:
            self.mint_status.setText(f"Error: {e}")

    def shutdown(self):
        self._timer.stop()
        self._fthread.quit()
        self._fthread.wait(2000)
