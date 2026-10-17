"""
The Entropy Chain itself: state machine + storage + reorg support.

Key data structures:
  - block_index[hash] = Block        every block we've ever seen, indexed by hash
  - main_chain = [hash_at_height_0, hash_at_height_1, ...]   the active chain
  - cumulative_work[hash] = int      total PoW work behind that block (tip selection)
  - state                            balances/tokens/nfts/lotteries derived from main_chain

Fork resolution:
  When we receive a block whose parent isn't our current tip, we add it to
  block_index anyway. If its cumulative work exceeds our current tip's work,
  we trigger a reorg: rewind state to the common ancestor, then replay the
  competing branch forward. State is fully rebuilt from main_chain replay.
"""

import json
import os
import threading
import time
from collections import defaultdict
from typing import Optional

from block import Block, merkle_root
from transactions import (
    Transaction, NATIVE_TOKEN,
    TX_TRANSFER, TX_TOKEN_CREATE, TX_TOKEN_MINT,
    TX_NFT_CREATE, TX_NFT_MINT, TX_NFT_TRANSFER, TX_COINBASE,
    TX_LOTTERY_CREATE, TX_LOTTERY_ENTER, TX_LOTTERY_DRAW,
    TX_NFT_RANDOM_MINT,
    make_coinbase,
)
from consensus import (
    pow_hash, hash_meets_difficulty, adjust_difficulty,
    derive_entropy_seed, GENESIS_DIFFICULTY,
    DIFFICULTY_WINDOW, TARGET_BLOCK_TIME,
)
from crypto_utils import address_is_valid
from genesis_config import (
    GENESIS_MESSAGE, GENESIS_TIMESTAMP, GENESIS_NETWORK_TAG, GENESIS_OPERATOR,
)


INITIAL_REWARD = 50 * 10**8
HALVING_INTERVAL = 1_337_000


def block_reward_at_height(height: int) -> int:
    halvings = height // HALVING_INTERVAL
    if halvings >= 64:
        return 0
    return INITIAL_REWARD >> halvings


def pick_winner_from_seed(entropy_seed_hex: str, lottery_id: str,
                          entrants: list[str]) -> str:
    import hashlib
    h = hashlib.sha256(bytes.fromhex(entropy_seed_hex) + lottery_id.encode()).digest()
    idx = int.from_bytes(h, "big") % len(entrants)
    return entrants[idx]


def derive_random_traits(entropy_seed_hex: str, collection_id: str,
                         token_id: str) -> dict:
    import hashlib
    h = hashlib.sha256(
        bytes.fromhex(entropy_seed_hex) + collection_id.encode() + token_id.encode()
    ).digest()
    return {
        "rarity": h[0],
        "power": h[1],
        "element": ["fire", "water", "earth", "air", "void"][h[2] % 5],
        "color_seed": h[3:6].hex(),
        "is_legendary": h[6] < 8,
        "entropy_source_height": None,
    }


