"""
Entropy Chain P2P node.

Each node:
  - Runs a websocket server on listen_port for incoming connections
  - Connects out to seed nodes + previously discovered peers
  - Maintains a set of connected peers
  - Gossips new blocks and transactions via inv messages
  - Syncs the chain on startup (download missing blocks from highest-work peer)
  - Forwards add_block to the chain, which handles reorgs

Threading model:
  Everything runs in one asyncio event loop. The chain itself is sync code,
  so we just call into it; there's no concurrency on chain mutations because
  asyncio is single-threaded by default and we don't await mid-mutation.

  The miner runs in a separate thread (CPU-bound) and posts mined blocks
  back to the loop via a thread-safe queue.
"""

import asyncio
import json
import logging
import time
import websockets
from typing import Optional

from blockchain import EntropyChain
from block import Block
from transactions import Transaction
import protocol as proto

log = logging.getLogger("p2p")


class Peer:
    """Represents a single connected peer."""
    def __init__(self, ws, address: tuple[str, int], outbound: bool):
        self.ws = ws
        self.address = address  # (host, port) - their listen address (not necessarily socket addr)
        self.outbound = outbound
        self.node_id: Optional[str] = None
        self.tip_hash: Optional[str] = None
        self.tip_height: int = -1
        self.total_work: int = 0
        self.connected_at = time.time()
        self.last_seen = time.time()
        # Misbehavior score: increments on bad messages, peer is banned at 100
        self.misbehavior: int = 0


