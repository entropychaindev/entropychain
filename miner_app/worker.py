"""
MinerWorker — runs the EntropyChain node in a background thread,
emitting Qt signals so the GUI can update reactively.

Architecture (mirroring node.py):
  - Worker thread runs its own asyncio event loop
  - P2PNode runs in that loop
  - A separate mining thread runs the CPU-bound Miner.mine() loop
  - When the mining thread produces a block, we apply it locally and
    schedule an announcement on the asyncio loop (cross-thread safe via
    asyncio.run_coroutine_threadsafe)
  - Cross-thread state changes flow OUT to the GUI via Qt signals
"""
import asyncio
import logging
import sys
import threading
import time
import traceback
from pathlib import Path
from typing import Optional

from PySide6.QtCore import QThread, Signal

# Add the chain code directory to sys.path so we can import it.
# When bundled by PyInstaller, this becomes the temp _MEIPASS dir.
_CHAIN_DIR = Path(__file__).resolve().parent.parent
if str(_CHAIN_DIR) not in sys.path:
    sys.path.insert(0, str(_CHAIN_DIR))

from blockchain import EntropyChain                    # noqa: E402
from p2p import P2PNode                                # noqa: E402
from miner import Miner                                # noqa: E402
from wallet import Wallet                              # noqa: E402

from .paths import chain_data_dir, log_file            # noqa: E402


# Default network parameters. These need to match the live testnet.
# When mainnet launches, flip TESTNET_FAST to False.
DEFAULT_SEEDS = [("seed.entropychain.io", 9001)]
DEFAULT_PORT = 9001
TESTNET_FAST = True


def _apply_testnet_fast():
    """Set the relaxed Argon2 parameters before any chain code runs."""
    import consensus
    consensus.ARGON2_MEMORY_KB = 1024
    consensus.GENESIS_DIFFICULTY = 200


class _SignalLogHandler(logging.Handler):
    """Routes Python logging records into a Qt signal so the GUI can show them."""
    def __init__(self, signal):
        super().__init__()
        self.signal = signal

    def emit(self, record):
        try:
            msg = self.format(record)
            self.signal.emit(msg)
        except Exception:
            pass


