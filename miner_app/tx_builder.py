"""
Transaction builder for the desktop wallet.

Wraps the chain's transaction constructors with a clean interface
that signs via the local Wallet and submits via the RPC.
"""
from __future__ import annotations

import sys
from pathlib import Path
from typing import Optional

_CHAIN_DIR = Path(__file__).resolve().parent.parent
if str(_CHAIN_DIR) not in sys.path:
    sys.path.insert(0, str(_CHAIN_DIR))

from transactions import (                         # noqa: E402
    make_transfer, make_token_create, make_token_mint,
)
from wallet import Wallet                          # noqa: E402

from .rpc_client import RpcClient, RpcError         # noqa: E402


# Native token, decimals — must match chain
NATIVE = "ENTRO"
DECIMALS = 8
SCALE = 10 ** DECIMALS


def to_units(human: str | float, decimals: int = DECIMALS) -> int:
    """Convert '1.5' or 1.5 to integer units (e.g. 150_000_000)."""
    s = str(human).strip()
    if not s:
        raise ValueError("Empty amount")
    if "." in s:
        whole, frac = s.split(".", 1)
        whole = whole or "0"
        frac = (frac + "0" * decimals)[:decimals]
        return int(whole) * (10 ** decimals) + int(frac)
    return int(s) * (10 ** decimals)


def fmt_amount(units, decimals: int = DECIMALS) -> str:
    """Convert integer units (int or str) to human-readable string."""
    if units is None:
        return "—"
    try:
        n = int(units)
    except (TypeError, ValueError):
        return "—"
    sign = "-" if n < 0 else ""
    n = abs(n)
    whole = n // (10 ** decimals)
    frac = n % (10 ** decimals)
    if frac == 0:
        return f"{sign}{whole:,}"
    s = f"{sign}{whole:,}.{frac:0{decimals}d}".rstrip("0").rstrip(".")
    return s


class TxSubmitError(Exception):
    pass


class TxBuilder:
    def __init__(self, wallet: Wallet, rpc: RpcClient):
        self.wallet = wallet
        self.rpc = rpc

    # ---- Transfer (ENTRO or any token) ----------------------------------

    def transfer(self, sender: str, recipient: str,
                 amount_human: str, token: str = NATIVE,
                 fee: int = 1) -> dict:
        units = to_units(amount_human)
        tx = make_transfer(sender=sender, recipient=recipient,
                           amount=units, token=token, fee=fee)
        return self._sign_and_submit(tx, sender)

    # ---- Token create ---------------------------------------------------

    def token_create(self, sender: str, symbol: str, name: str,
                     total_supply_human: str, decimals: int = 8,
                     fee: int = 100) -> dict:
        units = to_units(total_supply_human, decimals=decimals)
        tx = make_token_create(sender=sender, symbol=symbol, name=name,
                               total_supply=units, decimals=decimals, fee=fee)
        return self._sign_and_submit(tx, sender)

    # ---- Token mint -----------------------------------------------------

    def token_mint(self, sender: str, symbol: str, recipient: str,
                   amount_human: str, decimals: int = 8, fee: int = 10) -> dict:
        units = to_units(amount_human, decimals=decimals)
        tx = make_token_mint(sender=sender, symbol=symbol,
                             recipient=recipient, amount=units, fee=fee)
        return self._sign_and_submit(tx, sender)

    # ---- Internal -------------------------------------------------------

    def _sign_and_submit(self, tx, sender: str) -> dict:
        signed = self.wallet.sign_tx(tx, sender)
        try:
            # RPC expects the transaction wrapped under a 'tx' key
            resp = self.rpc.submit_tx({"tx": signed.to_dict()})
        except RpcError as e:
            raise TxSubmitError(str(e)) from e
        if not resp.get("ok"):
            reason = resp.get("reason") or resp.get("error") or "unknown error"
            raise TxSubmitError(reason)
        return resp
