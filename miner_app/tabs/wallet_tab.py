"""
Wallet tab — for the active account:
  - All-token balance list
  - Send form (recipient + amount + token dropdown)
  - Recent transactions
"""
from __future__ import annotations

from typing import Optional

from PySide6.QtCore import Qt, Signal, QThread, QTimer, Slot
from PySide6.QtCore import QObject
from PySide6.QtWidgets import (
    QWidget, QVBoxLayout, QHBoxLayout, QLabel, QPushButton, QLineEdit,
    QComboBox, QGroupBox, QGridLayout, QFrame, QMessageBox,
)

from ..rpc_client import RpcClient, RpcError
from ..tx_builder import TxBuilder, TxSubmitError, fmt_amount, NATIVE


class _Fetcher(QObject):
    """Background worker for RPC calls so the UI doesn't freeze."""
    balances_ready = Signal(dict)        # {token: units (as str)}
    history_ready = Signal(list)          # [tx-dict, ...]
    fetch_error = Signal(str)

    def __init__(self, rpc: RpcClient):
        super().__init__()
        self.rpc = rpc
        self._stop = False

    @Slot(str)
    def fetch_for(self, address: str):
        if not address:
            return
        try:
            bal = self.rpc.balance(address)
            # Endpoint returns {balances: {token: units}} or {token: units}
            if "balances" in bal:
                raw = bal["balances"]
            else:
                raw = {k: v for k, v in bal.items()
                       if k not in ("address", "ok", "error")}
            # IMPORTANT: PySide6 marshals dict values via Qt's type system,
            # and Qt ints overflow at 2^63 (~9.2 quintillion). Big-supply
            # tokens easily exceed that. Convert all values to strings before
            # crossing the signal boundary; the receiver parses back to int.
            stringified = {str(k): str(v) for k, v in raw.items()}
            self.balances_ready.emit(stringified)
        except RpcError as e:
            # 5xx and transient errors should fail quietly; the next tick refreshes
            msg = str(e)
            if "HTTP 5" in msg or "RPC error" in msg:
                return
            self.fetch_error.emit(f"Could not fetch balances: {e}")

        # History is best-effort — if the endpoint is missing or slow, skip
        try:
            h = self.rpc.history(address, limit=20)
            txs = h.get("history", []) if isinstance(h, dict) else []
            self.history_ready.emit(txs)
        except RpcError:
            self.history_ready.emit([])


