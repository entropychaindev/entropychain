"""
RPC server for a running Entropy Chain node.

Runs as a Flask app in a background thread inside the node process. Unlike
explorer.py (which reads chain.json from disk), this server has direct
access to the live in-memory chain, so submitted transactions land in the
node's actual mempool and get gossiped to peers immediately.

Endpoints:
  GET  /rpc/info                       node + chain status
  GET  /rpc/balance/<addr>             all token balances
  GET  /rpc/balance/<addr>/<token>     specific token balance
  GET  /rpc/tokens                     all registered tokens
  GET  /rpc/token/<symbol>             one token's info
  GET  /rpc/nft/<col>/<id>             NFT details
  GET  /rpc/lottery/<id>               lottery state
  GET  /rpc/entropy/<height>           {height, seed}
  GET  /rpc/mempool                    pending tx hashes
  POST /rpc/submit_tx                  body: {"tx": <tx dict>}

This server intentionally exposes only read + tx-submit. It does NOT expose
keys, signing, mining controls, or peer management - those stay process-local.
"""

import asyncio
import logging
import threading
from flask import Flask, jsonify, request

from blockchain import EntropyChain
from transactions import Transaction, NATIVE_TOKEN
from genesis_config import GENESIS_NETWORK_TAG, GENESIS_MESSAGE
import protocol

log = logging.getLogger("rpc")


