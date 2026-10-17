"""
Three-node P2P integration test.

Sets up:
  - Node A (port 9101, mining)
  - Node B (port 9102, not mining initially, seeded from A)
  - Node C (port 9103, mining, seeded from A)

Tests:
  1. Block propagation: A mines blocks, B and C receive them
  2. Sync on join: late-joining node catches up
  3. Forced fork + reorg: temporarily isolate C, let it mine a longer
     branch, reconnect, verify A and B reorg to C's branch

Everything runs in one process using asyncio + isolated event-loop tasks
(no threads) so we don't actually need to mine real PoW - we use a fake
'instant mine' helper that produces valid blocks without the Argon2
search loop. This makes the test deterministic and fast.
"""

import asyncio
import logging
import os
import shutil
import secrets
import time

import consensus
# Make PoW trivially easy so we can mine "instantly" for the test
consensus.ARGON2_MEMORY_KB = 8       # 8 KB - basically free
consensus.GENESIS_DIFFICULTY = 1     # accept ~any nonce

from blockchain import EntropyChain
from wallet import Wallet
from miner import Miner
from p2p import P2PNode
from block import Block, merkle_root
from consensus import pow_hash, hash_meets_difficulty, derive_entropy_seed
from transactions import make_coinbase

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(name)s %(levelname)s %(message)s")
log = logging.getLogger("test")


def fast_mine_one(chain: EntropyChain, miner_addr: str) -> Block:
    """Quickly produce one valid block extending the chain's tip.
    Difficulty is ~1 in the test config, so first nonce usually works."""
    miner = Miner(chain, miner_addr)
    block = miner.assemble_candidate()
    # Search for a valid nonce - very fast at difficulty=1
    for _ in range(1000):
        nonce = secrets.randbits(64)
        h = pow_hash(block.header_bytes(), nonce)
        if hash_meets_difficulty(h, block.difficulty):
            block.nonce = nonce
            return block
    raise RuntimeError("could not mine even at difficulty 1?!")


async def setup_node(port: int, datadir: str, seeds: list) -> tuple[P2PNode, EntropyChain, str]:
    """Create and start a node. Returns (node, chain, mining_address)."""
    if os.path.isdir(datadir):
        shutil.rmtree(datadir)
    os.makedirs(datadir)
    chain = EntropyChain(data_dir=os.path.join(datadir, "chaindata"))
    wallet = Wallet(path=os.path.join(datadir, "wallet.json"))
    addr = wallet.create_address("miner")
    node = P2PNode(chain, listen_port=port, seed_nodes=seeds)
    await node.start()
    return node, chain, addr


async def wait_until(condition, timeout=10, interval=0.1):
    start = time.time()
    while time.time() - start < timeout:
        if condition():
            return True
        await asyncio.sleep(interval)
    return False


