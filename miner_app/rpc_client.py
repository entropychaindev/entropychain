"""
HTTP client for the public EntropyChain RPC.

We hit the same endpoints the browser wallet hits. This way the desktop
app shows the network's view of an address (balances, tokens, history)
even when the user's local node is mid-sync.

All calls are blocking; callers should run them off the GUI thread.
"""
from __future__ import annotations

import json
from typing import Optional
from urllib.error import URLError, HTTPError
from urllib.request import Request, urlopen


DEFAULT_RPC_URL = "https://rpc.entropychain.io"


class RpcError(Exception):
    pass


class RpcClient:
    def __init__(self, base_url: str = DEFAULT_RPC_URL, timeout: float = 8.0):
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

    # ---- low-level ------------------------------------------------------

    def _get(self, path: str) -> dict:
        url = f"{self.base_url}{path}"
        try:
            req = Request(url, headers={"Accept": "application/json"})
            with urlopen(req, timeout=self.timeout) as r:
                body = r.read().decode("utf-8")
                return json.loads(body)
        except HTTPError as e:
            if e.code == 404:
                return {}
            raise RpcError(f"HTTP {e.code} from {url}") from e
        except (URLError, json.JSONDecodeError) as e:
            raise RpcError(f"RPC error: {e}") from e

    def _post(self, path: str, body: dict) -> dict:
        url = f"{self.base_url}{path}"
        data = json.dumps(body).encode("utf-8")
        try:
            req = Request(
                url,
                data=data,
                headers={"Content-Type": "application/json"},
                method="POST",
            )
            with urlopen(req, timeout=self.timeout) as r:
                return json.loads(r.read().decode("utf-8"))
        except HTTPError as e:
            try:
                msg = e.read().decode("utf-8")
            except Exception:
                msg = str(e)
            raise RpcError(f"HTTP {e.code}: {msg}") from e
        except (URLError, json.JSONDecodeError) as e:
            raise RpcError(f"RPC error: {e}") from e

    # ---- public endpoints (mirror what browser wallet uses) -----------

    def info(self) -> dict:
        """Network info: height, difficulty, peer count, genesis."""
        return self._get("/rpc/info")

    def balance(self, address: str, token: Optional[str] = None) -> dict:
        """All-token balance for an address, or single-token if token given."""
        if token:
            return self._get(f"/rpc/balance/{address}/{token}")
        return self._get(f"/rpc/balance/{address}")

    def tokens(self) -> dict:
        """All tokens created on the chain."""
        return self._get("/rpc/tokens")

    def token(self, symbol: str) -> dict:
        """Single token detail (name, decimals, supply, creator)."""
        return self._get(f"/rpc/token/{symbol}")

    def history(self, address: str, limit: int = 50,
                before_height: Optional[int] = None) -> dict:
        """
        Recent txs involving an address. Returns {history: [...], count: N}.
        The endpoint is backed by an in-memory index on the chain side, so
        this is fast at any chain length.
        """
        path = f"/rpc/history/{address}?limit={limit}"
        if before_height is not None:
            path += f"&before={before_height}"
        try:
            return self._get(path)
        except RpcError:
            # Older RPCs may not have the endpoint
            return {"history": [], "count": 0}

    def submit_tx(self, signed_tx: dict) -> dict:
        """Broadcast a signed transaction. Returns {ok, hash} or {error}."""
        return self._post("/rpc/submit_tx", signed_tx)
