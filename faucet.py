"""
EntropyChain Testnet-1 Faucet

Drips 100 ENTRO to any valid testnet-4 address, with per-IP and
per-recipient cooldowns. Backed by its own dedicated wallet (separate
from the seed node's mining wallet for security isolation).

The faucet:
  1. Loads a plaintext wallet at --wallet-path
  2. Hits an upstream RPC (--rpc-url) to verify the chain and submit txs
  3. Persists drip history at --state-path so cooldowns survive restarts
  4. Serves a JSON API and a static HTML page on --port

API:
  GET  /api/info               Faucet status (balance, drip amount, etc.)
  POST /api/request            { address: "E..." } -> { ok, tx_hash, ... }
  GET  /api/recent             Last 20 successful drips (for the UI feed)
  GET  /                       The static HTML UI

Rate limits:
  - Per IP: 1 successful request per 24h
  - Per recipient address: 1 successful request per 24h
  Both must be satisfied. A failed submission does NOT consume the budget.

Run:
  python faucet.py \\
      --wallet-path ~/data/faucet-wallet.json \\
      --state-path ~/data/faucet-state.json \\
      --rpc-url http://127.0.0.1:8001 \\
      --port 8090 \\
      --static-dir ~/static-faucet
"""

import argparse
import json
import logging
import os
import threading
import time
from pathlib import Path
from typing import Optional

import requests
from flask import Flask, jsonify, request, send_from_directory

from crypto_utils import sign as crypto_sign
from transactions import make_transfer
from wallet import Wallet

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(name)s %(levelname)s %(message)s",
)
log = logging.getLogger("faucet")


DEFAULT_DRIP_AMOUNT = 100_00000000   # 100 ENTRO in smallest units
DEFAULT_FEE = 10                       # smallest units, paid by faucet
DEFAULT_COOLDOWN_SECONDS = 24 * 60 * 60   # 24 hours
RECENT_DRIPS_KEEP = 50                 # cap on history kept in memory
NATIVE_TOKEN = "ENTRO"


class FaucetState:
    """Tracks per-IP and per-address last-drip timestamps, plus recent drips."""

    def __init__(self, path: str):
        self.path = path
        self.lock = threading.Lock()
        # ip -> last drip timestamp (unix seconds)
        self.ip_last: dict[str, float] = {}
        # address -> last drip timestamp
        self.addr_last: dict[str, float] = {}
        # list of {ts, address, ip_prefix, tx_hash, amount} - most recent first
        self.recent: list[dict] = []
        self._load()

    def _load(self):
        if not os.path.exists(self.path):
            return
        try:
            with open(self.path) as f:
                data = json.load(f)
            self.ip_last = data.get("ip_last", {})
            self.addr_last = data.get("addr_last", {})
            self.recent = data.get("recent", [])
            log.info(f"Loaded faucet state: {len(self.ip_last)} IPs, "
                     f"{len(self.addr_last)} addresses tracked, "
                     f"{len(self.recent)} recent drips.")
        except Exception as e:
            log.warning(f"Could not load faucet state {self.path}: {e}")

    def _save_locked(self):
        # Caller must hold self.lock
        tmp = self.path + ".tmp"
        with open(tmp, "w") as f:
            json.dump({
                "ip_last": self.ip_last,
                "addr_last": self.addr_last,
                "recent": self.recent,
            }, f, indent=2)
        os.replace(tmp, self.path)

    def can_drip(self, ip: str, addr: str, cooldown: float) -> tuple[bool, str, float]:
        """Returns (allowed, reason_if_not, seconds_until_next_eligible)."""
        now = time.time()
        with self.lock:
            ip_last = self.ip_last.get(ip, 0)
            addr_last = self.addr_last.get(addr, 0)
            ip_wait = max(0, (ip_last + cooldown) - now)
            addr_wait = max(0, (addr_last + cooldown) - now)
            wait = max(ip_wait, addr_wait)
            if ip_wait > 0:
                return False, f"IP rate limit: try again in {fmt_duration(ip_wait)}", wait
            if addr_wait > 0:
                return False, f"Address rate limit: try again in {fmt_duration(addr_wait)}", wait
            return True, "", 0.0

    def record_drip(self, ip: str, addr: str, tx_hash: str, amount: int):
        now = time.time()
        with self.lock:
            self.ip_last[ip] = now
            self.addr_last[addr] = now
            self.recent.insert(0, {
                "ts": now,
                "address": addr,
                "ip_prefix": ip_prefix(ip),
                "tx_hash": tx_hash,
                "amount": amount,
            })
            self.recent = self.recent[:RECENT_DRIPS_KEEP]
            self._save_locked()