class MinerWorker(QThread):
    # Lifecycle
    started_mining = Signal()
    stopped_mining = Signal()
    fatal_error = Signal(str)

    # Status updates (emitted from the worker thread, received on main thread)
    status_changed = Signal(dict)
    log_line = Signal(str)
    block_mined = Signal(int)

    def __init__(self, wallet_path: Path, parent=None):
        super().__init__(parent)
        self.wallet_path = wallet_path
        self.mine_to_address: Optional[str] = None  # if set, mine to this addr instead of wallet[0]
        self._loop: Optional[asyncio.AbstractEventLoop] = None
        self._chain: Optional[EntropyChain] = None
        self._p2p: Optional[P2PNode] = None
        self._wallet: Optional[Wallet] = None
        self._stop_event: Optional[asyncio.Event] = None
        self._stop_flag = threading.Event()
        self._mining_thread: Optional[threading.Thread] = None

        self._mined_count = 0
        self._mining_address = ""
        self._current_hashrate = 0.0

    def request_stop(self):
        """Signals the worker to shut down cleanly."""
        self._stop_flag.set()
        if self._loop and self._stop_event:
            self._loop.call_soon_threadsafe(self._stop_event.set)

    def run(self):
        log_handler = _SignalLogHandler(self.log_line)
        log_handler.setFormatter(logging.Formatter("%(asctime)s %(name)s %(message)s"))
        root_logger = logging.getLogger()
        root_logger.addHandler(log_handler)
        root_logger.setLevel(logging.INFO)

        try:
            file_handler = logging.FileHandler(str(log_file()), encoding="utf-8")
            file_handler.setFormatter(
                logging.Formatter("%(asctime)s %(name)s %(levelname)s %(message)s")
            )
            root_logger.addHandler(file_handler)
        except Exception:
            pass

        if TESTNET_FAST:
            _apply_testnet_fast()
            self.log_line.emit(
                "TESTNET-FAST mode active. Compatible with the public testnet."
            )

        self._loop = asyncio.new_event_loop()
        asyncio.set_event_loop(self._loop)
        try:
            self._loop.run_until_complete(self._main_async())
        except Exception as e:
            tb = traceback.format_exc()
            self.fatal_error.emit(f"{e}\n\n{tb}")
        finally:
            try:
                self._loop.close()
            except Exception:
                pass
            self.stopped_mining.emit()

    async def _main_async(self):
        self._stop_event = asyncio.Event()

        self._wallet = Wallet(path=str(self.wallet_path))
        if not self._wallet.addresses():
            addr = self._wallet.create_address("default")
            self.log_line.emit(f"Created mining address: {addr}")

        # Honor explicit mine_to_address if set, else fall back to first
        if self.mine_to_address and self.mine_to_address in self._wallet.addresses():
            self._mining_address = self.mine_to_address
        else:
            self._mining_address = self._wallet.addresses()[0]

        data_dir = str(chain_data_dir() / "chaindata")
        self.log_line.emit(f"Loading chain from {data_dir}")
        self._chain = EntropyChain(data_dir=data_dir)
        self.log_line.emit(
            f"Chain at height {self._chain.height}. "
            f"Mining to: {self._mining_address}"
        )

        self._p2p = P2PNode(
            chain=self._chain,
            listen_port=DEFAULT_PORT,
            seed_nodes=DEFAULT_SEEDS,
        )
        await self._p2p.start()
        self.log_line.emit(f"P2P listening on port {DEFAULT_PORT}, dialing seeds...")

        miner = Miner(self._chain, self._mining_address)
        self._mining_thread = threading.Thread(
            target=self._mining_loop, args=(miner,), daemon=True
        )
        self._mining_thread.start()

        self.started_mining.emit()

        try:
            while not self._stop_event.is_set():
                self._emit_status()
                try:
                    await asyncio.wait_for(self._stop_event.wait(), timeout=2.0)
                except asyncio.TimeoutError:
                    pass
        finally:
            self.log_line.emit("Stopping miner...")
            self._stop_flag.set()
            try:
                self._p2p.stop()
            except Exception:
                pass
            if self._mining_thread:
                self._mining_thread.join(timeout=3.0)

    def _mining_loop(self, miner: Miner):
        """CPU-bound; runs in its own OS thread."""
        loop = self._loop
        while not self._stop_flag.is_set():
            t0 = time.time()
            try:
                block = miner.mine(max_attempts=200_000, log_every=200_000)
            except Exception as e:
                self.log_line.emit(f"Mining error: {e}")
                time.sleep(1)
                continue

            elapsed = max(time.time() - t0, 1e-6)
            # Estimate: if we got a block before exhausting attempts,
            # we don't know exactly how many attempts. Use rate from
            # full-attempt rounds for hashrate display.
            if block is None:
                self._current_hashrate = 200_000 / elapsed

            if self._stop_flag.is_set():
                break
            if block is None:
                continue

            try:
                ok, reason = self._chain.add_block(block)
            except Exception as e:
                self.log_line.emit(f"Apply-block error: {e}")
                continue

            if ok:
                self._mined_count += 1
                self.block_mined.emit(block.height)
                if loop and self._p2p:
                    try:
                        asyncio.run_coroutine_threadsafe(
                            self._p2p.announce_local_block(block), loop
                        )
                    except Exception as e:
                        self.log_line.emit(f"Announce error: {e}")

    def _emit_status(self):
        try:
            height = self._chain.height if self._chain else 0
        except Exception:
            height = 0

        peer_count = 0
        if self._p2p:
            try:
                peer_count = len(self._p2p.peers)
            except Exception:
                pass

        balance = 0
        if self._chain and self._mining_address:
            try:
                balance = self._chain.get_balance(self._mining_address)
            except Exception:
                pass

        self.status_changed.emit({
            "height": height,
            "peers": peer_count,
            "hashrate": self._current_hashrate,
            "mined_count": self._mined_count,
            "balance": balance,
            "address": self._mining_address,
        })
