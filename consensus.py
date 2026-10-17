"""
Proof-of-Entropy consensus for Entropy Chain.

Why this is different:
  Traditional PoW (Bitcoin SHA-256, Eth's old Ethash) eventually loses
  to specialized hardware. Even Ethash got FPGA'd. Monero's RandomX
  fights back by being memory-hard AND requiring a JIT-compiled VM
  per nonce. We take a simpler but still effective approach: Argon2id.

  Argon2id is the winner of the Password Hashing Competition (2015) and
  is *designed* to be memory-hard and resistant to GPU/ASIC attacks.
  GPUs hate it because each thread needs its own MB of working memory,
  so you can't run thousands of them in parallel.

The "Entropy" twist:
  Each block must include an `entropy_seed` field - 32 bytes the miner
  generates by hashing recent block hashes with their own randomness.
  Other nodes verify the seed was constructed correctly. This means
  every block contributes verifiable randomness to a public beacon
  that smart contracts (or in our case, native lottery/NFT-mint
  transactions) can consume.

  So the chain produces a useful side product: randomness-as-a-service.
"""

import struct
from argon2.low_level import hash_secret_raw, Type
from crypto_utils import sha256d


# Argon2id parameters - tuned for ~50-200ms per attempt on a modern CPU.
# Memory cost dominates; raise this and GPU/ASIC mining gets even harder.
ARGON2_TIME_COST = 1
ARGON2_MEMORY_KB = 16 * 1024  # 16 MB per attempt
ARGON2_PARALLELISM = 1
ARGON2_HASH_LEN = 32

# Block target time in seconds. Difficulty retargets to hit this average.
TARGET_BLOCK_TIME = 30

# How many blocks between difficulty adjustments.
DIFFICULTY_WINDOW = 20

# Genesis difficulty - low so the first blocks mine fast for testing.
GENESIS_DIFFICULTY = 1000


def pow_hash(header_bytes: bytes, nonce: int) -> bytes:
    """
    Compute the Proof-of-Entropy hash for a given header + nonce.

    We use the header as the Argon2 'password' and a fixed salt derived
    from the header. Argon2id requires a salt >= 8 bytes; we use the
    SHA-256 of the header so it's deterministic and verifiable.
    """
    nonce_bytes = struct.pack("<Q", nonce)
    payload = header_bytes + nonce_bytes
    salt = sha256d(header_bytes)[:16]  # 16-byte deterministic salt
    return hash_secret_raw(
        secret=payload,
        salt=salt,
        time_cost=ARGON2_TIME_COST,
        memory_cost=ARGON2_MEMORY_KB,
        parallelism=ARGON2_PARALLELISM,
        hash_len=ARGON2_HASH_LEN,
        type=Type.ID,
    )


def hash_meets_difficulty(h: bytes, difficulty: int) -> bool:
    """
    A hash 'meets' difficulty D if its big-endian integer value is
    less than (2^256 / D). Higher D = harder.
    """
    target = (1 << 256) // max(difficulty, 1)
    h_int = int.from_bytes(h, "big")
    return h_int < target


def adjust_difficulty(prev_difficulty: int, actual_window_seconds: int) -> int:
    """
    Adjust difficulty so the next window hits TARGET_BLOCK_TIME.

    Bounded to 4x change per window to prevent oscillation attacks.
    """
    expected = TARGET_BLOCK_TIME * DIFFICULTY_WINDOW
    actual = max(actual_window_seconds, 1)

    # New difficulty = old * (expected / actual)
    new = prev_difficulty * expected // actual

    # Clamp to [old/4, old*4]
    new = max(new, prev_difficulty // 4)
    new = min(new, prev_difficulty * 4)
    return max(new, 1)


def derive_entropy_seed(prev_hash: bytes, miner_nonce: bytes) -> bytes:
    """
    Construct the entropy contribution for a block. Anyone can verify
    this matches the block's claimed seed, so miners can't lie about it.

    The seed becomes part of the public randomness beacon. Native
    transactions (lotteries, NFT mints with random traits, etc.) can
    consume past entropy seeds knowing they're verifiable.
    """
    return sha256d(prev_hash + miner_nonce)
