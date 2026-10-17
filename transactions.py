"""
Native transaction types for Entropy Chain.

No general-purpose VM. Instead, every "thing you can do" is a typed
transaction the protocol understands directly. This is faster, simpler
to audit, and impossible to write bad smart contracts against because
there are no smart contracts to write.

Transaction types:
  TRANSFER         - move ENTRO or any token between addresses
  TOKEN_CREATE     - mint a new fungible token (ENTRO-20 equivalent)
  TOKEN_MINT       - issue more of a token you control
  NFT_CREATE       - create a new NFT collection
  NFT_MINT         - mint an NFT in a collection you control
  NFT_TRANSFER     - move an NFT to another address
"""

import json
import time
import secrets
from dataclasses import dataclass, asdict, field
from typing import Optional
from crypto_utils import sha256d, sign, verify, pubkey_to_address

# Transaction type constants
TX_TRANSFER = "transfer"
TX_TOKEN_CREATE = "token_create"
TX_TOKEN_MINT = "token_mint"
TX_NFT_CREATE = "nft_create"
TX_NFT_MINT = "nft_mint"
TX_NFT_TRANSFER = "nft_transfer"
TX_LOTTERY_CREATE = "lottery_create"
TX_LOTTERY_ENTER = "lottery_enter"
TX_LOTTERY_DRAW = "lottery_draw"
TX_NFT_RANDOM_MINT = "nft_random_mint"
TX_COINBASE = "coinbase"  # Block reward, no signature needed

# Native chain currency symbol
NATIVE_TOKEN = "ENTRO"


@dataclass
class Transaction:
    tx_type: str
    sender: str  # address (empty for coinbase)
    timestamp: float = field(default_factory=time.time)
    nonce: str = field(default_factory=lambda: secrets.token_hex(8))
    data: dict = field(default_factory=dict)
    public_key: Optional[str] = None  # hex of compressed pubkey
    signature: Optional[str] = None  # hex of DER-encoded signature
    fee: int = 0  # paid in ENTRO (smallest unit)

    def signing_payload(self) -> bytes:
        """Bytes to sign - everything except the signature itself."""
        d = {
            "tx_type": self.tx_type,
            "sender": self.sender,
            "timestamp": self.timestamp,
            "nonce": self.nonce,
            "data": self.data,
            "fee": self.fee,
        }
        return json.dumps(d, sort_keys=True).encode()

    def hash(self) -> str:
        return sha256d(self.signing_payload()).hex()

    def sign_with(self, private_key: bytes, public_key: bytes) -> None:
        self.public_key = public_key.hex()
        sig = sign(private_key, self.signing_payload())
        self.signature = sig.hex()

    def verify_signature(self) -> bool:
        if self.tx_type == TX_COINBASE:
            return True
        if not self.public_key or not self.signature:
            return False
        # Check pubkey actually corresponds to claimed sender
        derived_addr = pubkey_to_address(bytes.fromhex(self.public_key))
        if derived_addr != self.sender:
            return False
        return verify(
            bytes.fromhex(self.public_key),
            bytes.fromhex(self.signature),
            self.signing_payload(),
        )

    def to_dict(self) -> dict:
        return asdict(self)

    @classmethod
    def from_dict(cls, d: dict) -> "Transaction":
        return cls(**d)


# ---- Constructors for each transaction type ----

def make_transfer(sender: str, recipient: str, amount: int,
                  token: str = NATIVE_TOKEN, fee: int = 1) -> Transaction:
    return Transaction(
        tx_type=TX_TRANSFER,
        sender=sender,
        fee=fee,
        data={"recipient": recipient, "amount": amount, "token": token},
    )


def make_token_create(sender: str, symbol: str, name: str,
                      total_supply: int, decimals: int = 8,
                      fee: int = 100) -> Transaction:
    return Transaction(
        tx_type=TX_TOKEN_CREATE,
        sender=sender,
        fee=fee,
        data={
            "symbol": symbol.upper(),
            "name": name,
            "total_supply": total_supply,
            "decimals": decimals,
        },
    )


