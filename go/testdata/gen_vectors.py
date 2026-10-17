"""
Generate cross-implementation test vectors from the Python reference code.

Run from anywhere:   python go/testdata/gen_vectors.py
Writes:              go/testdata/vectors.json

The Go test suite (go test ./...) loads vectors.json and checks that the
Go implementation reproduces every hash, payload, address and signature
byte for byte. Re-run this whenever the Python consensus code changes.
"""

import hashlib
import json
import os
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.abspath(os.path.join(HERE, "..", ".."))
sys.path.insert(0, ROOT)

from ecdsa import SigningKey, SECP256k1  # noqa: E402

import consensus  # noqa: E402
from block import Block, merkle_root  # noqa: E402
from blockchain import (  # noqa: E402
    EntropyChain, block_reward_at_height, pick_winner_from_seed, derive_random_traits,
)
from crypto_utils import pubkey_to_address, sign, verify, sha256d  # noqa: E402
from transactions import (  # noqa: E402
    Transaction, make_transfer, make_token_create, make_nft_mint, make_coinbase,
    make_lottery_create, make_nft_random_mint,
)
from wallet import Wallet  # noqa: E402


def priv_for(seed: str) -> bytes:
    return hashlib.sha256(seed.encode()).digest()


def pub_for(priv: bytes) -> bytes:
    return SigningKey.from_string(priv, curve=SECP256k1).get_verifying_key().to_string("compressed")


def pub_uncompressed(priv: bytes) -> bytes:
    return SigningKey.from_string(priv, curve=SECP256k1).get_verifying_key().to_string("uncompressed")


out = {}

# ---- JSON canonicalisation ----
floats = [0.0, -0.0, 0.1, 0.5, 1.0, 2.5, 100.0, 1e15, 1e16, 1.5e16, 123456789012345678.0,
          0.0001, 0.00001, 1.5e-5, 1e-7, 1759449600.0, 1759449600.123456, 1714000000.5,
          5e-324, 1.7976931348623157e308, 1e22, 0.30000000000000004, -12.75, 3.14159e100]
out["floats"] = [{"value": repr(f), "json": json.dumps(f)} for f in floats]

json_texts = [
    '{"b": 1, "a": [1, 2.0, "x"], "c": {"z": null, "y": true, "x": false}}',
    '{"s": "caf\\u00e9 \\u2014 \\ud83d\\ude00 \\"quote\\" back\\\\slash \\n\\t\\r\\b\\f \\u0001 \\u007f /"}',
    '{"n": [0, -0, 10, 1e3, 1E3, 1.50, -0.0, 123456789012345678901234567890]}',
    '{"\\u00e9": 1, "e": 2, "z": 3, "A": 4, "\\ud83d\\ude00": 5}',
    '{"nested": [{"b": [], "a": {}}, [[]], ""]}',
]
out["canonical_json"] = [
    {"input": t, "python": json.dumps(json.loads(t), sort_keys=True)} for t in json_texts
]

# ---- Keys, addresses, signatures ----
keys = []
for seed in ["alice", "bob", "carol", "miner", "faucet"]:
    priv = priv_for("entropy-vector-" + seed)
    pub = pub_for(priv)
    unc = pub_uncompressed(priv)
    keys.append({
        "seed": seed,
        "private_key": priv.hex(),
        "public_key": pub.hex(),
        "address": pubkey_to_address(pub),
        "public_key_uncompressed": unc.hex(),
        "address_uncompressed": pubkey_to_address(unc),
    })
out["keys"] = keys

sigs = []
for i, k in enumerate(keys):
    for msg in [b"", b"hello entropy", ("message-%d" % i).encode() * 7]:
        priv = bytes.fromhex(k["private_key"])
        s = sign(priv, msg)
        assert verify(bytes.fromhex(k["public_key"]), s, msg)
        sigs.append({"private_key": k["private_key"], "public_key": k["public_key"],
                     "message": msg.hex(), "signature": s.hex()})
out["signatures"] = sigs

out["invalid_addresses"] = ["", "E", "1BoatSLRHtKNngkdXEeobR76b53LETtpyT",
                            keys[0]["address"][:-1] + ("a" if keys[0]["address"][-1] != "a" else "b"),
                            "0OIl" + keys[0]["address"][4:]]

# ---- Transactions ----
A, B = keys[0], keys[1]


def fix(tx: Transaction, ts, nonce: str) -> Transaction:
    tx.timestamp = ts
    tx.nonce = nonce
    return tx


def signed(tx: Transaction, k) -> Transaction:
    tx.sign_with(bytes.fromhex(k["private_key"]), bytes.fromhex(k["public_key"]))
    return tx


