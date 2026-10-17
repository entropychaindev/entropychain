"""
Wallet for Entropy Chain.

Two formats supported:
  1. PLAINTEXT (legacy)   - wallet.json with raw private keys in hex.
                            Still readable for compat with existing demos.
  2. ENCRYPTED (new)      - wallet.json with passphrase-encrypted key blob.
                            PBKDF2-SHA256 (100k iterations) -> AES-GCM.

Format detection is automatic: if the JSON file has a top-level "encrypted"
key, it's the new format; otherwise it's plaintext.

Encrypted format:
{
  "encrypted": true,
  "kdf": "pbkdf2-sha256",
  "iterations": 100000,
  "salt": "<hex 16 bytes>",
  "nonce": "<hex 12 bytes>",
  "ciphertext": "<hex>",
  "addresses": ["EXY...", ...]   # plaintext list of addresses for ls without unlock
}

The plaintext blob inside ciphertext is JSON: {addr: {private_key, public_key, label}}.
"""

import json
import os
import secrets
from typing import Optional

from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.kdf.pbkdf2 import PBKDF2HMAC
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

from crypto_utils import generate_keypair, pubkey_to_address
from transactions import Transaction


PBKDF2_ITERATIONS = 100_000
KDF_KEY_LEN = 32
SALT_LEN = 16
NONCE_LEN = 12


def _derive_key(passphrase: str, salt: bytes, iterations: int = PBKDF2_ITERATIONS) -> bytes:
    kdf = PBKDF2HMAC(
        algorithm=hashes.SHA256(),
        length=KDF_KEY_LEN,
        salt=salt,
        iterations=iterations,
    )
    return kdf.derive(passphrase.encode())


class WalletLockedError(Exception):
    """Raised when an operation needs the wallet unlocked but it isn't."""