def ip_prefix(ip: str) -> str:
    """Return a privacy-friendly partial IP for public display.
    "1.2.3.4" -> "1.2.x.x"
    "::1" -> "::x"
    """
    if "." in ip:
        parts = ip.split(".")
        if len(parts) == 4:
            return f"{parts[0]}.{parts[1]}.x.x"
    if ":" in ip:
        return ip.split(":")[0] + ":x"
    return "x"


def fmt_duration(seconds: float) -> str:
    s = int(seconds)
    if s >= 3600:
        h = s // 3600
        m = (s % 3600) // 60
        return f"{h}h {m}m" if m else f"{h}h"
    if s >= 60:
        return f"{s // 60}m"
    return f"{s}s"


def get_client_ip(req) -> str:
    """Extract client IP, honoring X-Forwarded-For when behind a reverse proxy."""
    xff = req.headers.get("X-Forwarded-For", "")
    if xff:
        # Use the leftmost (original client) entry
        return xff.split(",")[0].strip()
    real = req.headers.get("X-Real-IP", "")
    if real:
        return real.strip()
    return req.remote_addr or "unknown"


def looks_like_address(s: str) -> bool:
    """Cheap pre-check; the chain will reject malformed addresses authoritatively."""
    if not isinstance(s, str):
        return False
    if not (25 <= len(s) <= 40):
        return False
    if not s.startswith("E"):
        return False
    return all(c.isalnum() for c in s)