class WalletTab(QWidget):
    """Wallet tab. Bound to the currently-active account in the app."""

    def __init__(self, get_wallet, get_active_address, get_active_label,
                 rpc: RpcClient, parent=None):
        super().__init__(parent)
        self._get_wallet = get_wallet
        self._get_active = get_active_address
        self._get_label = get_active_label
        self.rpc = rpc
        self.tx_builder: Optional[TxBuilder] = None
        self._balances: dict = {}

        v = QVBoxLayout(self)
        v.setContentsMargins(0, 8, 0, 0)
        v.setSpacing(14)

        v.addWidget(self._build_balances())
        v.addWidget(self._build_send())
        v.addWidget(self._build_history(), stretch=1)

        # Track pending send: when set, we're waiting for the balance to
        # change (or for the timeout) before clearing the status text.
        self._pending_send_balance: int | None = None  # ENTRO units at time of send
        self._pending_send_token: str | None = None

        # Background fetcher
        self._fetcher = _Fetcher(rpc)
        self._fetcher_thread = QThread(self)
        self._fetcher.moveToThread(self._fetcher_thread)
        self._fetcher.balances_ready.connect(self._on_balances)
        self._fetcher.history_ready.connect(self._on_history)
        self._fetcher.fetch_error.connect(self._on_fetch_error)
        self._fetcher_thread.start()

        # Periodic refresh
        self._timer = QTimer(self)
        self._timer.setInterval(15_000)  # 15s
        self._timer.timeout.connect(self.refresh)
        self._timer.start()

    # ---- UI construction ------------------------------------------------

    def _build_balances(self) -> QGroupBox:
        g = QGroupBox("BALANCES")
        v = QVBoxLayout(g)
        v.setContentsMargins(14, 18, 14, 14)
        self.balance_rows_widget = QWidget()
        self.balance_rows = QVBoxLayout(self.balance_rows_widget)
        self.balance_rows.setContentsMargins(0, 0, 0, 0)
        self.balance_rows.setSpacing(6)
        v.addWidget(self.balance_rows_widget)

        refresh_row = QHBoxLayout()
        refresh_row.addStretch(1)
        self.refresh_btn = QPushButton("Refresh")
        self.refresh_btn.setProperty("role", "secondary")
        self.refresh_btn.clicked.connect(self.refresh)
        refresh_row.addWidget(self.refresh_btn)
        v.addLayout(refresh_row)

        self._render_balance_rows({})
        return g

    def _build_send(self) -> QGroupBox:
        g = QGroupBox("SEND")
        v = QVBoxLayout(g)
        v.setContentsMargins(14, 18, 14, 14)
        v.setSpacing(8)

        v.addWidget(QLabel("Recipient address"))
        self.send_recipient = QLineEdit()
        self.send_recipient.setPlaceholderText("E... (recipient address)")
        v.addWidget(self.send_recipient)

        row = QHBoxLayout()
        amt_col = QVBoxLayout()
        amt_col.addWidget(QLabel("Amount"))
        self.send_amount = QLineEdit()
        self.send_amount.setPlaceholderText("0.0")
        amt_col.addWidget(self.send_amount)
        row.addLayout(amt_col, 2)

        tok_col = QVBoxLayout()
        tok_col.addWidget(QLabel("Token"))
        self.send_token = QComboBox()
        self.send_token.addItem(NATIVE)
        tok_col.addWidget(self.send_token)
        row.addLayout(tok_col, 1)
        v.addLayout(row)

        self.send_status = QLabel("")
        self.send_status.setWordWrap(True)
        self.send_status.setStyleSheet("font-size: 12px; color: #6c7990;")
        v.addWidget(self.send_status)

        action_row = QHBoxLayout()
        action_row.addStretch(1)
        self.send_btn = QPushButton("Send")
        self.send_btn.clicked.connect(self._on_send)
        action_row.addWidget(self.send_btn)
        v.addLayout(action_row)
        return g

    def _build_history(self) -> QGroupBox:
        from PySide6.QtWidgets import QScrollArea
        g = QGroupBox("RECENT ACTIVITY")
        outer = QVBoxLayout(g)
        outer.setContentsMargins(14, 18, 14, 14)

        # Scrollable list inside the box
        self.history_scroll = QScrollArea()
        self.history_scroll.setWidgetResizable(True)
        self.history_scroll.setStyleSheet("background: transparent; border: none;")
        self.history_scroll.setMinimumHeight(160)

        self.history_widget = QWidget()
        self.history_layout = QVBoxLayout(self.history_widget)
        self.history_layout.setContentsMargins(0, 0, 0, 0)
        self.history_layout.setSpacing(2)
        self.history_layout.addStretch(1)

        self.history_scroll.setWidget(self.history_widget)
        outer.addWidget(self.history_scroll)

        # Initial placeholder
        self._render_history([])
        return g

    def _render_history(self, txs: list):
        # Clear the layout
        while self.history_layout.count():
            item = self.history_layout.takeAt(0)
            w = item.widget()
            if w:
                w.deleteLater()

        if not txs:
            empty = QLabel("(No activity yet — receive or send to see history)")
            empty.setStyleSheet("color: #404a5e; font-size: 12px; padding: 16px 0;")
            empty.setAlignment(Qt.AlignCenter)
            self.history_layout.addWidget(empty)
            self.history_layout.addStretch(1)
            return

        active = self._get_active()
        for tx in txs:
            self.history_layout.addWidget(self._build_history_row(tx, active))
        self.history_layout.addStretch(1)

    def _build_history_row(self, tx: dict, my_addr: str) -> QWidget:
        wrap = QWidget()
        wrap.setStyleSheet(
            "QWidget { background: transparent; border-bottom: 1px solid #131b27; }"
        )
        wrap.setMinimumHeight(46)
        h = QHBoxLayout(wrap)
        h.setContentsMargins(8, 8, 8, 8)
        h.setSpacing(10)

        kind = tx.get("tx_type", "?")
        d = tx.get("data", {})
        sender = tx.get("sender", "")
        height = tx.get("height", "?")
        outgoing = sender == my_addr and kind != "coinbase"

        # Direction arrow
        arrow = QLabel("↑" if outgoing else "↓")
        arrow_color = "#ff6b9d" if outgoing else "#00d4aa"
        arrow.setStyleSheet(f"color: {arrow_color}; font-size: 16px; font-weight: 600;")
        arrow.setFixedWidth(18)
        h.addWidget(arrow)

        # Description (left side)
        desc_col = QVBoxLayout()
        desc_col.setSpacing(2)
        desc_col.setContentsMargins(0, 0, 0, 0)

        title = QLabel(self._describe_tx(tx, my_addr))
        title.setStyleSheet("color: #d2dae6; font-size: 12px;")
        desc_col.addWidget(title)

        sub = QLabel(f"Block #{height}")
        sub.setStyleSheet("color: #6c7990; font-size: 10px;")
        desc_col.addWidget(sub)
        h.addLayout(desc_col, stretch=1)

        # Amount (right side) — only for tx types where it makes sense
        amt_units = d.get("amount") if "amount" in d else None
        if amt_units is not None and kind in ("transfer", "token_mint", "coinbase"):
            try:
                amt_int = int(amt_units)
            except (TypeError, ValueError):
                amt_int = 0
            if kind == "transfer":
                token = d.get("token", NATIVE)
            elif kind == "token_mint":
                token = d.get("symbol", NATIVE)
            else:
                token = NATIVE
            sign = "−" if outgoing else "+"
            color = "#ff6b9d" if outgoing else "#00d4aa"
            amt_label = QLabel(f"{sign}{fmt_amount(amt_int)} {token}")
            amt_label.setStyleSheet(
                f"color: {color}; font-family: Consolas, monospace; font-size: 12px;"
            )
            h.addWidget(amt_label)

        return wrap

    def _describe_tx(self, tx: dict, my_addr: str) -> str:
        kind = tx.get("tx_type", "?")
        d = tx.get("data", {})
        sender = tx.get("sender", "")

        def short(a: str) -> str:
            if not a or len(a) < 14:
                return a or "?"
            return f"{a[:8]}…{a[-4:]}"

        if kind == "coinbase":
            return "Mined block reward"
        if kind == "transfer":
            tok = d.get("token", NATIVE)
            if sender == my_addr:
                return f"Sent {tok} to {short(d.get('recipient', ''))}"
            return f"Received {tok} from {short(sender)}"
        if kind == "token_create":
            return f"Created token {d.get('symbol', '?')}"
        if kind == "token_mint":
            recipient = d.get("recipient", "")
            if sender == my_addr:
                return f"Minted {d.get('symbol', '?')} to {short(recipient)}"
            return f"Received {d.get('symbol', '?')} (minted)"
        if kind == "nft_create":
            return f"Created collection {d.get('collection_id', '?')}"
        if kind in ("nft_mint", "nft_random_mint"):
            recipient = d.get("recipient", "")
            cid = d.get("collection_id", "?")
            tid = d.get("token_id", "?")
            if sender == my_addr:
                return f"Minted NFT {cid}#{tid} to {short(recipient)}"
            return f"Received NFT {cid}#{tid}"
        if kind == "nft_transfer":
            cid = d.get("collection_id", "?")
            tid = d.get("token_id", "?")
            if sender == my_addr:
                return f"Sent NFT {cid}#{tid}"
            return f"Received NFT {cid}#{tid}"
        if kind == "lottery_create":
            return f"Created lottery {d.get('lottery_id', '?')}"
        if kind == "lottery_enter":
            return f"Entered lottery {d.get('lottery_id', '?')}"
        if kind == "lottery_draw":
            return f"Drew lottery {d.get('lottery_id', '?')}"
        return kind

    def _on_history(self, txs: list):
        self._render_history(txs)

    # ---- Render helpers -------------------------------------------------

    def _render_balance_rows(self, balances: dict):
        # Clear existing rows
        while self.balance_rows.count():
            item = self.balance_rows.takeAt(0)
            w = item.widget()
            if w:
                w.deleteLater()

        if not balances:
            empty = QLabel("(No balances yet)")
            empty.setStyleSheet("color: #404a5e; font-size: 12px;")
            self.balance_rows.addWidget(empty)
            self.balance_rows.addStretch(1)
            return

        for token, units in sorted(balances.items()):
            wrap = QWidget()
            # Explicit min-height so rows render uniformly even with many tokens
            wrap.setMinimumHeight(34)
            wrap.setStyleSheet(
                "QWidget { background: transparent; border-bottom: 1px solid #131b27; }"
            )
            row = QHBoxLayout(wrap)
            row.setContentsMargins(8, 6, 8, 6)
            row.setSpacing(12)

            t = QLabel(token)
            t.setStyleSheet(
                "color: #00d4aa; font-weight: 500; font-family: Consolas, monospace; "
                "font-size: 13px; border: none;"
            )
            t.setMinimumWidth(80)
            row.addWidget(t)
            row.addStretch(1)

            v = QLabel(fmt_amount(units))
            v.setStyleSheet(
                "color: #ffffff; font-family: Consolas, monospace; font-size: 13px; border: none;"
            )
            row.addWidget(v)

            self.balance_rows.addWidget(wrap)

        self.balance_rows.addStretch(1)

    # ---- Actions --------------------------------------------------------

    def refresh(self):
        addr = self._get_active()
        if not addr:
            return
        # Fire-and-forget; results come back via signals
        from PySide6.QtCore import QMetaObject, Q_ARG
        QMetaObject.invokeMethod(
            self._fetcher, "fetch_for",
            Qt.QueuedConnection,
            Q_ARG(str, addr),
        )

    def on_active_account_changed(self):
        # Force immediate refresh when user switches accounts
        self.send_status.setText("")
        self._render_balance_rows({})
        self._render_history([])
        self.refresh()

    def _on_balances(self, balances: dict):
        # Convert string units to ints if needed
        clean = {}
        for k, v in balances.items():
            try:
                clean[k] = int(v)
            except (TypeError, ValueError):
                continue

        # If a send is pending, check whether the balance has dropped
        # (confirming the transfer landed in a block). When it does,
        # clear the pending-status message.
        if (self._pending_send_balance is not None and
                self._pending_send_token in clean and
                clean[self._pending_send_token] < self._pending_send_balance):
            self._pending_send_balance = None
            self._pending_send_token = None
            self.send_status.setText("✓ Confirmed.")
            QTimer.singleShot(4000, lambda: (
                self.send_status.setText("")
                if self.send_status.text() == "✓ Confirmed." else None
            ))

        self._balances = clean
        self._render_balance_rows(clean)

        # Repopulate the token dropdown in the Send form
        current = self.send_token.currentText()
        self.send_token.blockSignals(True)
        self.send_token.clear()
        self.send_token.addItem(NATIVE)
        for tok in sorted(clean.keys()):
            if tok != NATIVE:
                self.send_token.addItem(tok)
        idx = self.send_token.findText(current)
        if idx >= 0:
            self.send_token.setCurrentIndex(idx)
        self.send_token.blockSignals(False)

    def _on_fetch_error(self, msg: str):
        # Quietly show a status; don't pop a dialog every 15s
        self.send_status.setText(msg)

    def _on_send(self):
        # Disable the button as the FIRST thing — guards against double-clicks
        # or any path that could trigger this handler twice.
        self.send_btn.setEnabled(False)
        try:
            self._do_send()
        finally:
            self.send_btn.setEnabled(True)

    def _do_send(self):
        wallet = self._get_wallet()
        addr = self._get_active()
        if not wallet or not addr:
            return
        recipient = self.send_recipient.text().strip()
        amount = self.send_amount.text().strip()
        token = self.send_token.currentText().strip() or NATIVE

        if not recipient:
            self.send_status.setText("Recipient address required.")
            return
        if not amount:
            self.send_status.setText("Amount required.")
            return
        try:
            float(amount)
        except ValueError:
            self.send_status.setText("Amount must be a number.")
            return

        # Confirm
        ans = QMessageBox.question(
            self, "Confirm send",
            f"Send {amount} {token}\n→ {recipient}\n\nfrom your account "
            f"{self._get_label()} ({addr[:10]}…{addr[-6:]})?\n\n"
            "Transactions cannot be reversed.",
            QMessageBox.Yes | QMessageBox.No,
            QMessageBox.No,
        )
        if ans != QMessageBox.Yes:
            return

        if not self.tx_builder:
            self.tx_builder = TxBuilder(wallet, self.rpc)
        else:
            self.tx_builder.wallet = wallet  # in case wallet was reloaded

        self.send_status.setText("Submitting...")

        # Snapshot the balance before send so _on_balances can detect when
        # the change lands and clear the pending-status message.
        try:
            self._pending_send_balance = int(self._balances.get(token, 0))
            self._pending_send_token = token
        except (ValueError, TypeError):
            self._pending_send_balance = None
            self._pending_send_token = None

        try:
            resp = self.tx_builder.transfer(
                sender=addr, recipient=recipient,
                amount_human=amount, token=token,
            )
            tx_hash = resp.get("tx_hash", "")
            self.send_status.setText(
                f"Sent. Pending confirmation. Tx hash: "
                f"{tx_hash[:16]}…" if tx_hash else "Sent. Pending confirmation."
            )
            # Clear form
            self.send_recipient.clear()
            self.send_amount.clear()
            # Optimistic refresh
            QTimer.singleShot(2000, self.refresh)
            # Hard timeout: clear status after 90 seconds even if no balance change
            QTimer.singleShot(90_000, self._clear_pending_send_if_stale)
        except TxSubmitError as e:
            self._pending_send_balance = None
            self._pending_send_token = None
            self.send_status.setText(f"Failed: {e}")
        except Exception as e:
            self._pending_send_balance = None
            self._pending_send_token = None
            self.send_status.setText(f"Error: {e}")

    def _clear_pending_send_if_stale(self):
        """Hard-timeout fallback if the balance change never gets detected."""
        if self._pending_send_balance is not None:
            self._pending_send_balance = None
            self._pending_send_token = None
            self.send_status.setText("")

    def shutdown(self):
        self._timer.stop()
        self._fetcher_thread.quit()
        self._fetcher_thread.wait(2000)
