"""
Mine tab — start/stop mining, live stats, log pane.

Mining is sticky to the address it started with. If the user switches
the active wallet account while mining is running, this tab shows a
small note: 'Mining to X · Active account is Y'.
"""
from __future__ import annotations

from typing import Optional

from PySide6.QtCore import Qt, Signal
from PySide6.QtWidgets import (
    QWidget, QVBoxLayout, QHBoxLayout, QLabel, QPushButton,
    QPlainTextEdit, QGroupBox, QGridLayout,
)

from ..widgets import StatTile
from ..worker import MinerWorker
from ..tx_builder import fmt_amount


def fmt_hashrate(hps: float) -> str:
    if hps <= 0:
        return "0 H/s"
    if hps < 1000:
        return f"{hps:.0f} H/s"
    if hps < 1_000_000:
        return f"{hps/1000:.2f} kH/s"
    return f"{hps/1_000_000:.2f} MH/s"


class MineTab(QWidget):
    # Emitted when mining state changes; main window uses this to
    # disable/enable the account switcher etc.
    mining_state_changed = Signal(bool)
    block_mined = Signal(int)  # forward, so main window can refresh balances

    def __init__(self, wallet_path, get_active_address, parent=None):
        super().__init__(parent)
        self.wallet_path = wallet_path
        self._get_active_address = get_active_address  # callable returning current active addr
        self.worker: Optional[MinerWorker] = None
        self.mining = False
        self.mining_address: str = ""

        v = QVBoxLayout(self)
        v.setContentsMargins(0, 8, 0, 0)
        v.setSpacing(14)

        # Top row: address indicator
        self.addr_indicator = QLabel("")
        self.addr_indicator.setStyleSheet("color: #6c7990; font-size: 11px;")
        self.addr_indicator.setVisible(False)
        v.addWidget(self.addr_indicator)

        # Stats panel
        v.addWidget(self._build_stats())

        # Action button row
        v.addLayout(self._build_action_row())

        # Log pane
        v.addWidget(self._build_log_pane(), stretch=1)

    # ---- UI construction --------------------------------------------------

    def _build_stats(self) -> QGroupBox:
        g = QGroupBox("LIVE STATS")
        grid = QGridLayout(g)
        grid.setContentsMargins(14, 18, 14, 14)
        grid.setSpacing(10)

        self.peers_tile = StatTile("Peers")
        self.peers_tile.set_value("0", "no connections")

        self.height_tile = StatTile("Chain height")
        self.height_tile.set_value("0", "latest tip")

        self.hashrate_tile = StatTile("Your hashrate")
        self.hashrate_tile.set_value("0 H/s", "this session")

        self.mined_tile = StatTile("Blocks mined")
        self.mined_tile.set_value("0", "by you, this session")

        self.balance_tile = StatTile("Mining balance")
        self.balance_tile.set_value("0", "ENTRO")

        grid.addWidget(self.peers_tile, 0, 0)
        grid.addWidget(self.height_tile, 0, 1)
        grid.addWidget(self.hashrate_tile, 0, 2)
        grid.addWidget(self.mined_tile, 1, 0)
        grid.addWidget(self.balance_tile, 1, 1)

        return g

    def _build_action_row(self) -> QHBoxLayout:
        h = QHBoxLayout()
        h.addStretch(1)
        self.action_btn = QPushButton("▶  Start mining")
        self.action_btn.setProperty("role", "big")
        self.action_btn.setMinimumWidth(220)
        self.action_btn.clicked.connect(self._toggle_mining)
        h.addWidget(self.action_btn)
        h.addStretch(1)
        return h

    def _build_log_pane(self) -> QGroupBox:
        g = QGroupBox("LOG")
        v = QVBoxLayout(g)
        v.setContentsMargins(10, 16, 10, 10)
        self.log_pane = QPlainTextEdit()
        self.log_pane.setReadOnly(True)
        self.log_pane.setMaximumBlockCount(2000)
        v.addWidget(self.log_pane)
        return g

    # ---- Lifecycle / actions ---------------------------------------------

    def _toggle_mining(self):
        if self.mining:
            self._stop_mining()
        else:
            self._start_mining()

    def _start_mining(self):
        # Pin the mining address to whatever's active at start time.
        # Account switching after this point won't change what we mine to.
        self.mining_address = self._get_active_address()
        if not self.mining_address:
            return

        self.action_btn.setEnabled(False)
        self.action_btn.setText("Starting...")

        self.worker = MinerWorker(wallet_path=self.wallet_path)
        # Tell the worker which address to mine to (added below in worker.py)
        self.worker.mine_to_address = self.mining_address
        self.worker.started_mining.connect(self._on_mining_started)
        self.worker.stopped_mining.connect(self._on_mining_stopped)
        self.worker.fatal_error.connect(self._on_fatal_error)
        self.worker.status_changed.connect(self._on_status_changed)
        self.worker.log_line.connect(self._on_log_line)
        self.worker.block_mined.connect(self._on_block_mined)
        self.worker.start()

    def _stop_mining(self):
        if self.worker:
            self.action_btn.setEnabled(False)
            self.action_btn.setText("Stopping...")
            self.worker.request_stop()

    def _on_mining_started(self):
        self.mining = True
        self.action_btn.setEnabled(True)
        self.action_btn.setText("◼  Stop mining")
        self.action_btn.setProperty("role", "stop")
        self.action_btn.style().polish(self.action_btn)
        self.update_address_indicator()
        self.mining_state_changed.emit(True)

    def _on_mining_stopped(self):
        self.mining = False
        self.action_btn.setEnabled(True)
        self.action_btn.setText("▶  Start mining")
        self.action_btn.setProperty("role", "big")
        self.action_btn.style().polish(self.action_btn)
        self.worker = None
        self.update_address_indicator()
        self.mining_state_changed.emit(False)

    def _on_fatal_error(self, msg: str):
        self.log_pane.appendPlainText(f"\n*** FATAL ERROR ***\n{msg}\n")
        self._on_mining_stopped()

    def _on_status_changed(self, st: dict):
        self.peers_tile.set_value(
            str(st["peers"]),
            "connected" if st["peers"] > 0 else "no connections"
        )
        self.height_tile.set_value(f"{st['height']:,}", "latest tip")
        self.hashrate_tile.set_value(fmt_hashrate(st["hashrate"]), "this session")
        self.mined_tile.set_value(str(st["mined_count"]), "by you, this session")
        self.balance_tile.set_value(fmt_amount(st["balance"]), "ENTRO")

    def _on_log_line(self, line: str):
        self.log_pane.appendPlainText(line)

    def _on_block_mined(self, height: int):
        self.log_pane.appendPlainText(f"⛏  Mined block #{height}!")
        self.block_mined.emit(height)

    # ---- Public helpers --------------------------------------------------

    def update_address_indicator(self):
        """Show 'mining to A · viewing B' when the active account differs."""
        active = self._get_active_address()
        if self.mining and self.mining_address and active and active != self.mining_address:
            self.addr_indicator.setText(
                f"⛏  Mining to <span style='color:#ff6b9d;font-family:Consolas,monospace'>"
                f"{self.mining_address[:8]}…{self.mining_address[-4:]}</span> "
                f"· Active wallet account is <span style='color:#00d4aa'>{active[:8]}…{active[-4:]}</span>"
            )
            self.addr_indicator.setTextFormat(Qt.RichText)
            self.addr_indicator.setVisible(True)
        else:
            self.addr_indicator.setVisible(False)

    def request_stop(self):
        """Called by main window on app close."""
        if self.worker:
            self.worker.request_stop()
            self.worker.wait(5000)
