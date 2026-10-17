"""
Miner for Entropy Chain. Assembles candidate blocks from mempool and
hammers nonces until one meets difficulty.
"""

import secrets
import time
from block import Block, merkle_root
from blockchain import EntropyChain, block_reward_at_height
from consensus import pow_hash, hash_meets_difficulty, derive_entropy_seed
from transactions import make_coinbase


class Miner:
    def __init__(self, chain: EntropyChain, miner_address: str,
                 max_txs_per_block: int = 100):
        self.chain = chain
        self.address = miner_address
        self.max_txs = max_txs_per_block

    def assemble_candidate(self) -> Block:
        # Atomic snapshot of all the state the miner needs. This avoids
        # races where the chain mutates while we're partway through reading.
        prev, height, difficulty, valid_txs = self.chain.snapshot_for_mining(
            max_txs=self.max_txs
        )
        prev_hash_bytes = bytes.fromhex(prev.hash())

        # Per-block randomness contribution. The miner picks miner_nonce
        # freely; the entropy_seed is derived from prev_hash + miner_nonce
        # so it's verifiable.
        miner_nonce = secrets.token_bytes(16)
        entropy_seed = derive_entropy_seed(prev_hash_bytes, miner_nonce)

        total_fees = sum(tx.fee for tx in valid_txs)
        coinbase = make_coinbase(
            miner=self.address,
            reward=block_reward_at_height(height) + total_fees,
            height=height,
        )
        all_txs = [coinbase] + valid_txs

        block = Block(
            height=height,
            prev_hash=prev.hash(),
            merkle_root_hex=merkle_root([tx.hash() for tx in all_txs]),
            timestamp=time.time(),
            difficulty=difficulty,
            entropy_seed=entropy_seed.hex(),
            miner_nonce=miner_nonce.hex(),
            nonce=0,
            transactions=all_txs,
        )
        return block

    def mine(self, max_attempts: int = 1_000_000, log_every: int = 50) -> Block | None:
        """
        Single-threaded CPU mining loop. Returns the mined block on
        success, or None if max_attempts exhausted.
        """
        candidate = self.assemble_candidate()
        header = candidate.header_bytes()
        diff = candidate.difficulty
        start = time.time()

        for attempt in range(max_attempts):
            nonce = secrets.randbits(64)
            h = pow_hash(header, nonce)
            if hash_meets_difficulty(h, diff):
                candidate.nonce = nonce
                elapsed = time.time() - start
                rate = (attempt + 1) / max(elapsed, 0.001)
                print(f"⛏  Mined block {candidate.height} in {elapsed:.1f}s "
                      f"({attempt + 1} attempts, {rate:.1f} H/s, "
                      f"difficulty={diff})")
                return candidate
            if (attempt + 1) % log_every == 0:
                elapsed = time.time() - start
                rate = (attempt + 1) / max(elapsed, 0.001)
                print(f"   ... {attempt + 1} attempts, {rate:.1f} H/s, elapsed {elapsed:.1f}s")
        return None