def make_token_mint(sender: str, symbol: str, recipient: str,
                    amount: int, fee: int = 10) -> Transaction:
    return Transaction(
        tx_type=TX_TOKEN_MINT,
        sender=sender,
        fee=fee,
        data={"symbol": symbol.upper(), "recipient": recipient, "amount": amount},
    )


def make_nft_create(sender: str, collection_id: str, name: str,
                    description: str = "", fee: int = 100) -> Transaction:
    return Transaction(
        tx_type=TX_NFT_CREATE,
        sender=sender,
        fee=fee,
        data={"collection_id": collection_id, "name": name, "description": description},
    )


def make_nft_mint(sender: str, collection_id: str, token_id: str,
                  recipient: str, metadata_uri: str = "",
                  traits: Optional[dict] = None, fee: int = 5) -> Transaction:
    return Transaction(
        tx_type=TX_NFT_MINT,
        sender=sender,
        fee=fee,
        data={
            "collection_id": collection_id,
            "token_id": token_id,
            "recipient": recipient,
            "metadata_uri": metadata_uri,
            "traits": traits or {},
        },
    )


def make_nft_transfer(sender: str, collection_id: str, token_id: str,
                      recipient: str, fee: int = 1) -> Transaction:
    return Transaction(
        tx_type=TX_NFT_TRANSFER,
        sender=sender,
        fee=fee,
        data={"collection_id": collection_id, "token_id": token_id, "recipient": recipient},
    )


def make_coinbase(miner: str, reward: int, height: int) -> Transaction:
    """Coinbase has no signature and creates ENTRO out of thin air per consensus."""
    tx = Transaction(
        tx_type=TX_COINBASE,
        sender="",
        data={"recipient": miner, "amount": reward, "height": height},
    )
    return tx


# ---- Lottery: a 3-stage flow ----
# 1. CREATE: organizer locks the prize and names the draw_height (must be future)
# 2. ENTER: anyone pays ticket_price to enter, until draw_height is reached
# 3. DRAW: anyone can submit at/after draw_height; winner is computed from
#    that block's entropy_seed. The protocol enforces correctness.

def make_lottery_create(sender: str, lottery_id: str, prize_amount: int,
                        ticket_price: int, draw_height: int,
                        token: str = NATIVE_TOKEN, fee: int = 50) -> Transaction:
    return Transaction(
        tx_type=TX_LOTTERY_CREATE,
        sender=sender,
        fee=fee,
        data={
            "lottery_id": lottery_id,
            "prize_amount": prize_amount,
            "ticket_price": ticket_price,
            "draw_height": draw_height,
            "token": token,
        },
    )


def make_lottery_enter(sender: str, lottery_id: str, fee: int = 1) -> Transaction:
    return Transaction(
        tx_type=TX_LOTTERY_ENTER,
        sender=sender,
        fee=fee,
        data={"lottery_id": lottery_id},
    )


def make_lottery_draw(sender: str, lottery_id: str, fee: int = 1) -> Transaction:
    """Anyone can submit the draw once draw_height is reached. The winner
    is determined by protocol from the draw block's entropy_seed, not by
    the submitter."""
    return Transaction(
        tx_type=TX_LOTTERY_DRAW,
        sender=sender,
        fee=fee,
        data={"lottery_id": lottery_id},
    )


def make_nft_random_mint(sender: str, collection_id: str, token_id: str,
                         recipient: str, entropy_height: int,
                         metadata_uri: str = "", fee: int = 5) -> Transaction:
    """Mint an NFT whose traits will be derived from the entropy_seed of
    the block at entropy_height. That block must already be on-chain when
    this tx is included, so traits are deterministic and verifiable, but
    the seed was committed before the mint was conceived."""
    return Transaction(
        tx_type=TX_NFT_RANDOM_MINT,
        sender=sender,
        fee=fee,
        data={
            "collection_id": collection_id,
            "token_id": token_id,
            "recipient": recipient,
            "metadata_uri": metadata_uri,
            "entropy_height": entropy_height,
        },
    )