class Wallet:
    """
    Wallet that auto-detects plaintext vs encrypted on load.

    Encrypted wallets must be unlocked with a passphrase before signing.
    Addresses are always visible (you can list them without unlocking).
    """

    def __init__(self, path: str = "./wallet.json"):
        self.path = path
        self._encrypted: bool = False
        # New plaintext wallets are unlocked by default; encrypted wallets
        # become unlocked only after successful unlock().
        self._unlocked: bool = True
        self._keys: dict[str, dict] = {}     # addr -> {private_key, public_key, label}
        self._addresses: list[str] = []      # always populated, even when locked
        # Encrypted-only fields
        self._salt: Optional[bytes] = None
        self._iterations: int = PBKDF2_ITERATIONS
        self._passphrase: Optional[str] = None  # held only if unlocked

        self._load()

    # ----------------- Load / save -----------------

    def _load(self) -> None:
        if not os.path.exists(self.path):
            return
        with open(self.path) as f:
            data = json.load(f)

        if isinstance(data, dict) and data.get("encrypted"):
            # New encrypted format
            self._encrypted = True
            self._salt = bytes.fromhex(data["salt"])
            self._iterations = int(data.get("iterations", PBKDF2_ITERATIONS))
            self._addresses = list(data.get("addresses", []))
            self._unlocked = False
        elif isinstance(data, dict):
            # Legacy plaintext format: top-level dict is addr -> {private_key, public_key, label}
            self._encrypted = False
            self._unlocked = True  # plaintext wallets are always "unlocked"
            self._keys = data
            self._addresses = list(data.keys())
        else:
            raise ValueError(f"Unrecognized wallet format at {self.path}")

    def _save_plaintext(self) -> None:
        os.makedirs(os.path.dirname(os.path.abspath(self.path)), exist_ok=True)
        with open(self.path, "w") as f:
            json.dump(self._keys, f, indent=2)

    def _save_encrypted(self, passphrase: str) -> None:
        os.makedirs(os.path.dirname(os.path.abspath(self.path)), exist_ok=True)
        if self._salt is None:
            self._salt = secrets.token_bytes(SALT_LEN)
        key = _derive_key(passphrase, self._salt, self._iterations)
        nonce = secrets.token_bytes(NONCE_LEN)
        plaintext = json.dumps(self._keys).encode()
        ciphertext = AESGCM(key).encrypt(nonce, plaintext, associated_data=None)
        envelope = {
            "encrypted": True,
            "kdf": "pbkdf2-sha256",
            "iterations": self._iterations,
            "salt": self._salt.hex(),
            "nonce": nonce.hex(),
            "ciphertext": ciphertext.hex(),
            "addresses": list(self._keys.keys()),
        }
        with open(self.path, "w") as f:
            json.dump(envelope, f, indent=2)
        self._encrypted = True
        self._passphrase = passphrase
        self._unlocked = True
        self._addresses = list(self._keys.keys())

    def _save(self) -> None:
        """Save in whatever format is current."""
        if self._encrypted:
            if not self._unlocked or self._passphrase is None:
                raise WalletLockedError("Cannot save: wallet is locked")
            self._save_encrypted(self._passphrase)
        else:
            self._save_plaintext()

    # ----------------- Status / unlock / migrate -----------------

    @property
    def is_encrypted(self) -> bool:
        return self._encrypted

    @property
    def is_unlocked(self) -> bool:
        return self._unlocked

    @property
    def exists(self) -> bool:
        return os.path.exists(self.path)

    def unlock(self, passphrase: str) -> bool:
        """Decrypt the wallet with a passphrase. Returns True on success."""
        if not self._encrypted:
            return True  # plaintext wallets are always 'unlocked'
        with open(self.path) as f:
            data = json.load(f)
        salt = bytes.fromhex(data["salt"])
        iterations = int(data.get("iterations", PBKDF2_ITERATIONS))
        nonce = bytes.fromhex(data["nonce"])
        ciphertext = bytes.fromhex(data["ciphertext"])
        try:
            key = _derive_key(passphrase, salt, iterations)
            plaintext = AESGCM(key).decrypt(nonce, ciphertext, associated_data=None)
            self._keys = json.loads(plaintext.decode())
            self._addresses = list(self._keys.keys())
            self._unlocked = True
            self._passphrase = passphrase
            self._salt = salt
            self._iterations = iterations
            return True
        except Exception:
            return False

    def lock(self) -> None:
        """Forget the in-memory plaintext keys. Stays encrypted on disk."""
        if self._encrypted:
            self._keys = {}
            self._unlocked = False
            self._passphrase = None
            # _addresses stays populated (visible without unlock)

    def encrypt(self, passphrase: str) -> None:
        """Convert a plaintext wallet to encrypted (or change passphrase)."""
        if not self._unlocked:
            raise WalletLockedError("Unlock the wallet before encrypting it")
        self._encrypted = True
        self._salt = secrets.token_bytes(SALT_LEN)
        self._save_encrypted(passphrase)

    # ----------------- Addresses & keys -----------------

    def addresses(self) -> list[str]:
        return list(self._addresses)

    def get_label(self, address: str) -> str:
        if not self._unlocked:
            return ""
        return self._keys.get(address, {}).get("label", "")

    def create_address(self, label: str = "", passphrase: Optional[str] = None) -> str:
        """
        Create a new keypair. If the wallet is empty and a passphrase is
        provided, the wallet is initialized as encrypted.
        """
        priv, pub = generate_keypair()
        addr = pubkey_to_address(pub)
        if not self._unlocked and self._encrypted:
            raise WalletLockedError("Unlock the wallet before creating addresses")
        if not self._encrypted and passphrase:
            # Initializing a fresh encrypted wallet
            self._keys[addr] = {
                "private_key": priv.hex(),
                "public_key": pub.hex(),
                "label": label,
            }
            self._unlocked = True   # we have the keys in memory
            self.encrypt(passphrase)  # writes to disk encrypted
            return addr
        self._keys[addr] = {
            "private_key": priv.hex(),
            "public_key": pub.hex(),
            "label": label,
        }
        self._addresses = list(self._keys.keys())
        self._save()
        return addr

    def export_private_key(self, address: str) -> str:
        """Return private key as hex. Requires unlocked wallet."""
        if not self._unlocked:
            raise WalletLockedError("Unlock the wallet before exporting keys")
        if address not in self._keys:
            raise ValueError(f"Address {address} not in wallet")
        return self._keys[address]["private_key"]

    def import_private_key(self, private_key_hex: str, label: str = "") -> str:
        """Import an existing private key. Requires unlocked wallet."""
        from ecdsa import SigningKey, SECP256k1
        if not self._unlocked:
            raise WalletLockedError("Unlock the wallet before importing keys")
        priv = bytes.fromhex(private_key_hex)
        sk = SigningKey.from_string(priv, curve=SECP256k1)
        pub = sk.get_verifying_key().to_string("compressed")
        addr = pubkey_to_address(pub)
        self._keys[addr] = {
            "private_key": priv.hex(),
            "public_key": pub.hex(),
            "label": label,
        }
        self._addresses = list(self._keys.keys())
        self._save()
        return addr

    # ----------------- Signing -----------------

    def sign_tx(self, tx: Transaction, address: str) -> Transaction:
        if not self._unlocked:
            raise WalletLockedError("Unlock the wallet before signing")
        if address not in self._keys:
            raise ValueError(f"Address {address} not in wallet")
        k = self._keys[address]
        tx.sender = address
        tx.sign_with(bytes.fromhex(k["private_key"]), bytes.fromhex(k["public_key"]))
        return tx
