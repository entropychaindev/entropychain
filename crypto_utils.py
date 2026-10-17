"""
Cryptographic primitives for Entropy Chain.

Uses secp256k1 ECDSA for keypairs/signatures (same as Bitcoin/Ethereum)
and double SHA-256 for general hashing. Addresses are Base58Check
encoded with a custom version byte so they look distinct.
"""

import hashlib
import os
import base58
from ecdsa import SigningKey, VerifyingKey, SECP256k1, BadSignatureError

# Version byte for Entropy Chain addresses. Picked so addresses start
# with 'E' in Base58. Different from BTC (0x00) and most clones.
ADDR_VERSION = 0x21


def sha256d(data: bytes) -> bytes:
    """Double SHA-256, the standard 'hash' function on the chain."""
    return hashlib.sha256(hashlib.sha256(data).digest()).digest()


def sha256(data: bytes) -> bytes:
    return hashlib.sha256(data).digest()


def ripemd160(data: bytes) -> bytes:
    h = hashlib.new("ripemd160")
    h.update(data)
    return h.digest()


def generate_keypair() -> tuple[bytes, bytes]:
    """Returns (private_key_bytes, public_key_bytes_compressed)."""
    sk = SigningKey.generate(curve=SECP256k1)
    vk = sk.get_verifying_key()
    # Compressed pubkey: 33 bytes
    return sk.to_string(), vk.to_string("compressed")


def pubkey_to_address(pubkey: bytes) -> str:
    """Pubkey -> RIPEMD160(SHA256(pubkey)) -> versioned -> Base58Check."""
    h = ripemd160(sha256(pubkey))
    versioned = bytes([ADDR_VERSION]) + h
    checksum = sha256d(versioned)[:4]
    return base58.b58encode(versioned + checksum).decode()


def address_is_valid(addr: str) -> bool:
    try:
        decoded = base58.b58decode(addr)
        if len(decoded) != 25:
            return False
        payload, checksum = decoded[:21], decoded[21:]
        if sha256d(payload)[:4] != checksum:
            return False
        return payload[0] == ADDR_VERSION
    except Exception:
        return False


def sign(private_key: bytes, message: bytes) -> bytes:
    sk = SigningKey.from_string(private_key, curve=SECP256k1)
    return sk.sign_deterministic(message, hashfunc=hashlib.sha256)


def verify(public_key: bytes, signature: bytes, message: bytes) -> bool:
    try:
        vk = VerifyingKey.from_string(public_key, curve=SECP256k1)
        return vk.verify(signature, message, hashfunc=hashlib.sha256)
    except (BadSignatureError, Exception):
        return False
