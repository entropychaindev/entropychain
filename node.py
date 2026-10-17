"""
Run a full Entropy Chain node.

Usage:
    python node.py --port 9001 --datadir ./node1 --mine
    python node.py --port 9002 --datadir ./node2 --seeds 127.0.0.1:9001
    python node.py --port 9003 --datadir ./node3 --seeds 127.0.0.1:9001,127.0.0.1:9002 --mine

Flags:
    --port      websocket listen port
    --datadir   directory for chain.json + wallet.json
    --seeds     comma-separated host:port seed nodes (optional)
    --mine      run a mining loop in a background thread
    --quiet     suppress most logging
"""

import argparse
import asyncio
import logging
import os
import threading
import time

# NOTE: production mining parameters live in consensus.py.
# The --testnet-fast flag below relaxes them for local development.

from blockchain import EntropyChain
from wallet import Wallet
from miner import Miner
from p2p import P2PNode


def parse_seeds(s: str) -> list[tuple[str, int]]:
    if not s:
        return []
    out = []
    for entry in s.split(","):
        host, _, port = entry.strip().partition(":")
        out.append((host, int(port)))
    return out


async def main_async(args):
    chain = EntropyChain(data_dir=os.path.join(args.datadir, "chaindata"))
    wallet = Wallet(path=os.path.join(args.datadir, "wallet.json"))

    # Make sure we have at least one address to mine to
    if not wallet.addresses():
        addr = wallet.create_address("default")
        print(f"Created mining address: {addr}")
    miner_addr = wallet.addresses()[0]

    seeds = parse_seeds(args.seeds)
    node = P2PNode(chain, listen_port=args.port, seed_nodes=seeds)
    server = await node.start()
    print(f"Node listening on ws://0.0.0.0:{args.port}  "
          f"(node_id={node.node_id[:8]}, mining_to={miner_addr})")

    # RPC server (used by the CLI wallet and explorer)
    if args.rpc_port:
        from rpc_server import RPCServer
        rpc = RPCServer(chain, p2p_node=node, host=args.rpc_host, port=args.rpc_port)
        rpc.attach_loop(asyncio.get_event_loop())
        rpc.start_in_thread()
        print(f"RPC listening on http://{args.rpc_host}:{args.rpc_port}")

    # Mining thread (CPU-bound, so threading not asyncio)
    if args.mine:
        miner = Miner(chain, miner_addr)
        loop = asyncio.get_event_loop()

        def mining_loop():
            while True:
                try:
                    block = miner.mine(max_attempts=500_000, log_every=500_000)
                    if block is None:
                        time.sleep(0.1)
                        continue
                    # Apply locally first
                    ok, reason = chain.add_block(block)
                    if ok:
                        # Schedule announce on the asyncio loop
                        asyncio.run_coroutine_threadsafe(
                            node.announce_local_block(block), loop
                        )
                    else:
                        # Stale (someone else's block beat us); just drop and re-mine
                        if reason != "already known":
                            print(f"  local block rejected: {reason}")
                except Exception as e:
                    print(f"Mining error: {e}")
                    time.sleep(1)

        t = threading.Thread(target=mining_loop, daemon=True)
        t.start()
        print("Mining started.")

    # Run forever
    await asyncio.Future()


def main():
    # Testnet-4 uses beacon v2 (randomness from block hashes, random mints
    # revealed by the next block), which only the Go node implements. Running
    # this node would fork itself off the network, so refuse.
    import sys
    sys.exit("The Python node is retired: testnet-4 consensus (beacon v2) is only "
             "implemented by the Go node. Use go/cmd/entropyd instead.")
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=9001)
    ap.add_argument("--datadir", default="./node_data")
    ap.add_argument("--seeds", default="")
    ap.add_argument("--mine", action="store_true")
    ap.add_argument("--quiet", action="store_true")
    ap.add_argument("--rpc-port", type=int, default=8001,
                    help="HTTP RPC port (set 0 to disable)")
    ap.add_argument("--rpc-host", default="127.0.0.1",
                    help="RPC bind host (use 0.0.0.0 to expose)")
    ap.add_argument("--testnet-fast", action="store_true",
                    help="Lower Argon2 memory cost & genesis difficulty for fast local dev. "
                         "DO NOT USE on a deployed node - breaks consensus with production peers.")
    args = ap.parse_args()

    level = logging.WARNING if args.quiet else logging.INFO
    logging.basicConfig(level=level, format="%(asctime)s %(name)s %(levelname)s %(message)s")

    if args.testnet_fast:
        import consensus
        consensus.ARGON2_MEMORY_KB = 1024
        consensus.GENESIS_DIFFICULTY = 200
        print("⚠  TESTNET-FAST mode: relaxed Argon2 memory cost. "
              "Will not interoperate with production nodes.")

    try:
        asyncio.run(main_async(args))
    except KeyboardInterrupt:
        print("\nShutting down.")


if __name__ == "__main__":
    main()