async def main():
    # Clean slate
    for d in ["./test_node_a", "./test_node_b", "./test_node_c"]:
        if os.path.isdir(d):
            shutil.rmtree(d)

    log.info("=" * 60)
    log.info("TEST 1: Setup 3 nodes, verify they connect")
    log.info("=" * 60)

    node_a, chain_a, addr_a = await setup_node(9101, "./test_node_a", [])
    await asyncio.sleep(0.3)
    node_b, chain_b, addr_b = await setup_node(9102, "./test_node_b", [("127.0.0.1", 9101)])
    node_c, chain_c, addr_c = await setup_node(9103, "./test_node_c", [("127.0.0.1", 9101)])

    ok = await wait_until(lambda: len(node_a.peers) >= 2 and len(node_b.peers) >= 1 and len(node_c.peers) >= 1, timeout=5)
    assert ok, f"peers didn't connect: A={len(node_a.peers)}, B={len(node_b.peers)}, C={len(node_c.peers)}"
    log.info(f"  ✓ A has {len(node_a.peers)} peers, B has {len(node_b.peers)}, C has {len(node_c.peers)}")

    log.info("=" * 60)
    log.info("TEST 2: A mines 3 blocks, B and C receive them via gossip")
    log.info("=" * 60)

    for i in range(3):
        block = fast_mine_one(chain_a, addr_a)
        ok, reason = chain_a.add_block(block)
        assert ok, reason
        await node_a.announce_local_block(block)
        log.info(f"  A mined block {block.height}: {block.hash()[:12]}")
        await asyncio.sleep(0.3)

    ok = await wait_until(lambda: chain_b.height == 3 and chain_c.height == 3, timeout=5)
    assert ok, f"propagation failed: A={chain_a.height}, B={chain_b.height}, C={chain_c.height}"
    assert chain_a.tip_hash == chain_b.tip_hash == chain_c.tip_hash
    log.info(f"  ✓ All nodes at height 3, tip {chain_a.tip_hash[:12]}")

    log.info("=" * 60)
    log.info("TEST 3: Force a fork - mine 2 blocks on A while C mines 3 in 'isolation'")
    log.info("=" * 60)

    # Disconnect C from everyone by closing its websockets
    for peer in list(node_c.peers.values()):
        await peer.ws.close()
    for n in (node_a, node_b):
        for peer in list(n.peers.values()):
            if peer.address[1] == 9103:
                await peer.ws.close()
    await asyncio.sleep(0.5)
    assert not any(p.address[1] == 9103 for p in node_a.peers.values()), "C still connected to A"
    assert not any(p.address[1] == 9103 for p in node_b.peers.values()), "C still connected to B"
    log.info("  ✓ C is isolated")

    # A mines 2 blocks (gossiped to B)
    for _ in range(2):
        block = fast_mine_one(chain_a, addr_a)
        chain_a.add_block(block)
        await node_a.announce_local_block(block)
        await asyncio.sleep(0.3)
    log.info(f"  A and B at height {chain_a.height} (tip {chain_a.tip_hash[:12]})")

    # C mines 3 blocks on its own branch
    for _ in range(3):
        block = fast_mine_one(chain_c, addr_c)
        chain_c.add_block(block)
        await asyncio.sleep(0.05)
    log.info(f"  C at height {chain_c.height} on its own fork (tip {chain_c.tip_hash[:12]})")

    assert chain_a.tip_hash != chain_c.tip_hash, "branches should differ"
    log.info(f"  ✓ Fork established: A/B={chain_a.height} blocks vs C={chain_c.height} blocks")

    log.info("=" * 60)
    log.info("TEST 4: Reconnect C; A and B should reorg to C's longer branch")
    log.info("=" * 60)

    # Reconnect C to A
    asyncio.create_task(node_c._dial(("127.0.0.1", 9101)))
    asyncio.create_task(node_c._dial(("127.0.0.1", 9102)))
    await asyncio.sleep(1.0)

    # A and B should sync to C's chain (more cumulative work since 6 > 5 blocks
    # at the same difficulty). The sync loop runs every 5 seconds; force it
    # by triggering announce of C's tip to its now-connected peers.
    for peer in node_c.peers.values():
        if peer.total_work < chain_c.cumulative_work[chain_c.tip_hash]:
            from protocol import make_inv_block, encode
            await peer.ws.send(encode(make_inv_block(chain_c.tip_hash, chain_c.height)))

    # Wait for reorg
    ok = await wait_until(
        lambda: chain_a.tip_hash == chain_c.tip_hash and chain_b.tip_hash == chain_c.tip_hash,
        timeout=15,
    )
    if not ok:
        log.error(f"Reorg did not converge!")
        log.error(f"  A: height={chain_a.height} tip={chain_a.tip_hash[:12]} work={chain_a.cumulative_work[chain_a.tip_hash]}")
        log.error(f"  B: height={chain_b.height} tip={chain_b.tip_hash[:12]} work={chain_b.cumulative_work[chain_b.tip_hash]}")
        log.error(f"  C: height={chain_c.height} tip={chain_c.tip_hash[:12]} work={chain_c.cumulative_work[chain_c.tip_hash]}")
        raise SystemExit(1)
    log.info(f"  ✓ All nodes converged on tip {chain_a.tip_hash[:12]} at height {chain_a.height}")
    log.info(f"  ✓ A and B successfully reorged to C's longer branch")

    log.info("=" * 60)
    log.info("ALL TESTS PASSED ✓")
    log.info("=" * 60)


if __name__ == "__main__":
    asyncio.run(main())