class EntropyChain:
    def __init__(self, data_dir: str = "./chaindata"):
        self.data_dir = data_dir
        os.makedirs(data_dir, exist_ok=True)

        # Re-entrant lock protecting ALL chain state mutations and reads
        # that need a consistent snapshot. RLock (not regular Lock) because
        # public methods sometimes call other public methods on the same
        # thread — a regular Lock would deadlock there. See the comment on
        # add_block for the "do work under lock, fire callbacks after" pattern
        # we use to keep network I/O out of the critical section.
        self._lock = threading.RLock()

        # Callbacks queued under the lock, fired AFTER the lock releases.
        # Items are (callback_callable, args_tuple). Drained by
        # _drain_pending_callbacks at the end of any locked operation.
        self._pending_callbacks: list = []

        self.block_index: dict[str, Block] = {}
        self.main_chain: list[str] = []
        self.cumulative_work: dict[str, int] = {}

        self.balances: dict[str, dict[str, int]] = defaultdict(lambda: defaultdict(int))
        self.tokens: dict[str, dict] = {}
        self.nft_collections: dict[str, dict] = {}
        self.nfts: dict[tuple, dict] = {}
        self.lotteries: dict[str, dict] = {}
        self.tx_seen: set[str] = set()
        # Transaction history index — see _rebuild_state for invariants
        self.tx_index: dict[str, list[tuple[int, str]]] = defaultdict(list)
        self.tx_by_hash: dict[str, tuple[int, "Transaction"]] = {}

        self.mempool: list[Transaction] = []

        # Subscribed by the network layer
        self.on_new_tip: list = []
        self.on_reorg: list = []

        self._load_or_genesis()

    # ----------------- Genesis & persistence -----------------

    def _load_or_genesis(self) -> None:
        path = os.path.join(self.data_dir, "chain.json")
        tmp_path = path + ".tmp"

        # Clean up any leftover tmp from an interrupted previous write.
        # The fact that .tmp exists means _save was killed before the
        # atomic rename happened; the real chain.json (if any) is still
        # the authoritative one to load.
        if os.path.exists(tmp_path):
            try:
                os.remove(tmp_path)
                print(f"Removed stale {tmp_path} from interrupted write")
            except OSError:
                pass

        if os.path.exists(path):
            try:
                with open(path) as f:
                    data = json.load(f)
            except json.JSONDecodeError as e:
                # chain.json is corrupted. This is recoverable: rename the
                # corrupt file out of the way and create fresh genesis.
                # The node will then resync from peers. We back up the corrupt
                # file rather than delete it in case manual recovery is wanted.
                from datetime import datetime
                backup = path + ".corrupt." + datetime.now().strftime("%Y%m%d-%H%M%S")
                try:
                    os.rename(path, backup)
                    print(f"chain.json is corrupted ({e}); backed up to {backup}")
                    print("Creating fresh genesis. Node will resync from peers.")
                except OSError as ren_err:
                    print(f"chain.json corrupt and could not be moved: {ren_err}")
                    raise
                self._create_genesis()
                return

            for b in data["blocks"]:
                block = Block.from_dict(b)
                self._index_block(block)
            self.main_chain = data["main_chain"]
            self._rebuild_state()
            print(f"Loaded {len(self.main_chain)} blocks from disk "
                  f"({len(self.block_index)} total in index).")
        else:
            self._create_genesis()

    def _create_genesis(self) -> None:
        # Embed the launch identity as a "coinbase" tx with 0 amount.
        # Its tx_hash becomes the merkle root, which becomes part of the
        # genesis block hash, which is the canonical chain identifier.
        # Anyone running this code with different genesis_config values
        # will produce a different genesis hash and not peer with us.
        genesis_tx = Transaction(
            tx_type=TX_COINBASE,
            sender="",
            timestamp=GENESIS_TIMESTAMP,
            nonce="genesis",
            data={
                "recipient": "",  # no one - this is a memorial coinbase
                "amount": 0,
                "height": 0,
                "message": GENESIS_MESSAGE,
                "network_tag": GENESIS_NETWORK_TAG,
                "operator": GENESIS_OPERATOR,
            },
        )
        tx_hashes = [genesis_tx.hash()]

        genesis = Block(
            height=0,
            prev_hash="00" * 32,
            merkle_root_hex=merkle_root(tx_hashes),
            timestamp=GENESIS_TIMESTAMP,
            difficulty=GENESIS_DIFFICULTY,
            entropy_seed="00" * 32,
            miner_nonce="00" * 16,
            nonce=0,
            transactions=[genesis_tx],
        )
        self._index_block(genesis)
        self.main_chain = [genesis.hash()]
        self._save()
        print(f"Created genesis block: {genesis.hash()[:16]}…")
        print(f"  Network: {GENESIS_NETWORK_TAG}")
        print(f"  Message: {GENESIS_MESSAGE}")

    def _drain_pending_callbacks(self) -> None:
        """
        Fire and clear queued callbacks. MUST be called only after releasing
        self._lock — callbacks may do network I/O, and we never want network
        I/O while holding the chain lock.

        Callbacks that raise are caught and swallowed: a broken peer-broadcast
        callback must not corrupt or stall the chain.
        """
        # Snapshot and clear under lock so we don't race with new callbacks
        # being queued by a concurrent operation.
        with self._lock:
            pending = self._pending_callbacks
            self._pending_callbacks = []
        for cb, args in pending:
            try:
                cb(*args)
            except Exception:
                pass

    def _index_block(self, block: Block) -> None:
        h = block.hash()
        self.block_index[h] = block
        if block.height == 0:
            self.cumulative_work[h] = block.difficulty
        else:
            parent_work = self.cumulative_work.get(block.prev_hash, 0)
            self.cumulative_work[h] = parent_work + block.difficulty

    def _save(self) -> None:
        """
        Atomically persist chain state to disk.

        Writes to chain.json.tmp first, fsync()s the file (so the data is on
        disk, not just in the kernel buffer), then atomically renames over
        chain.json. If the process is killed mid-write, chain.json is either
        unchanged or fully replaced — never partially written.

        This closes the failure mode where a power loss / OOM kill / sigkill
        mid-_save would corrupt chain.json and brick the node on next start.
        """
        path = os.path.join(self.data_dir, "chain.json")
        tmp_path = path + ".tmp"
        payload = {
            "blocks": [b.to_dict() for b in self.block_index.values()],
            "main_chain": self.main_chain,
        }
        # Write fully to a temp file
        with open(tmp_path, "w") as f:
            json.dump(payload, f, indent=2)
            f.flush()
            os.fsync(f.fileno())
        # Atomic rename — POSIX guarantees this either succeeds completely
        # or leaves the target untouched. Windows os.replace has the same
        # behavior on a single filesystem, which is what we have here.
        os.replace(tmp_path, path)

    # ----------------- Read helpers -----------------

    @property
    def height(self) -> int:
        return len(self.main_chain) - 1

    @property
    def tip_hash(self) -> str:
        return self.main_chain[-1]

    @property
    def tip(self) -> Block:
        return self.block_index[self.tip_hash]

    @property
    def blocks(self) -> list[Block]:
        return [self.block_index[h] for h in self.main_chain]

    def get_balance(self, address: str, token: str = NATIVE_TOKEN) -> int:
        with self._lock:
            return self.balances[address][token]

    def get_history(self, address: str, limit: int = 50,
                    before_height: int | None = None) -> list[dict]:
        """
        Return up to `limit` recent txs involving this address, newest first.

        If `before_height` is given, only returns txs from blocks strictly
        before that height (cursor-based pagination).

        Each entry in the returned list is a dict with the tx fields (matching
        Transaction.to_dict) plus a "height" field for the block it was in.
        """
        with self._lock:
            entries = self.tx_index.get(address, [])
            if before_height is not None:
                # entries are in chain order, so cursor by linear filter is fine
                # because the index list is bounded by the address's own activity
                entries = [(h, th) for (h, th) in entries if h < before_height]
            # Newest first
            entries = list(reversed(entries))
            if limit > 0:
                entries = entries[:limit]
            out = []
            for height, tx_h in entries:
                stored = self.tx_by_hash.get(tx_h)
                if not stored:
                    continue
                stored_height, stored_tx = stored
                d = stored_tx.to_dict()
                d["height"] = stored_height
                out.append(d)
            return out

    # ----------------- State rebuild -----------------

    def _rebuild_state(self) -> None:
        self.balances = defaultdict(lambda: defaultdict(int))
        self.tokens = {}
        self.nft_collections = {}
        self.nfts = {}
        self.lotteries = {}
        self.tx_seen = set()
        # Transaction history index. Keyed by address, each value is a list of
        # (height, tx_hash) tuples in chain order. Maintained by _apply_tx as
        # the chain extends, and rebuilt from scratch here on reorg.
        self.tx_index: dict[str, list[tuple[int, str]]] = defaultdict(list)
        # Hash-indexed lookup for hydrating history entries into full tx data.
        self.tx_by_hash: dict[str, tuple[int, "Transaction"]] = {}
        for h in self.main_chain:
            block = self.block_index[h]
            for tx in block.transactions:
                self._apply_tx(tx, block.height)

    # ----------------- Validation -----------------

    def validate_transaction(self, tx: Transaction, applying: bool = False) -> tuple[bool, str]:
        if tx.hash() in self.tx_seen and not applying:
            return False, "duplicate transaction"

        if tx.tx_type != TX_COINBASE:
            if not address_is_valid(tx.sender):
                return False, "invalid sender address"
            if not tx.verify_signature():
                return False, "invalid signature"
            if self.get_balance(tx.sender, NATIVE_TOKEN) < tx.fee:
                return False, "insufficient balance for fee"

        d = tx.data

        if tx.tx_type == TX_TRANSFER:
            token = d.get("token", NATIVE_TOKEN)
            amount = d.get("amount", 0)
            recipient = d.get("recipient", "")
            if not address_is_valid(recipient):
                return False, "invalid recipient"
            if amount <= 0:
                return False, "amount must be positive"
            available = self.get_balance(tx.sender, token)
            if token == NATIVE_TOKEN:
                if available < amount + tx.fee:
                    return False, "insufficient balance"
            else:
                if available < amount:
                    return False, "insufficient token balance"

        elif tx.tx_type == TX_TOKEN_CREATE:
            symbol = d.get("symbol", "").upper()
            if not symbol or symbol == NATIVE_TOKEN:
                return False, "invalid symbol"
            if symbol in self.tokens:
                return False, "token already exists"
            if d.get("total_supply", 0) <= 0:
                return False, "total_supply must be positive"

        elif tx.tx_type == TX_TOKEN_MINT:
            symbol = d.get("symbol", "").upper()
            if symbol not in self.tokens:
                return False, "token does not exist"
            if self.tokens[symbol]["creator"] != tx.sender:
                return False, "only creator can mint"
            if d.get("amount", 0) <= 0:
                return False, "amount must be positive"

        elif tx.tx_type == TX_NFT_CREATE:
            cid = d.get("collection_id", "")
            if not cid or cid in self.nft_collections:
                return False, "collection_id missing or already exists"

        elif tx.tx_type == TX_NFT_MINT:
            cid = d.get("collection_id", "")
            tid = d.get("token_id", "")
            if cid not in self.nft_collections:
                return False, "collection does not exist"
            if self.nft_collections[cid]["creator"] != tx.sender:
                return False, "only collection creator can mint"
            if (cid, tid) in self.nfts:
                return False, "NFT token_id already minted"
            if not address_is_valid(d.get("recipient", "")):
                return False, "invalid recipient"

        elif tx.tx_type == TX_NFT_TRANSFER:
            cid = d.get("collection_id", "")
            tid = d.get("token_id", "")
            if (cid, tid) not in self.nfts:
                return False, "NFT does not exist"
            if self.nfts[(cid, tid)]["owner"] != tx.sender:
                return False, "sender does not own this NFT"
            if not address_is_valid(d.get("recipient", "")):
                return False, "invalid recipient"

        elif tx.tx_type == TX_LOTTERY_CREATE:
            lid = d.get("lottery_id", "")
            if not lid or lid in self.lotteries:
                return False, "lottery_id missing or already exists"
            if d.get("prize_amount", 0) <= 0:
                return False, "prize_amount must be positive"
            if d.get("ticket_price", 0) < 0:
                return False, "ticket_price cannot be negative"
            if d.get("draw_height", 0) < self.height + 2:
                return False, "draw_height must be at least 2 blocks in future"
            token = d.get("token", NATIVE_TOKEN)
            if token == NATIVE_TOKEN:
                if self.get_balance(tx.sender, NATIVE_TOKEN) < d["prize_amount"] + tx.fee:
                    return False, "insufficient balance for prize + fee"
            else:
                if self.get_balance(tx.sender, token) < d["prize_amount"]:
                    return False, "insufficient token balance for prize"

        elif tx.tx_type == TX_LOTTERY_ENTER:
            lid = d.get("lottery_id", "")
            if lid not in self.lotteries:
                return False, "lottery does not exist"
            lot = self.lotteries[lid]
            if lot["drawn"]:
                return False, "lottery already drawn"
            if self.height + 1 >= lot["draw_height"]:
                return False, "lottery entry closed (draw block reached)"
            if tx.sender in lot["entrants"]:
                return False, "address already entered"
            cost = lot["ticket_price"] + tx.fee
            if self.get_balance(tx.sender, NATIVE_TOKEN) < cost:
                return False, "insufficient balance for ticket + fee"

        elif tx.tx_type == TX_LOTTERY_DRAW:
            lid = d.get("lottery_id", "")
            if lid not in self.lotteries:
                return False, "lottery does not exist"
            lot = self.lotteries[lid]
            if lot["drawn"]:
                return False, "lottery already drawn"
            if self.height + 1 < lot["draw_height"]:
                return False, f"draw_height {lot['draw_height']} not yet reached"
            if not lot["entrants"]:
                return False, "no entrants - lottery cannot draw"

        elif tx.tx_type == TX_NFT_RANDOM_MINT:
            cid = d.get("collection_id", "")
            tid = d.get("token_id", "")
            if cid not in self.nft_collections:
                return False, "collection does not exist"
            if self.nft_collections[cid]["creator"] != tx.sender:
                return False, "only collection creator can mint"
            if (cid, tid) in self.nfts:
                return False, "NFT token_id already minted"
            if not address_is_valid(d.get("recipient", "")):
                return False, "invalid recipient"
            eh = d.get("entropy_height", -1)
            if eh < 1 or eh > self.height:
                return False, f"entropy_height {eh} must be a past block"

        elif tx.tx_type == TX_COINBASE:
            if not applying:
                return False, "coinbase not allowed in mempool"

        else:
            return False, f"unknown tx type: {tx.tx_type}"

        return True, "ok"

    # ----------------- State application -----------------

    def _apply_tx(self, tx: Transaction, height: int) -> None:
        d = tx.data

        if tx.tx_type == TX_COINBASE:
            self.balances[d["recipient"]][NATIVE_TOKEN] += d["amount"]
            self.tx_seen.add(tx.hash())
            return

        if tx.fee > 0:
            self.balances[tx.sender][NATIVE_TOKEN] -= tx.fee

        if tx.tx_type == TX_TRANSFER:
            token = d.get("token", NATIVE_TOKEN)
            amt = d["amount"]
            self.balances[tx.sender][token] -= amt
            self.balances[d["recipient"]][token] += amt

        elif tx.tx_type == TX_TOKEN_CREATE:
            sym = d["symbol"].upper()
            self.tokens[sym] = {
                "name": d["name"], "total_supply": d["total_supply"],
                "decimals": d.get("decimals", 8), "creator": tx.sender,
            }
            self.balances[tx.sender][sym] += d["total_supply"]

        elif tx.tx_type == TX_TOKEN_MINT:
            sym = d["symbol"].upper()
            self.tokens[sym]["total_supply"] += d["amount"]
            self.balances[d["recipient"]][sym] += d["amount"]

        elif tx.tx_type == TX_NFT_CREATE:
            self.nft_collections[d["collection_id"]] = {
                "name": d["name"], "description": d.get("description", ""),
                "creator": tx.sender,
            }

        elif tx.tx_type == TX_NFT_MINT:
            self.nfts[(d["collection_id"], d["token_id"])] = {
                "owner": d["recipient"],
                "metadata_uri": d.get("metadata_uri", ""),
                "traits": d.get("traits", {}),
                "minted_at_height": height,
                "creator": tx.sender,
            }

        elif tx.tx_type == TX_NFT_TRANSFER:
            self.nfts[(d["collection_id"], d["token_id"])]["owner"] = d["recipient"]

        elif tx.tx_type == TX_LOTTERY_CREATE:
            self.balances[tx.sender][d["token"]] -= d["prize_amount"]
            self.lotteries[d["lottery_id"]] = {
                "organizer": tx.sender,
                "prize_amount": d["prize_amount"],
                "ticket_price": d["ticket_price"],
                "draw_height": d["draw_height"],
                "token": d["token"],
                "entrants": [], "entry_pool": 0,
                "drawn": False, "winner": None, "entropy_used": None,
                "created_at_height": height,
            }

        elif tx.tx_type == TX_LOTTERY_ENTER:
            lot = self.lotteries[d["lottery_id"]]
            self.balances[tx.sender][NATIVE_TOKEN] -= lot["ticket_price"]
            lot["entry_pool"] += lot["ticket_price"]
            lot["entrants"].append(tx.sender)

        elif tx.tx_type == TX_LOTTERY_DRAW:
            lot = self.lotteries[d["lottery_id"]]
            draw_block = self.blocks[lot["draw_height"]]
            winner = pick_winner_from_seed(
                draw_block.entropy_seed, d["lottery_id"], lot["entrants"]
            )
            payout = lot["prize_amount"] + lot["entry_pool"]
            self.balances[winner][lot["token"]] += payout
            lot["drawn"] = True
            lot["winner"] = winner
            lot["entropy_used"] = draw_block.entropy_seed
            lot["drawn_at_height"] = height

        elif tx.tx_type == TX_NFT_RANDOM_MINT:
            entropy_block = self.blocks[d["entropy_height"]]
            traits = derive_random_traits(
                entropy_block.entropy_seed,
                d["collection_id"],
                d["token_id"],
            )
            traits["entropy_source_height"] = d["entropy_height"]
            self.nfts[(d["collection_id"], d["token_id"])] = {
                "owner": d["recipient"],
                "metadata_uri": d.get("metadata_uri", ""),
                "traits": traits,
                "minted_at_height": height,
                "creator": tx.sender,
                "entropy_source_height": d["entropy_height"],
            }

        self.tx_seen.add(tx.hash())
        self._record_tx_for_history(tx, height)

    def _record_tx_for_history(self, tx: "Transaction", height: int) -> None:
        """
        Add this tx to the history index under every address it involves.

        Called from _apply_tx (so it covers both initial chain replay during
        load AND incremental extension during normal operation). On reorg,
        _rebuild_state wipes the index and replays main_chain, which calls
        _apply_tx for every tx — keeping the index canonical without delicate
        revert hooks.
        """
        tx_h = tx.hash()
        d = tx.data
        addrs = set()

        # The sender is involved in every non-coinbase tx
        if tx.sender:
            addrs.add(tx.sender)

        # Recipients vary by tx type
        if tx.tx_type == TX_COINBASE:
            if d.get("recipient"):
                addrs.add(d["recipient"])
        elif tx.tx_type == TX_TRANSFER:
            if d.get("recipient"):
                addrs.add(d["recipient"])
        elif tx.tx_type == TX_TOKEN_MINT:
            if d.get("recipient"):
                addrs.add(d["recipient"])
        elif tx.tx_type in (TX_NFT_MINT, TX_NFT_RANDOM_MINT, TX_NFT_TRANSFER):
            if d.get("recipient"):
                addrs.add(d["recipient"])
        elif tx.tx_type == TX_LOTTERY_DRAW:
            # The winner is interested in this tx even though they didn't send it
            lot = self.lotteries.get(d.get("lottery_id"))
            if lot and lot.get("winner"):
                addrs.add(lot["winner"])

        # Stash the tx for hash lookup
        self.tx_by_hash[tx_h] = (height, tx)

        # Append to each address's history list
        for addr in addrs:
            self.tx_index[addr].append((height, tx_h))

    # ----------------- Block validation -----------------

    def validate_block_standalone(self, block: Block) -> tuple[bool, str]:
        h = pow_hash(block.header_bytes(), block.nonce)
        if not hash_meets_difficulty(h, block.difficulty):
            return False, "PoW does not meet difficulty"
        tx_hashes = [tx.hash() for tx in block.transactions]
        if block.merkle_root_hex != merkle_root(tx_hashes):
            return False, "merkle root mismatch"
        if not block.transactions or block.transactions[0].tx_type != TX_COINBASE:
            return False, "first tx must be coinbase"
        for tx in block.transactions[1:]:
            if tx.tx_type == TX_COINBASE:
                return False, "multiple coinbase txs"
        return True, "ok"

    def validate_block_contextual(self, block: Block, parent_hash: str,
                                  expected_height: int, expected_difficulty: int) -> tuple[bool, str]:
        if block.height != expected_height:
            return False, f"expected height {expected_height}, got {block.height}"
        if block.prev_hash != parent_hash:
            return False, "prev_hash mismatch"
        if block.difficulty != expected_difficulty:
            return False, f"wrong difficulty (expected {expected_difficulty})"

        expected_seed = derive_entropy_seed(
            bytes.fromhex(parent_hash),
            bytes.fromhex(block.miner_nonce),
        ).hex()
        if block.entropy_seed != expected_seed:
            return False, "entropy_seed not derived correctly"

        coinbase = block.transactions[0]
        expected_reward = block_reward_at_height(block.height)
        total_fees = sum(tx.fee for tx in block.transactions[1:])
        if coinbase.data.get("amount", 0) != expected_reward + total_fees:
            return False, "coinbase amount mismatch"

        for tx in block.transactions[1:]:
            ok, reason = self.validate_transaction(tx, applying=True)
            if not ok:
                return False, f"tx invalid: {reason}"
        return True, "ok"

    def _difficulty_for_height(self, parent_hash: str, new_height: int) -> int:
        parent = self.block_index[parent_hash]
        # The first window (ending at height DIFFICULTY_WINDOW) would measure
        # from the genesis timestamp, which is fixed in advance rather than
        # mined, so it does not retarget. Retargeting starts at 2*WINDOW.
        if new_height <= DIFFICULTY_WINDOW or new_height % DIFFICULTY_WINDOW != 0:
            return parent.difficulty
        cursor = parent
        for _ in range(DIFFICULTY_WINDOW - 1):
            cursor = self.block_index[cursor.prev_hash]
        actual = parent.timestamp - cursor.timestamp
        return adjust_difficulty(parent.difficulty, int(actual))

    def _expected_difficulty_for_next(self) -> int:
        return self._difficulty_for_height(self.tip_hash, self.height + 1)

    # Compatibility shim for the miner
    def _expected_difficulty(self) -> int:
        return self._expected_difficulty_for_next()

    # ----------------- The main entry point: add_block -----------------

    def add_block(self, block: Block) -> tuple[bool, str]:
        """
        Thread-safe block acceptance. All state mutation happens under the
        chain lock. Callbacks (which may do network I/O like broadcasting to
        peers) are queued during the locked section and fired AFTER we
        release the lock — otherwise a slow broadcast would block every
        other thread that needs chain access.
        """
        with self._lock:
            h = block.hash()
            if h in self.block_index:
                return False, "already known"

            ok, reason = self.validate_block_standalone(block)
            if not ok:
                return False, reason

            if block.prev_hash not in self.block_index:
                return False, "unknown parent (request from peer)"

            parent_work = self.cumulative_work[block.prev_hash]
            new_work = parent_work + block.difficulty

            if block.prev_hash == self.tip_hash:
                expected_diff = self._expected_difficulty_for_next()
                ok, reason = self.validate_block_contextual(
                    block, self.tip_hash, self.height + 1, expected_diff
                )
                if not ok:
                    return False, reason
                self._index_block(block)
                self.main_chain.append(h)
                for tx in block.transactions:
                    self._apply_tx(tx, block.height)
                included = {tx.hash() for tx in block.transactions}
                self.mempool = [tx for tx in self.mempool if tx.hash() not in included]
                self._save()
                # Queue callbacks for after-lock drain instead of firing here
                for cb in self.on_new_tip:
                    self._pending_callbacks.append((cb, (block,)))
                result = (True, "extended tip")
            else:
                # Block extends a non-tip branch
                self._index_block(block)
                if new_work > self.cumulative_work[self.tip_hash]:
                    # _reorg_to also queues its callbacks rather than firing
                    result = self._reorg_to(h)
                else:
                    self._save()
                    result = (True, "stored on side branch")

        # Lock released; safe to do network I/O via callbacks now
        self._drain_pending_callbacks()
        return result

    def _reorg_to(self, new_tip_hash: str) -> tuple[bool, str]:
        """Switch active chain to the branch ending at new_tip_hash."""
        new_branch = self._chain_back_to_root(new_tip_hash)
        old_branch_set = set(self.main_chain)

        common_ancestor = None
        common_idx_in_new = None
        for i, h in enumerate(new_branch):
            if h in old_branch_set:
                common_ancestor = h
                common_idx_in_new = i
                break

        if common_ancestor is None:
            return False, "no common ancestor (corrupted state?)"

        new_branch_forward = list(reversed(new_branch[:common_idx_in_new]))

        saved_main_chain = list(self.main_chain)
        saved_mempool = list(self.mempool)

        ancestor_idx = self.main_chain.index(common_ancestor)
        old_tip_hash = self.tip_hash
        depth = (len(self.main_chain) - 1) - ancestor_idx

        # Rewind to common ancestor
        self.main_chain = self.main_chain[:ancestor_idx + 1]
        self._rebuild_state()

        # Apply new branch
        parent_hash = common_ancestor
        applied = []
        for blk_hash in new_branch_forward:
            block = self.block_index[blk_hash]
            expected_height = self.height + 1
            expected_diff = self._difficulty_for_height(parent_hash, expected_height)
            ok, reason = self.validate_block_contextual(
                block, parent_hash, expected_height, expected_diff
            )
            if not ok:
                # Abort and restore
                self.main_chain = saved_main_chain
                self.mempool = saved_mempool
                self._rebuild_state()
                return False, f"reorg aborted at {blk_hash[:12]}: {reason}"
            self.main_chain.append(blk_hash)
            for tx in block.transactions:
                self._apply_tx(tx, block.height)
            applied.append(block)
            parent_hash = blk_hash

        included = set()
        for block in applied:
            for tx in block.transactions:
                included.add(tx.hash())
        self.mempool = [tx for tx in self.mempool if tx.hash() not in included]
        self._save()
        # Queue callbacks for the caller (add_block) to drain after lock release
        for cb in self.on_reorg:
            self._pending_callbacks.append((cb, (old_tip_hash, new_tip_hash, depth)))
        for cb in self.on_new_tip:
            self._pending_callbacks.append((cb, (self.tip,)))
        return True, f"reorg succeeded (depth {depth} -> {len(applied)})"

    def _chain_back_to_root(self, h: str) -> list[str]:
        chain = []
        cursor = h
        while True:
            chain.append(cursor)
            block = self.block_index[cursor]
            if block.height == 0:
                break
            cursor = block.prev_hash
        return chain

    # ----------------- Mempool -----------------

    # Maximum pending txs the mempool will hold. New low-fee txs get rejected
    # when full; new higher-fee txs evict the lowest-fee tx in the pool.
    MEMPOOL_MAX_SIZE = 10_000

    def submit_transaction(self, tx: Transaction) -> tuple[bool, str]:
        with self._lock:
            ok, reason = self.validate_transaction(tx, applying=False)
            if not ok:
                return False, reason

            # Reject if the same tx hash is already pending in the mempool.
            # validate_transaction already checks tx_seen (mined hashes), but
            # the mempool itself was not deduplicated, which let the same
            # tx end up in a block twice.
            tx_h = tx.hash()
            for pending in self.mempool:
                if pending.hash() == tx_h:
                    return False, "transaction already in mempool"

            if len(self.mempool) >= self.MEMPOOL_MAX_SIZE:
                # Find the lowest-fee tx already in the pool
                lowest = min(self.mempool, key=lambda t: t.fee)
                if tx.fee <= lowest.fee:
                    return False, "mempool full and your fee is too low"
                # Evict the lowest-fee tx to make room
                self.mempool.remove(lowest)

            self.mempool.append(tx)
            return True, "queued"

    # ----------------- Network helpers -----------------

    def snapshot_for_mining(self, max_txs: int = 100):
        """
        Atomic snapshot of the chain state the miner needs to assemble a
        candidate block: current tip, height, expected difficulty, and a
        filtered list of valid mempool txs.

        Returns a tuple (tip_block, next_height, expected_difficulty, valid_txs).
        Holding the lock for this is fast (just dict/list reads + validation
        of <=100 mempool txs, no PoW work).
        """
        with self._lock:
            tip = self.tip
            next_height = self.height + 1
            difficulty = self._expected_difficulty()
            candidate_txs = sorted(self.mempool, key=lambda t: -t.fee)[:max_txs]
            valid_txs = []
            for tx in candidate_txs:
                ok, _ = self.validate_transaction(tx, applying=True)
                if ok:
                    valid_txs.append(tx)
            return tip, next_height, difficulty, valid_txs

    def get_block_by_hash(self, h: str) -> Optional[Block]:
        with self._lock:
            return self.block_index.get(h)

    def get_block_by_height(self, height: int) -> Optional[Block]:
        with self._lock:
            if 0 <= height < len(self.main_chain):
                return self.block_index[self.main_chain[height]]
            return None

    def hash_at_height(self, height: int) -> Optional[str]:
        with self._lock:
            if 0 <= height < len(self.main_chain):
                return self.main_chain[height]
            return None
