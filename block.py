"""
Block structure for Entropy Chain.

Each block:
  - height
  - prev_hash
  - merkle_root of transactions
  - timestamp
  - difficulty
  - entropy_seed (the public randomness contribution)
  - miner_nonce (used to derive entropy_seed)
  - nonce (PoW search nonce)
  - transactions[]
"""

import json
import time
import struct
from dataclasses import dataclass, field, asdict
from typing import Optional
from crypto_utils import sha256d
from transactions import Transaction


def merkle_root(tx_hashes: list[str]) -> str:
    """Standard pairwise SHA-256 merkle root. Empty list -> zero hash."""
    if not tx_hashes:
        return "00" * 32
    layer = [bytes.fromhex(h) for h in tx_hashes]
    while len(layer) > 1:
        if len(layer) % 2 == 1:
            layer.append(layer[-1])  # duplicate last to pair up
        layer = [sha256d(layer[i] + layer[i + 1]) for i in range(0, len(layer), 2)]
    return layer[0].hex()


@dataclass
class Block:
    height: int
    prev_hash: str
    merkle_root_hex: str
    timestamp: float
    difficulty: int
    entropy_seed: str  # hex, 32 bytes
    miner_nonce: str   # hex, 16 bytes - input to entropy_seed derivation
    nonce: int          # PoW search nonce
    transactions: list[Transaction] = field(default_factory=list)

    def header_bytes(self) -> bytes:
        """
        Serialize the header for PoW hashing. Order matters and must be
        deterministic. The PoW nonce is NOT in here because it gets
        appended separately in pow_hash().
        """
        h = struct.pack("<I", self.height)
        h += bytes.fromhex(self.prev_hash) if self.prev_hash else b"\x00" * 32
        h += bytes.fromhex(self.merkle_root_hex)
        h += struct.pack("<d", self.timestamp)
        h += struct.pack("<Q", self.difficulty)
        h += bytes.fromhex(self.entropy_seed)
        h += bytes.fromhex(self.miner_nonce)
        return h

    def hash(self) -> str:
        """The canonical block hash includes the PoW nonce."""
        return sha256d(self.header_bytes() + struct.pack("<Q", self.nonce)).hex()

    def to_dict(self) -> dict:
        d = asdict(self)
        d["transactions"] = [tx.to_dict() for tx in self.transactions]
        return d

    @classmethod
    def from_dict(cls, d: dict) -> "Block":
        d = dict(d)
        d["transactions"] = [Transaction.from_dict(tx) for tx in d["transactions"]]
        return cls(**d)