txs = [
    signed(fix(make_transfer(A["address"], B["address"], 150_000_000, fee=10), 1759450000.123456, "00112233aabbccdd"), A),
    signed(fix(make_transfer(A["address"], B["address"], 1, token="CHAOS", fee=1), 1759450001, "int-timestamp"), A),
    signed(fix(make_token_create(A["address"], "chaos", "Chaos Token — ünïcødé 🎲", 10**15, 8), 1759450002.5, "n2"), A),
    signed(fix(make_nft_mint(A["address"], "cats", "001", B["address"], "ipfs://x",
                             traits={"fluff": 9.75, "tags": ["a", "b"], "nested": {"z": 1, "a": None}, "big": 2**70}),
               1759450003.0, "n3"), A),
    signed(fix(make_lottery_create(B["address"], "lot-1", 500, 10, 99), 1e-5, "tiny-ts"), B),
    signed(fix(make_nft_random_mint(B["address"], "cats", "002", A["address"], 5), 1759450004.999999, "n5"), B),
    fix(make_coinbase(A["address"], 50 * 10**8 + 12, 7), 1759450005.25, "cb"),
]
out["transactions"] = [{
    "json": json.dumps(tx.to_dict()),
    "signing_payload": tx.signing_payload().decode(),
    "hash": tx.hash(),
    "signature_valid": tx.verify_signature(),
} for tx in txs]

# ---- Merkle ----
hashes = [tx.hash() for tx in txs]
out["merkle"] = [{"hashes": hashes[:n], "root": merkle_root(hashes[:n])} for n in range(0, len(hashes) + 1)]

# ---- Genesis ----
with tempfile.TemporaryDirectory() as td:
    import contextlib, io
    with contextlib.redirect_stdout(io.StringIO()):
        chain = EntropyChain(data_dir=td)
    g = chain.blocks[0]
    out["genesis"] = {
        "json": json.dumps(g.to_dict()),
        "header": g.header_bytes().hex(),
        "hash": g.hash(),
        "tx_hash": g.transactions[0].hash(),
    }

# ---- PoW (production Argon2 parameters) ----
pow_cases = []
for header_hex, nonce in [(g.header_bytes().hex(), 0), (g.header_bytes().hex(), 2**64 - 1),
                          ("00" * 10, 12345), ("abcd" * 64, 987654321)]:
    h = consensus.pow_hash(bytes.fromhex(header_hex), nonce)
    pow_cases.append({"header": header_hex, "nonce": str(nonce), "hash": h.hex(),
                      "meets_1000": consensus.hash_meets_difficulty(h, 1000),
                      "meets_2": consensus.hash_meets_difficulty(h, 2)})
out["pow"] = pow_cases

# ---- A full block (difficulty 1, so any nonce is valid PoW) ----
prev = g.hash()
miner_nonce = bytes(range(16))
cb = fix(make_coinbase(A["address"], block_reward_at_height(1) + sum(t.fee for t in txs[:3]), 1), 1759450010.5, "cb1")
blk = Block(
    height=1, prev_hash=prev,
    merkle_root_hex=merkle_root([t.hash() for t in [cb] + txs[:3]]),
    timestamp=1759450011.75, difficulty=1,
    entropy_seed=consensus.derive_entropy_seed(bytes.fromhex(prev), miner_nonce).hex(),
    miner_nonce=miner_nonce.hex(), nonce=2**63 + 5, transactions=[cb] + txs[:3],
)
out["block"] = {"json": json.dumps(blk.to_dict()), "header": blk.header_bytes().hex(), "hash": blk.hash()}

# ---- Difficulty, reward, randomness ----
out["adjust_difficulty"] = [
    {"prev": p, "actual": a, "result": consensus.adjust_difficulty(p, a)}
    for p, a in [(1000, 600), (1000, 300), (1000, 1), (1000, 10**9), (1000, 0), (1000, -50),
                 (7, 601), (1, 10**6), (2**60, 1), (123456789, 777)]
]
out["block_reward"] = [{"height": h, "reward": block_reward_at_height(h)}
                       for h in [0, 1, 1_336_999, 1_337_000, 2_674_000, 1_337_000 * 63, 1_337_000 * 64]]
seed = sha256d(b"seed").hex()
entrants = [k["address"] for k in keys]
out["pick_winner"] = [{"seed": seed, "lottery_id": lid, "entrants": entrants,
                       "winner": pick_winner_from_seed(seed, lid, entrants)}
                      for lid in ["lot-1", "lot-2", "üñí"]]
out["random_traits"] = [{"seed": seed, "collection_id": c, "token_id": t,
                         "traits": derive_random_traits(seed, c, t)}
                        for c, t in [("cats", "001"), ("cats", "002"), ("dogs", "x")]]

# ---- Encrypted wallet file ----
with tempfile.TemporaryDirectory() as td:
    p = os.path.join(td, "wallet.json")
    w = Wallet(path=p)
    w._keys = {A["address"]: {"private_key": A["private_key"], "public_key": A["public_key"], "label": "alice"}}
    w._iterations = 1000  # keep the Go test fast
    w.encrypt("correct horse")
    with open(p) as f:
        out["encrypted_wallet"] = {"file": f.read(), "passphrase": "correct horse",
                                   "address": A["address"], "private_key": A["private_key"]}

with open(os.path.join(HERE, "vectors.json"), "w", encoding="utf-8", newline="\n") as f:
    json.dump(out, f, indent=2, ensure_ascii=False)
print("wrote", os.path.join(HERE, "vectors.json"))