class P2PNode:
    # Maximum websocket frame size we'll accept. 1MB is generous for blocks.
    MAX_MESSAGE_SIZE = 1 * 1024 * 1024
    # Misbehavior threshold - peer is dropped and IP banned for BAN_DURATION when reached.
    MISBEHAVIOR_BAN_THRESHOLD = 100
    BAN_DURATION_SECONDS = 60 * 60   # 1 hour
    # At most this many simultaneous connections per remote IP.
    MAX_CONNECTIONS_PER_IP = 3

    def __init__(self, chain: EntropyChain, listen_port: int,
                 seed_nodes: Optional[list[tuple[str, int]]] = None,
                 max_peers: int = 16):
        self.chain = chain
        self.listen_port = listen_port
        self.seed_nodes = seed_nodes or []
        self.max_peers = max_peers
        self.node_id = proto.make_node_id()
        self.peers: dict[str, Peer] = {}      # node_id -> Peer
        self.known_peer_addrs: set[tuple[str, int]] = set(self.seed_nodes)
        # Banned IP -> unix timestamp when ban expires
        self.banned_ips: dict[str, float] = {}
        self._stop = False

        # Hook into chain so we re-broadcast our newly mined blocks
        chain.on_new_tip.append(self._on_local_new_tip)
        chain.on_reorg.append(self._on_local_reorg)

    # ----------------- Lifecycle -----------------

    async def start(self):
        log.info(f"Node {self.node_id[:8]} starting on port {self.listen_port}")
        server = await websockets.serve(
            self._handle_inbound, "0.0.0.0", self.listen_port,
            max_size=self.MAX_MESSAGE_SIZE,
        )
        # Connect out to seeds
        for addr in self.seed_nodes:
            asyncio.create_task(self._dial(addr))
        # Periodic tasks
        asyncio.create_task(self._sync_loop())
        asyncio.create_task(self._peer_exchange_loop())
        asyncio.create_task(self._ban_cleanup_loop())
        return server

    def stop(self):
        self._stop = True

    # ----------------- Connection handling -----------------

    async def _handle_inbound(self, ws):
        addr = ws.remote_address
        ip = addr[0]
        # Ban check
        ban_until = self.banned_ips.get(ip, 0)
        if ban_until > time.time():
            log.info(f"Refusing connection from banned IP {ip}")
            await ws.close(code=4001, reason="banned")
            return
        # Per-IP connection cap
        existing_from_ip = sum(1 for p in self.peers.values() if p.address[0] == ip)
        if existing_from_ip >= self.MAX_CONNECTIONS_PER_IP:
            log.info(f"Refusing connection from {ip} (already {existing_from_ip} connections)")
            await ws.close(code=4002, reason="too many connections from your IP")
            return
        log.info(f"Inbound connection from {addr}")
        try:
            await self._peer_loop(ws, peer_addr=addr, outbound=False)
        except websockets.ConnectionClosed:
            pass
        except Exception as e:
            log.warning(f"Inbound peer error: {e}")

    async def _dial(self, addr: tuple[str, int]):
        if any(p.address == addr for p in self.peers.values()):
            return  # already connected
        if len(self.peers) >= self.max_peers:
            return
        url = f"ws://{addr[0]}:{addr[1]}"
        try:
            log.info(f"Dialing {addr}")
            ws = await asyncio.wait_for(websockets.connect(url), timeout=5)
            await self._peer_loop(ws, peer_addr=addr, outbound=True)
        except Exception as e:
            log.debug(f"Dial {addr} failed: {e}")

    async def _peer_loop(self, ws, peer_addr, outbound: bool):
        # Handshake first
        peer = Peer(ws, peer_addr, outbound)

        # Send our hello
        hello = proto.make_hello(
            self.node_id, self.listen_port,
            self.chain.tip_hash, self.chain.height,
            self.chain.cumulative_work[self.chain.tip_hash],
        )
        await ws.send(proto.encode(hello))

        # Receive their hello (or hello_ack)
        try:
            raw = await asyncio.wait_for(ws.recv(), timeout=10)
        except asyncio.TimeoutError:
            log.warning(f"{peer_addr}: handshake timeout")
            return
        msg = proto.decode(raw)
        if msg.get("type") not in ("hello", "hello_ack"):
            log.warning(f"{peer_addr}: expected hello, got {msg.get('type')}")
            return
        if msg.get("network_id") != proto.NETWORK_ID:
            log.warning(f"{peer_addr}: wrong network {msg.get('network_id')}")
            return
        if msg.get("node_id") == self.node_id:
            log.debug(f"{peer_addr}: self-connect, dropping")
            return
        if msg["node_id"] in self.peers:
            log.debug(f"{peer_addr}: already connected to this node_id")
            return

        peer.node_id = msg["node_id"]
        peer.tip_hash = msg["tip_hash"]
        peer.tip_height = msg["tip_height"]
        peer.total_work = msg["total_work"]
        # Their listen port may differ from socket port if they're outbound to us
        their_listen = msg.get("listen_port")
        if their_listen and not outbound:
            # Inbound: update peer.address to use their advertised listen port
            peer.address = (peer_addr[0], their_listen)
        self.known_peer_addrs.add(peer.address)

        # Send hello_ack if they sent hello (outbound peer dialed us)
        if msg["type"] == "hello":
            ack = proto.make_hello_ack(
                self.node_id, self.listen_port,
                self.chain.tip_hash, self.chain.height,
                self.chain.cumulative_work[self.chain.tip_hash],
            )
            await ws.send(proto.encode(ack))

        self.peers[peer.node_id] = peer
        log.info(f"Peer {peer.node_id[:8]} @ {peer.address} connected "
                 f"(their height={peer.tip_height}, ours={self.chain.height})")

        # If they're ahead of us in work, kick off a sync
        if peer.total_work > self.chain.cumulative_work[self.chain.tip_hash]:
            asyncio.create_task(self._sync_from(peer))

        # Main message loop for this peer
        try:
            async for raw in ws:
                peer.last_seen = time.time()
                try:
                    msg = proto.decode(raw)
                    await self._dispatch(peer, msg)
                except Exception as e:
                    log.warning(f"Error handling msg from {peer.node_id[:8]}: {e}")
                    if await self._misbehaved(peer, 10, f"unparseable msg: {e}"):
                        break
        except websockets.ConnectionClosed:
            pass
        finally:
            self.peers.pop(peer.node_id, None)
            log.info(f"Peer {peer.node_id[:8]} disconnected")

    async def _misbehaved(self, peer: Peer, points: int, reason: str) -> bool:
        """Increment a peer's misbehavior score. If they cross the threshold,
        ban their IP and disconnect. Returns True if peer was banned."""
        peer.misbehavior += points
        log.debug(f"Peer {peer.node_id[:8] if peer.node_id else '?'} +{points} misbehavior "
                  f"({peer.misbehavior}/{self.MISBEHAVIOR_BAN_THRESHOLD}): {reason}")
        if peer.misbehavior >= self.MISBEHAVIOR_BAN_THRESHOLD:
            ip = peer.address[0]
            self.banned_ips[ip] = time.time() + self.BAN_DURATION_SECONDS
            log.warning(f"Banning {ip} for {self.BAN_DURATION_SECONDS}s "
                        f"(score={peer.misbehavior}): {reason}")
            try:
                await peer.ws.close(code=4003, reason="banned for misbehavior")
            except Exception:
                pass
            return True
        return False

    async def _ban_cleanup_loop(self):
        """Periodically remove expired bans from memory."""
        while not self._stop:
            await asyncio.sleep(300)
            now = time.time()
            expired = [ip for ip, until in self.banned_ips.items() if until < now]
            for ip in expired:
                del self.banned_ips[ip]

    # ----------------- Message dispatch -----------------

    async def _dispatch(self, peer: Peer, msg: dict):
        t = msg.get("type")
        if t == "inv_block":
            await self._on_inv_block(peer, msg)
        elif t == "inv_tx":
            await self._on_inv_tx(peer, msg)
        elif t == "get_block":
            await self._on_get_block(peer, msg)
        elif t == "get_block_by_height":
            await self._on_get_block_by_height(peer, msg)
        elif t == "block":
            await self._on_block(peer, msg)
        elif t == "get_tx":
            await self._on_get_tx(peer, msg)
        elif t == "tx":
            await self._on_tx(peer, msg)
        elif t == "get_peers":
            await self._on_get_peers(peer, msg)
        elif t == "peers":
            await self._on_peers(peer, msg)
        elif t == "get_blocks_range":
            await self._on_get_blocks_range(peer, msg)
        else:
            log.debug(f"Unknown message type: {t}")

    async def _on_inv_block(self, peer: Peer, msg: dict):
        h = msg["hash"]
        peer.tip_hash = h
        peer.tip_height = max(peer.tip_height, msg.get("height", 0))
        if h not in self.chain.block_index:
            await peer.ws.send(proto.encode(proto.make_get_block(h)))

    async def _on_inv_tx(self, peer: Peer, msg: dict):
        h = msg["hash"]
        if h in self.chain.tx_seen:
            return
        if any(tx.hash() == h for tx in self.chain.mempool):
            return
        await peer.ws.send(proto.encode(proto.make_get_tx(h)))

    async def _on_get_block(self, peer: Peer, msg: dict):
        block = self.chain.get_block_by_hash(msg["hash"])
        if block:
            await peer.ws.send(proto.encode(proto.make_block_msg(block.to_dict())))

    async def _on_get_block_by_height(self, peer: Peer, msg: dict):
        block = self.chain.get_block_by_height(msg["height"])
        if block:
            await peer.ws.send(proto.encode(proto.make_block_msg(block.to_dict())))

    async def _on_block(self, peer: Peer, msg: dict):
        try:
            block = Block.from_dict(msg["block"])
        except Exception as e:
            log.warning(f"Bad block from {peer.node_id[:8]}: {e}")
            await self._misbehaved(peer, 20, f"malformed block: {e}")
            return

        # Try to add. If parent is unknown, request the parent and try again.
        ok, reason = self.chain.add_block(block)
        if ok:
            log.info(f"  ✓ accepted block {block.height} {block.hash()[:12]} "
                     f"from peer {peer.node_id[:8]} ({reason})")
            # Gossip to other peers
            inv = proto.make_inv_block(block.hash(), block.height)
            await self._broadcast(inv, exclude=peer.node_id)
        elif reason == "unknown parent (request from peer)":
            log.info(f"  ↻ block {block.height} parent missing, requesting")
            await peer.ws.send(proto.encode(proto.make_get_block(block.prev_hash)))
        elif reason == "already known":
            pass
        else:
            log.warning(f"  ✗ rejected block from {peer.node_id[:8]}: {reason}")
            # Invalid blocks are an attack vector (waste verification CPU). Score it.
            await self._misbehaved(peer, 25, f"invalid block: {reason}")

    async def _on_get_tx(self, peer: Peer, msg: dict):
        h = msg["hash"]
        for tx in self.chain.mempool:
            if tx.hash() == h:
                await peer.ws.send(proto.encode(proto.make_tx_msg(tx.to_dict())))
                return

    async def _on_tx(self, peer: Peer, msg: dict):
        try:
            tx = Transaction.from_dict(msg["tx"])
        except Exception as e:
            log.warning(f"Bad tx from {peer.node_id[:8]}: {e}")
            await self._misbehaved(peer, 5, f"malformed tx: {e}")
            return
        ok, reason = self.chain.submit_transaction(tx)
        if ok:
            log.debug(f"  ✓ accepted tx {tx.hash()[:12]}")
            inv = proto.make_inv_tx(tx.hash())
            await self._broadcast(inv, exclude=peer.node_id)
        elif reason in ("duplicate transaction", "mempool full and your fee is too low"):
            # Not malicious, just noise
            pass
        else:
            await self._misbehaved(peer, 2, f"invalid tx: {reason}")

    async def _on_get_peers(self, peer: Peer, msg: dict):
        # Share known peer addresses (excluding the requester's)
        addrs = [a for a in self.known_peer_addrs if a != peer.address][:32]
        await peer.ws.send(proto.encode(proto.make_peers(addrs)))

    async def _on_peers(self, peer: Peer, msg: dict):
        for entry in msg.get("peers", []):
            addr = (entry[0], int(entry[1]))
            if addr not in self.known_peer_addrs:
                self.known_peer_addrs.add(addr)

    async def _on_get_blocks_range(self, peer: Peer, msg: dict):
        start = msg["start_height"]
        count = min(msg["count"], 100)  # cap to prevent abuse
        for h in range(start, start + count):
            block = self.chain.get_block_by_height(h)
            if block is None:
                break
            await peer.ws.send(proto.encode(proto.make_block_msg(block.to_dict())))

    # ----------------- Outbound broadcast -----------------

    async def _broadcast(self, msg: dict, exclude: Optional[str] = None):
        encoded = proto.encode(msg)
        dead = []
        for nid, peer in self.peers.items():
            if nid == exclude:
                continue
            try:
                await peer.ws.send(encoded)
            except Exception:
                dead.append(nid)
        for nid in dead:
            self.peers.pop(nid, None)

    # ----------------- Local events -----------------

    def _on_local_new_tip(self, block: Block):
        """Chain just advanced - gossip the new tip."""
        inv = proto.make_inv_block(block.hash(), block.height)
        # We're being called from sync context; schedule the broadcast.
        try:
            loop = asyncio.get_event_loop()
            if loop.is_running():
                asyncio.create_task(self._broadcast(inv))
        except RuntimeError:
            pass  # no loop, e.g. during tests

    def _on_local_reorg(self, old_tip: str, new_tip: str, depth: int):
        log.info(f"  ⟲ REORG: depth={depth}, {old_tip[:12]} -> {new_tip[:12]}")

    # ----------------- Sync loop -----------------

    async def _sync_loop(self):
        """Periodically check if any peer is ahead of us and sync from them."""
        while not self._stop:
            await asyncio.sleep(5)
            if not self.peers:
                continue
            best_peer = max(self.peers.values(), key=lambda p: p.total_work)
            our_work = self.chain.cumulative_work[self.chain.tip_hash]
            if best_peer.total_work > our_work:
                log.info(f"Behind: peer {best_peer.node_id[:8]} has more work, syncing")
                asyncio.create_task(self._sync_from(best_peer))

    async def _sync_from(self, peer: Peer):
        """Request blocks in batches from peer until we catch up."""
        # Strategy: ask for blocks in ranges of 50, starting at our current height + 1.
        # Peers respond with individual block messages, which our normal
        # _on_block handler processes (including reorgs if applicable).
        start = self.chain.height + 1
        # If our tip is divergent from theirs, we need to walk back. Simple
        # approach: ask for blocks-by-height starting at our tip-10 so we get
        # potential overlap that triggers reorg.
        if start > 10:
            start -= 10
        try:
            await peer.ws.send(proto.encode(proto.make_get_blocks_range(start, 50)))
        except Exception as e:
            log.warning(f"Sync request to {peer.node_id[:8]} failed: {e}")

    # ----------------- Peer exchange -----------------

    async def _peer_exchange_loop(self):
        """Periodically ask peers who else they know, and dial unconnected ones."""
        while not self._stop:
            await asyncio.sleep(30)
            for peer in list(self.peers.values()):
                try:
                    await peer.ws.send(proto.encode(proto.make_get_peers()))
                except Exception:
                    pass
            # Try connecting to known addrs we're not yet connected to
            connected_addrs = {p.address for p in self.peers.values()}
            for addr in list(self.known_peer_addrs):
                if addr in connected_addrs:
                    continue
                if len(self.peers) >= self.max_peers:
                    break
                asyncio.create_task(self._dial(addr))

    # ----------------- Public API for the local miner / wallet -----------------

    async def announce_local_block(self, block: Block):
        """Called by the node runner when our miner produces a block."""
        inv = proto.make_inv_block(block.hash(), block.height)
        await self._broadcast(inv)

    async def announce_local_tx(self, tx: Transaction):
        inv = proto.make_inv_tx(tx.hash())
        await self._broadcast(inv)