class RPCServer:
    def __init__(self, chain: EntropyChain, p2p_node, host: str = "127.0.0.1",
                 port: int = 8001):
        self.chain = chain
        self.p2p_node = p2p_node    # may be None for tests
        self.host = host
        self.port = port
        self.app = Flask(__name__)
        self._configure_routes()
        self._asyncio_loop = None   # set when the node passes its loop in
        # Suppress Flask's default request log noise
        logging.getLogger("werkzeug").setLevel(logging.WARNING)

    def attach_loop(self, loop):
        """Called by node.py to give us a handle to the asyncio loop so we can
        announce txs from the Flask thread back into the network layer."""
        self._asyncio_loop = loop

    def _configure_routes(self):
        app = self.app
        chain = self.chain

        @app.route("/rpc/info")
        def info():
            return jsonify({
                "network_id": protocol.NETWORK_ID,
                "network_tag": GENESIS_NETWORK_TAG,
                "genesis_message": GENESIS_MESSAGE,
                "genesis_hash": chain.main_chain[0],
                "height": chain.height,
                "tip_hash": chain.tip_hash,
                "total_work": chain.cumulative_work[chain.tip_hash],
                "difficulty": chain.tip.difficulty,
                "tokens": list(chain.tokens.keys()),
                "nft_count": len(chain.nfts),
                "lottery_count": len(chain.lotteries),
                "entropy_seed": chain.tip.entropy_seed,
                "mempool_size": len(chain.mempool),
                "peer_count": (len(self.p2p_node.peers) if self.p2p_node else 0),
            })

        @app.route("/rpc/balance/<addr>")
        def balance_all(addr):
            return jsonify(dict(chain.balances.get(addr, {})))

        @app.route("/rpc/balance/<addr>/<token>")
        def balance_one(addr, token):
            token = token.upper() if token.upper() == NATIVE_TOKEN or token.upper() in chain.tokens else token
            return jsonify({
                "address": addr,
                "token": token,
                "balance": chain.get_balance(addr, token),
            })

        @app.route("/rpc/tokens")
        def tokens():
            return jsonify(chain.tokens)

        @app.route("/rpc/token/<symbol>")
        def token_one(symbol):
            symbol = symbol.upper()
            t = chain.tokens.get(symbol)
            if not t:
                return jsonify({"error": "not found"}), 404
            return jsonify({"symbol": symbol, **t})

        @app.route("/rpc/nft/<collection>/<token_id>")
        def nft_one(collection, token_id):
            nft = chain.nfts.get((collection, token_id))
            if not nft:
                return jsonify({"error": "not found"}), 404
            return jsonify({
                "collection": collection,
                "token_id": token_id,
                **nft,
            })

        @app.route("/rpc/nfts/<owner>")
        def nfts_owned(owner):
            """All NFTs currently owned by an address."""
            owned = []
            for (cid, tid), nft in chain.nfts.items():
                if nft.get("owner") == owner:
                    owned.append({
                        "collection": cid,
                        "token_id": tid,
                        **nft,
                    })
            owned.sort(key=lambda n: (n.get("minted_at_height", 0), n["collection"], n["token_id"]),
                       reverse=True)
            return jsonify({"owner": owner, "count": len(owned), "nfts": owned})

        @app.route("/rpc/collections")
        def collections_list():
            """All NFT collections, optionally filtered by ?creator=<addr>."""
            creator = request.args.get("creator")
            cols = []
            for cid, c in chain.nft_collections.items():
                if creator and c.get("creator") != creator:
                    continue
                # Count items minted in this collection
                minted = sum(1 for (col, _) in chain.nfts.keys() if col == cid)
                cols.append({"collection_id": cid, "minted_count": minted, **c})
            cols.sort(key=lambda c: c.get("created_at_height", 0), reverse=True)
            return jsonify({"count": len(cols), "collections": cols})

        @app.route("/rpc/lottery/<lid>")
        def lottery_one(lid):
            lot = chain.lotteries.get(lid)
            if not lot:
                return jsonify({"error": "not found"}), 404
            return jsonify({"lottery_id": lid, **lot})

        @app.route("/rpc/entropy/<int:height>")
        def entropy(height):
            b = chain.get_block_by_height(height)
            if not b:
                return jsonify({"error": "not found"}), 404
            return jsonify({
                "height": height,
                "seed": b.entropy_seed,
                "timestamp": b.timestamp,
                "block_hash": b.hash(),
                "miner_nonce": b.miner_nonce,
            })

        @app.route("/rpc/mempool")
        def mempool():
            return jsonify({
                "size": len(chain.mempool),
                "tx_hashes": [tx.hash() for tx in chain.mempool],
            })

        @app.route("/rpc/history/<addr>")
        def history(addr):
            """
            Recent txs involving an address. Backed by the chain's tx_index,
            so this is O(N) where N is the result size — fast even at 1M
            blocks. Pagination: pass ?before=<height> to get older entries.
            """
            try:
                limit = int(request.args.get("limit", "50"))
            except ValueError:
                limit = 50
            limit = max(1, min(limit, 200))
            before_raw = request.args.get("before")
            before_height = None
            if before_raw:
                try:
                    before_height = int(before_raw)
                except ValueError:
                    pass
            entries = chain.get_history(addr, limit=limit, before_height=before_height)
            # Stringify big numbers for cross-language safety (same reason as
            # the desktop wallet's int64 overflow workaround)
            for e in entries:
                d = e.get("data", {})
                for key in ("amount", "prize_amount", "ticket_price", "total_supply"):
                    if key in d and isinstance(d[key], int):
                        d[key] = str(d[key])
                if "fee" in e and isinstance(e["fee"], int):
                    e["fee"] = str(e["fee"])
            return jsonify({"address": addr, "history": entries, "count": len(entries)})

        @app.route("/rpc/submit_tx", methods=["POST"])
        def submit_tx():
            data = request.get_json(silent=True)
            if not data or "tx" not in data:
                return jsonify({"ok": False, "reason": "missing 'tx' field"}), 400
            try:
                tx = Transaction.from_dict(data["tx"])
            except Exception as e:
                return jsonify({"ok": False, "reason": f"bad tx: {e}"}), 400

            ok, reason = chain.submit_transaction(tx)
            if not ok:
                return jsonify({"ok": False, "reason": reason, "tx_hash": tx.hash()}), 400

            # Tx accepted into mempool. Gossip it to peers.
            if self.p2p_node and self._asyncio_loop:
                try:
                    asyncio.run_coroutine_threadsafe(
                        self.p2p_node.announce_local_tx(tx),
                        self._asyncio_loop,
                    )
                except Exception as e:
                    log.warning(f"Failed to announce tx: {e}")

            return jsonify({"ok": True, "reason": "queued", "tx_hash": tx.hash()})

    def start_in_thread(self):
        """Starts Flask on a daemon thread; returns immediately."""
        def run():
            # use_reloader=False is essential: reloader spawns a child process
            self.app.run(host=self.host, port=self.port, debug=False,
                         use_reloader=False, threaded=True)
        t = threading.Thread(target=run, daemon=True, name="rpc-server")
        t.start()
        log.info(f"RPC server listening on http://{self.host}:{self.port}")
        return t