class Faucet:
    def __init__(
        self,
        wallet_path: str,
        state_path: str,
        rpc_url: str,
        drip_amount: int,
        fee: int,
        cooldown: float,
        static_dir: Optional[str] = None,
    ):
        self.rpc_url = rpc_url.rstrip("/")
        self.drip_amount = drip_amount
        self.fee = fee
        self.cooldown = cooldown
        self.static_dir = static_dir
        self.state = FaucetState(state_path)

        # Load wallet (must be plaintext, since this runs unattended)
        if not os.path.exists(wallet_path):
            raise SystemExit(f"Faucet wallet not found at {wallet_path}. "
                             f"Run generate_faucet_key.py to create one.")
        self.wallet = Wallet(wallet_path)
        if self.wallet.is_encrypted and not self.wallet.is_unlocked:
            raise SystemExit(f"Faucet wallet at {wallet_path} is encrypted. "
                             f"The faucet runs unattended and needs a plaintext wallet.")
        addrs = self.wallet.addresses()
        if not addrs:
            raise SystemExit(f"Faucet wallet has no addresses.")
        self.faucet_address = addrs[0]
        log.info(f"Faucet wallet loaded. Drip address: {self.faucet_address}")
        log.info(f"Drip amount: {drip_amount / 1e8} ENTRO  Cooldown: {fmt_duration(cooldown)}")

    # ---------------- Chain interactions ----------------

    def _rpc_get(self, path: str) -> dict:
        r = requests.get(f"{self.rpc_url}{path}", timeout=10)
        r.raise_for_status()
        return r.json()

    def _rpc_post(self, path: str, body: dict) -> dict:
        r = requests.post(f"{self.rpc_url}{path}", json=body, timeout=10)
        return r.json()

    def get_balance(self) -> int:
        try:
            data = self._rpc_get(f"/rpc/balance/{self.faucet_address}")
            return int(data.get(NATIVE_TOKEN, 0))
        except Exception as e:
            log.warning(f"Balance query failed: {e}")
            return -1

    def get_chain_info(self) -> dict:
        try:
            return self._rpc_get("/rpc/info")
        except Exception as e:
            log.warning(f"Info query failed: {e}")
            return {}

    def submit_drip(self, recipient: str) -> tuple[bool, str, str]:
        """
        Build, sign, and submit a transfer. Returns (ok, tx_hash_or_reason, reason_human).
        """
        try:
            tx = make_transfer(
                sender=self.faucet_address,
                recipient=recipient,
                amount=self.drip_amount,
                token=NATIVE_TOKEN,
                fee=self.fee,
            )
            self.wallet.sign_tx(tx, self.faucet_address)
            res = self._rpc_post("/rpc/submit_tx", {"tx": tx.to_dict()})
            if res.get("ok"):
                return True, res.get("tx_hash", ""), ""
            return False, "", res.get("reason", "rejected")
        except Exception as e:
            log.exception("submit_drip failed")
            return False, "", f"internal: {e.__class__.__name__}"

    # ---------------- HTTP handlers ----------------

    def make_app(self) -> Flask:
        app = Flask("faucet")

        @app.after_request
        def add_cors(resp):
            resp.headers["Access-Control-Allow-Origin"] = "*"
            resp.headers["Access-Control-Allow-Methods"] = "GET, POST, OPTIONS"
            resp.headers["Access-Control-Allow-Headers"] = "Content-Type"
            return resp

        @app.route("/api/info")
        def info():
            chain = self.get_chain_info()
            balance = self.get_balance()
            return jsonify({
                "faucet_address": self.faucet_address,
                "drip_amount": self.drip_amount,
                "drip_amount_human": self.drip_amount / 1e8,
                "fee": self.fee,
                "cooldown_seconds": self.cooldown,
                "balance": balance,
                "balance_human": balance / 1e8 if balance >= 0 else None,
                "drips_remaining": (balance // (self.drip_amount + self.fee)) if balance > 0 else 0,
                "network": {
                    "network_id": chain.get("network_id"),
                    "height": chain.get("height"),
                    "genesis_hash": chain.get("genesis_hash"),
                },
            })

        @app.route("/api/recent")
        def recent():
            return jsonify({"recent": self.state.recent})

        @app.route("/api/request", methods=["POST", "OPTIONS"])
        def do_request():
            if request.method == "OPTIONS":
                return ("", 204)
            body = request.get_json(silent=True) or {}
            recipient = (body.get("address") or "").strip()
            ip = get_client_ip(request)

            if not looks_like_address(recipient):
                return jsonify({"ok": False, "reason": "Address looks malformed"}), 400

            allowed, reason, wait = self.state.can_drip(ip, recipient, self.cooldown)
            if not allowed:
                return jsonify({
                    "ok": False, "reason": reason, "retry_after_seconds": int(wait),
                }), 429

            balance = self.get_balance()
            if balance < self.drip_amount + self.fee:
                return jsonify({
                    "ok": False,
                    "reason": "Faucet is empty. Please notify the operator.",
                }), 503

            ok, tx_hash, fail_reason = self.submit_drip(recipient)
            if not ok:
                return jsonify({"ok": False, "reason": fail_reason}), 400

            self.state.record_drip(ip, recipient, tx_hash, self.drip_amount)
            log.info(f"DRIP {self.drip_amount/1e8} ENTRO -> {recipient} (ip={ip_prefix(ip)}) tx={tx_hash}")

            return jsonify({
                "ok": True,
                "tx_hash": tx_hash,
                "amount": self.drip_amount,
                "amount_human": self.drip_amount / 1e8,
                "recipient": recipient,
                "next_drip_in_seconds": int(self.cooldown),
            })

        # Static UI
        if self.static_dir:
            @app.route("/")
            def index():
                return send_from_directory(self.static_dir, "index.html")

            @app.route("/<path:path>")
            def static_files(path):
                return send_from_directory(self.static_dir, path)

        return app


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--wallet-path", required=True, help="Plaintext faucet wallet JSON")
    ap.add_argument("--state-path", default="./faucet-state.json", help="Where to persist drip history")
    ap.add_argument("--rpc-url", default="http://127.0.0.1:8001", help="Upstream node RPC")
    ap.add_argument("--host", default="127.0.0.1", help="Bind address")
    ap.add_argument("--port", type=int, default=8090, help="Bind port")
    ap.add_argument("--drip-amount", type=int, default=DEFAULT_DRIP_AMOUNT,
                    help="Amount per drip in smallest units (default 1000 ENTRO)")
    ap.add_argument("--fee", type=int, default=DEFAULT_FEE, help="Tx fee paid by faucet")
    ap.add_argument("--cooldown-seconds", type=int, default=DEFAULT_COOLDOWN_SECONDS,
                    help="Cooldown between drips for both IP and address")
    ap.add_argument("--static-dir", default=None, help="Directory containing index.html for the UI")
    args = ap.parse_args()

    faucet = Faucet(
        wallet_path=args.wallet_path,
        state_path=args.state_path,
        rpc_url=args.rpc_url,
        drip_amount=args.drip_amount,
        fee=args.fee,
        cooldown=args.cooldown_seconds,
        static_dir=args.static_dir,
    )
    app = faucet.make_app()
    log.info(f"Faucet listening on http://{args.host}:{args.port}")
    app.run(host=args.host, port=args.port, debug=False, use_reloader=False)


if __name__ == "__main__":
    main()
