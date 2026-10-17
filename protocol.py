"""
Wire protocol for Entropy Chain P2P.

All messages are JSON, one message per websocket frame. Every message has
a 'type' field; payloads vary by type. Message types:

  -- Handshake --
  hello              {version, network_id, node_id, listen_port, tip_hash, tip_height, total_work}
  hello_ack          {version, network_id, node_id, listen_port, tip_hash, tip_height, total_work}

  -- Peer discovery --
  get_peers          {}
  peers              {peers: [(host, port), ...]}

  -- Inventory & sync --
  inv_block          {hash, height}                        (announce a new block we have)
  inv_tx             {hash}                                (announce a tx we have)
  get_block          {hash}
  get_block_by_height {height}
  block              {block: <block dict>}
  get_tx             {hash}
  tx                 {tx: <tx dict>}

  -- Chain sync --
  get_headers        {start_height, count}                 (request headers from tip)
  headers            {headers: [{hash, height, prev_hash}]}
  get_blocks_range   {start_height, count}                 (bulk block download)

The 'network_id' field is critical: it prevents accidentally connecting
to the wrong network (testnet/mainnet). All Entropy Chain mainnet uses
"entropy-chain-mainnet". Anyone running a fork should change it.
"""

import json
import secrets

PROTOCOL_VERSION = 1
NETWORK_ID = "entropy-chain-testnet-4"


def make_node_id() -> str:
    """Random 16-byte node identifier (hex). New per process; not persisted."""
    return secrets.token_hex(16)


def encode(msg: dict) -> str:
    return json.dumps(msg)


def decode(raw: str) -> dict:
    return json.loads(raw)


def make_hello(node_id: str, listen_port: int, tip_hash: str,
               tip_height: int, total_work: int) -> dict:
    return {
        "type": "hello",
        "version": PROTOCOL_VERSION,
        "network_id": NETWORK_ID,
        "node_id": node_id,
        "listen_port": listen_port,
        "tip_hash": tip_hash,
        "tip_height": tip_height,
        "total_work": total_work,
    }


def make_hello_ack(node_id: str, listen_port: int, tip_hash: str,
                   tip_height: int, total_work: int) -> dict:
    msg = make_hello(node_id, listen_port, tip_hash, tip_height, total_work)
    msg["type"] = "hello_ack"
    return msg


def make_inv_block(block_hash: str, height: int) -> dict:
    return {"type": "inv_block", "hash": block_hash, "height": height}


def make_inv_tx(tx_hash: str) -> dict:
    return {"type": "inv_tx", "hash": tx_hash}


def make_get_block(block_hash: str) -> dict:
    return {"type": "get_block", "hash": block_hash}


def make_get_block_by_height(height: int) -> dict:
    return {"type": "get_block_by_height", "height": height}


def make_block_msg(block_dict: dict) -> dict:
    return {"type": "block", "block": block_dict}


def make_get_tx(tx_hash: str) -> dict:
    return {"type": "get_tx", "hash": tx_hash}


def make_tx_msg(tx_dict: dict) -> dict:
    return {"type": "tx", "tx": tx_dict}


def make_get_peers() -> dict:
    return {"type": "get_peers"}


def make_peers(peer_list: list[tuple[str, int]]) -> dict:
    return {"type": "peers", "peers": [list(p) for p in peer_list]}


def make_get_blocks_range(start_height: int, count: int) -> dict:
    return {"type": "get_blocks_range", "start_height": start_height, "count": count}
