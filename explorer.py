"""
Block explorer + JSON-RPC for Entropy Chain.

Runs as a separate process from your node, sharing the same chaindata
directory. The explorer re-reads the chain from disk periodically so it
stays roughly in sync with whatever node is writing to that directory.

Routes:
  /                              dashboard - tip, stats, recent blocks
  /block/<height_or_hash>        block detail with transactions
  /tx/<tx_hash>                  transaction detail
  /address/<addr>                balance, NFTs owned, recent activity
  /token/<symbol>                token info, holders
  /nft/<collection>/<token_id>   NFT detail with traits and entropy source
  /lottery/<id>                  lottery state, entrants, winner
  /entropy                       randomness beacon stream

  /rpc/info                      GET   {height, tip_hash, total_work, ...}
  /rpc/block/<h>                 GET   block by height
  /rpc/block_by_hash/<h>         GET   block by hash
  /rpc/balance/<addr>            GET   {ENTRO: ..., CHAOS: ..., ...}
  /rpc/tokens                    GET   list of all tokens
  /rpc/nft/<col>/<tid>           GET   single NFT
  /rpc/lottery/<id>              GET   lottery state
  /rpc/entropy/<height>          GET   {height, seed} for a block
  /rpc/submit_tx                 POST  {tx: <tx dict>} -> queues to local mempool

Usage:
    python explorer.py --datadir ./node1 --port 8080
"""

import argparse
import json
import os
import time
from collections import defaultdict
from flask import Flask, request, jsonify, abort, render_template_string

import consensus
consensus.ARGON2_MEMORY_KB = 1024
consensus.GENESIS_DIFFICULTY = 200

from blockchain import EntropyChain
from transactions import Transaction, NATIVE_TOKEN
from genesis_config import GENESIS_NETWORK_TAG, GENESIS_MESSAGE


# ============================================================
# Lazy chain accessor with auto-reload
# ============================================================

class ChainView:
    """Loads chain from disk, reloads when chain.json mtime changes."""
    def __init__(self, datadir: str):
        self.datadir = datadir
        self.chain_path = os.path.join(datadir, "chaindata", "chain.json")
        self._chain: EntropyChain | None = None
        self._last_mtime = 0

    def get(self) -> EntropyChain:
        try:
            mtime = os.path.getmtime(self.chain_path)
        except FileNotFoundError:
            mtime = 0
        if self._chain is None or mtime != self._last_mtime:
            self._chain = EntropyChain(data_dir=os.path.join(self.datadir, "chaindata"))
            self._last_mtime = mtime
        return self._chain


# ============================================================
# HTML template — single page with embedded styles
# ============================================================

PAGE = """<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>{{ title }} — Entropy Chain</title>
<style>
:root {
  --bg: #0a0e14;
  --panel: #111821;
  --panel-2: #161f2c;
  --border: #1f2a3a;
  --text: #c8d3e0;
  --muted: #6b7a8e;
  --accent: #00d4aa;
  --accent-2: #ff6b9d;
  --warn: #ffb454;
  --mono: 'SF Mono', 'Consolas', 'Menlo', monospace;
}
* { box-sizing: border-box; }
body {
  margin: 0; background: var(--bg); color: var(--text);
  font-family: -apple-system, BlinkMacSystemFont, 'Inter', sans-serif;
  font-size: 14px; line-height: 1.5;
}
header {
  background: var(--panel); border-bottom: 1px solid var(--border);
  padding: 14px 24px; display: flex; align-items: center; gap: 24px;
}
header .logo { font-weight: 700; font-size: 18px; color: var(--accent); letter-spacing: 0.5px; }
header .logo span { color: var(--accent-2); }
header nav { display: flex; gap: 18px; }
header nav a { color: var(--muted); text-decoration: none; font-size: 13px; }
header nav a:hover { color: var(--text); }
header form { margin-left: auto; }
header input {
  background: var(--bg); border: 1px solid var(--border); color: var(--text);
  padding: 6px 10px; border-radius: 4px; min-width: 320px; font-family: var(--mono);
  font-size: 12px;
}
.container { max-width: 1200px; margin: 0 auto; padding: 24px; }
.stats {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 12px; margin-bottom: 24px;
}
.stat {
  background: var(--panel); border: 1px solid var(--border); border-radius: 6px;
  padding: 14px 16px;
}
.stat .label { color: var(--muted); font-size: 11px; text-transform: uppercase; letter-spacing: 0.5px; }
.stat .value { font-size: 22px; font-weight: 600; margin-top: 4px; color: var(--accent); }
.panel {
  background: var(--panel); border: 1px solid var(--border); border-radius: 6px;
  margin-bottom: 24px;
}
.panel h2 {
  margin: 0; padding: 14px 18px; border-bottom: 1px solid var(--border);
  font-size: 14px; text-transform: uppercase; letter-spacing: 0.5px; color: var(--muted);
  font-weight: 600;
}
.panel-body { padding: 14px 18px; }
table { width: 100%; border-collapse: collapse; font-size: 13px; }
th, td {
  text-align: left; padding: 10px 18px; border-bottom: 1px solid var(--border);
}
th { color: var(--muted); font-weight: 500; font-size: 11px; text-transform: uppercase; letter-spacing: 0.5px; }
tr:last-child td { border-bottom: none; }
tr:hover { background: var(--panel-2); }
a { color: var(--accent); text-decoration: none; }
a:hover { text-decoration: underline; }
.mono { font-family: var(--mono); font-size: 12px; }
.hash { font-family: var(--mono); font-size: 12px; color: var(--accent); }
.addr { font-family: var(--mono); font-size: 12px; color: var(--accent-2); }
.dim { color: var(--muted); }
.kv { display: grid; grid-template-columns: 200px 1fr; gap: 8px 18px; padding: 6px 0; }
.kv .k { color: var(--muted); }
.kv .v { font-family: var(--mono); font-size: 12px; word-break: break-all; }
.tag {
  display: inline-block; padding: 2px 8px; border-radius: 3px; font-size: 11px;
  background: var(--panel-2); border: 1px solid var(--border); color: var(--muted);
  text-transform: uppercase; letter-spacing: 0.5px;
}
.tag.coinbase { color: var(--warn); border-color: var(--warn); }
.tag.lottery  { color: var(--accent-2); border-color: var(--accent-2); }
.tag.nft      { color: var(--accent); border-color: var(--accent); }
.entropy {
  font-family: var(--mono); font-size: 11px; color: var(--accent-2);
  word-break: break-all; padding: 8px 12px; background: var(--bg);
  border-radius: 4px; border: 1px solid var(--border);
}
.error { color: var(--warn); padding: 24px; }
.trait { display: inline-block; margin: 2px 4px 2px 0; padding: 3px 9px; background: var(--panel-2); border: 1px solid var(--border); border-radius: 3px; font-size: 11px; }
.trait.legendary { color: var(--warn); border-color: var(--warn); font-weight: 600; }
</style>
</head>
<body>
<header>
  <div class="logo">⟁ ENTROPY<span>CHAIN</span></div>
  <span class="tag" style="color: var(--warn); border-color: var(--warn);">{{ network_tag }}</span>
  <nav>
    <a href="/">Dashboard</a>
    <a href="/entropy">Entropy Beacon</a>
    <a href="/rpc/info">RPC</a>
  </nav>
  <form action="/search" method="get">
    <input name="q" placeholder="Search block / hash / address / token / lottery..." autocomplete="off">
  </form>
</header>
<div class="container">{{ body|safe }}</div>
</body></html>
"""


def page(title: str, body: str) -> str:
    return render_template_string(PAGE, title=title, body=body,
                                  network_tag=GENESIS_NETWORK_TAG.upper())


def fmt_amount(amount: int, decimals: int = 8) -> str:
    """Format an integer 'satoshi-style' amount with the given decimals."""
    whole = amount // (10 ** decimals)
    frac = amount % (10 ** decimals)
    if frac == 0:
        return f"{whole:,}"
    s = f"{whole:,}.{frac:0{decimals}d}".rstrip("0").rstrip(".")
    return s


def short_hash(h: str, n: int = 12) -> str:
    return h if len(h) <= n else f"{h[:n]}…"


def short_addr(a: str, n: int = 16) -> str:
    return a if len(a) <= n else f"{a[:6]}…{a[-6:]}"


def time_ago(ts: float) -> str:
    delta = time.time() - ts
    if delta < 60: return f"{int(delta)}s ago"
    if delta < 3600: return f"{int(delta/60)}m ago"
    if delta < 86400: return f"{int(delta/3600)}h ago"
    return f"{int(delta/86400)}d ago"


# ============================================================
# Flask app
# ============================================================

app = Flask(__name__)
chain_view: ChainView = None  # set in main()


def get_chain() -> EntropyChain:
    return chain_view.get()


# --------------- HTML routes ---------------

@app.route("/")
def dashboard():
    chain = get_chain()
    total_supply = sum(
        chain.get_balance(a) for a in chain.balances
    )

    stats_html = f"""
    <div class="stats">
      <div class="stat"><div class="label">Height</div><div class="value">{chain.height:,}</div></div>
      <div class="stat"><div class="label">Total ENTRO</div><div class="value">{fmt_amount(total_supply)}</div></div>
      <div class="stat"><div class="label">Difficulty</div><div class="value">{chain.tip.difficulty:,}</div></div>
      <div class="stat"><div class="label">Tokens</div><div class="value">{len(chain.tokens)}</div></div>
      <div class="stat"><div class="label">NFTs</div><div class="value">{len(chain.nfts)}</div></div>
      <div class="stat"><div class="label">Lotteries</div><div class="value">{len(chain.lotteries)}</div></div>
    </div>
    """

    # Recent blocks table
    rows = []
    recent = list(reversed(chain.main_chain[-15:]))
    for h in recent:
        b = chain.block_index[h]
        n_tx = len(b.transactions)
        rows.append(f"""
        <tr>
          <td><a href="/block/{b.height}">{b.height}</a></td>
          <td><a href="/block/{h}" class="hash">{short_hash(h)}</a></td>
          <td>{n_tx}</td>
          <td class="dim">{time_ago(b.timestamp)}</td>
          <td class="mono">{b.difficulty:,}</td>
          <td><span class="entropy" style="display:inline-block; padding:2px 6px; font-size:10px;">{b.entropy_seed[:16]}…</span></td>
        </tr>""")

    # Token list
    token_rows = []
    for sym, info in chain.tokens.items():
        token_rows.append(f"""
        <tr>
          <td><a href="/token/{sym}"><strong>{sym}</strong></a></td>
          <td class="dim">{info['name']}</td>
          <td class="mono">{fmt_amount(info['total_supply'], info.get('decimals', 8))}</td>
          <td><a href="/address/{info['creator']}" class="addr">{short_addr(info['creator'])}</a></td>
        </tr>""")
    if not token_rows:
        token_rows = ['<tr><td colspan="4" class="dim" style="padding:18px;">No tokens created yet.</td></tr>']

    body = stats_html + f"""
    <div class="panel">
      <h2>Recent Blocks</h2>
      <table>
        <tr><th>Height</th><th>Hash</th><th>Txs</th><th>Time</th><th>Difficulty</th><th>Entropy Seed</th></tr>
        {''.join(rows)}
      </table>
    </div>
    <div class="panel">
      <h2>Tokens</h2>
      <table>
        <tr><th>Symbol</th><th>Name</th><th>Supply</th><th>Creator</th></tr>
        {''.join(token_rows)}
      </table>
    </div>
    """
    return page("Dashboard", body)


@app.route("/block/<key>")
def block_detail(key):
    chain = get_chain()
    block = None
    if key.isdigit():
        block = chain.get_block_by_height(int(key))
    else:
        block = chain.get_block_by_hash(key)
    if block is None:
        return page("Block Not Found", '<div class="error">Block not found.</div>'), 404

    h = block.hash()
    is_main = h in chain.main_chain
    on_main = "On main chain" if is_main else "Side branch (orphaned)"
    work = chain.cumulative_work.get(h, 0)

    # Transaction rows
    tx_rows = []
    for tx in block.transactions:
        tag = ""
        from transactions import (TX_COINBASE, TX_LOTTERY_CREATE, TX_LOTTERY_ENTER,
                                  TX_LOTTERY_DRAW, TX_NFT_RANDOM_MINT, TX_NFT_MINT,
                                  TX_NFT_CREATE, TX_NFT_TRANSFER, TX_TOKEN_CREATE,
                                  TX_TOKEN_MINT)
        if tx.tx_type == TX_COINBASE:
            tag = '<span class="tag coinbase">COINBASE</span>'
        elif tx.tx_type.startswith("lottery"):
            tag = '<span class="tag lottery">LOTTERY</span>'
        elif "nft" in tx.tx_type:
            tag = '<span class="tag nft">NFT</span>'
        else:
            tag = f'<span class="tag">{tx.tx_type.upper()}</span>'
        sender = short_addr(tx.sender) if tx.sender else "—"
        sender_html = f'<a href="/address/{tx.sender}" class="addr">{sender}</a>' if tx.sender else '<span class="dim">—</span>'
        tx_rows.append(f"""
        <tr>
          <td><a href="/tx/{tx.hash()}" class="hash">{short_hash(tx.hash(), 16)}</a></td>
          <td>{tag}</td>
          <td>{sender_html}</td>
          <td class="mono">{tx.fee}</td>
        </tr>""")

    body = f"""
    <h1 style="margin-top:0;">Block #{block.height}</h1>
    <div class="panel">
      <div class="panel-body">
        <div class="kv"><span class="k">Hash</span><span class="v">{h}</span></div>
        <div class="kv"><span class="k">Status</span><span class="v">{on_main}</span></div>
        <div class="kv"><span class="k">Previous</span><span class="v"><a href="/block/{block.prev_hash}" class="hash">{block.prev_hash}</a></span></div>
        <div class="kv"><span class="k">Merkle Root</span><span class="v">{block.merkle_root_hex}</span></div>
        <div class="kv"><span class="k">Timestamp</span><span class="v">{time.strftime('%Y-%m-%d %H:%M:%S UTC', time.gmtime(block.timestamp))} ({time_ago(block.timestamp)})</span></div>
        <div class="kv"><span class="k">Difficulty</span><span class="v">{block.difficulty:,}</span></div>
        <div class="kv"><span class="k">Cumulative Work</span><span class="v">{work:,}</span></div>
        <div class="kv"><span class="k">Nonce</span><span class="v">{block.nonce}</span></div>
        <div class="kv"><span class="k">Miner Nonce</span><span class="v">{block.miner_nonce}</span></div>
        <div class="kv"><span class="k">Entropy Seed</span><span class="v"><span class="entropy">{block.entropy_seed}</span></span></div>
      </div>
    </div>

    <div class="panel">
      <h2>Transactions ({len(block.transactions)})</h2>
      <table>
        <tr><th>Hash</th><th>Type</th><th>Sender</th><th>Fee</th></tr>
        {''.join(tx_rows)}
      </table>
    </div>
    """
    return page(f"Block #{block.height}", body)


@app.route("/tx/<tx_hash>")
def tx_detail(tx_hash):
    chain = get_chain()
    # Search through blocks for this tx (small chains; for production, index)
    found_block = None
    found_tx = None
    for h in chain.main_chain:
        b = chain.block_index[h]
        for tx in b.transactions:
            if tx.hash() == tx_hash:
                found_block = b
                found_tx = tx
                break
        if found_tx:
            break
    if found_tx is None:
        return page("Transaction Not Found", '<div class="error">Transaction not found in main chain.</div>'), 404

    sender_html = (f'<a href="/address/{found_tx.sender}" class="addr">{found_tx.sender}</a>'
                   if found_tx.sender else '<span class="dim">—</span>')

    data_rows = []
    for k, v in found_tx.data.items():
        if isinstance(v, str) and len(v) > 30 and (v.startswith("E") or all(c in "0123456789abcdef" for c in v[:20].lower())):
            # Looks like an address or hash
            if v.startswith("E"):
                v_html = f'<a href="/address/{v}" class="addr">{v}</a>'
            else:
                v_html = f'<span class="hash">{v}</span>'
        else:
            v_html = f'<span class="mono">{json.dumps(v) if not isinstance(v, str) else v}</span>'
        data_rows.append(f'<div class="kv"><span class="k">{k}</span><span class="v">{v_html}</span></div>')

    body = f"""
    <h1 style="margin-top:0;">Transaction</h1>
    <div class="panel">
      <div class="panel-body">
        <div class="kv"><span class="k">Hash</span><span class="v">{tx_hash}</span></div>
        <div class="kv"><span class="k">Type</span><span class="v">{found_tx.tx_type}</span></div>
        <div class="kv"><span class="k">Block</span><span class="v"><a href="/block/{found_block.height}">#{found_block.height}</a></span></div>
        <div class="kv"><span class="k">Sender</span><span class="v">{sender_html}</span></div>
        <div class="kv"><span class="k">Fee</span><span class="v">{found_tx.fee}</span></div>
        <div class="kv"><span class="k">Timestamp</span><span class="v">{time.strftime('%Y-%m-%d %H:%M:%S UTC', time.gmtime(found_tx.timestamp))}</span></div>
      </div>
    </div>
    <div class="panel">
      <h2>Payload</h2>
      <div class="panel-body">{''.join(data_rows)}</div>
    </div>
    """
    return page("Transaction", body)


@app.route("/address/<addr>")
def address_detail(addr):
    chain = get_chain()
    balances = dict(chain.balances.get(addr, {}))

    # NFTs owned
    owned_nfts = [(cid, tid, nft) for (cid, tid), nft in chain.nfts.items() if nft["owner"] == addr]

    # Tokens created
    created_tokens = [s for s, info in chain.tokens.items() if info["creator"] == addr]
    created_collections = [c for c, info in chain.nft_collections.items() if info["creator"] == addr]

    bal_rows = []
    for tok, amt in balances.items():
        if amt == 0:
            continue
        decimals = chain.tokens.get(tok, {}).get("decimals", 8)
        link = f'<a href="/token/{tok}">{tok}</a>' if tok != NATIVE_TOKEN else f'<strong>{tok}</strong>'
        bal_rows.append(f"<tr><td>{link}</td><td class='mono'>{fmt_amount(amt, decimals)}</td></tr>")
    if not bal_rows:
        bal_rows = ['<tr><td colspan="2" class="dim">No balance.</td></tr>']

    nft_rows = []
    for cid, tid, nft in owned_nfts[:50]:
        traits = nft.get("traits", {})
        leg = " ⭐" if traits.get("is_legendary") else ""
        nft_rows.append(f"""<tr>
          <td><a href="/nft/{cid}/{tid}">{cid} #{tid}</a>{leg}</td>
          <td class="dim">{traits.get('element', '—')}</td>
          <td class="mono">rarity {traits.get('rarity', '—')}</td>
        </tr>""")
    if not nft_rows:
        nft_rows = ['<tr><td colspan="3" class="dim">No NFTs owned.</td></tr>']

    body = f"""
    <h1 style="margin-top:0;">Address</h1>
    <div class="panel"><div class="panel-body">
      <div class="kv"><span class="k">Address</span><span class="v">{addr}</span></div>
      <div class="kv"><span class="k">Created Tokens</span><span class="v">{', '.join(created_tokens) or '<span class=dim>none</span>'}</span></div>
      <div class="kv"><span class="k">Created NFT Collections</span><span class="v">{', '.join(created_collections) or '<span class=dim>none</span>'}</span></div>
    </div></div>
    <div class="panel">
      <h2>Balances</h2>
      <table><tr><th>Token</th><th>Amount</th></tr>{''.join(bal_rows)}</table>
    </div>
    <div class="panel">
      <h2>NFTs Owned ({len(owned_nfts)})</h2>
      <table><tr><th>NFT</th><th>Element</th><th>Rarity</th></tr>{''.join(nft_rows)}</table>
    </div>
    """
    return page(f"Address {short_addr(addr)}", body)


@app.route("/token/<symbol>")
def token_detail(symbol):
    symbol = symbol.upper()
    chain = get_chain()
    if symbol != NATIVE_TOKEN and symbol not in chain.tokens:
        return page("Token Not Found", '<div class="error">Token not found.</div>'), 404

    if symbol == NATIVE_TOKEN:
        info = {"name": "Entropy Chain Native Token", "decimals": 8, "creator": "—"}
        total = sum(b[NATIVE_TOKEN] for b in chain.balances.values())
    else:
        info = chain.tokens[symbol]
        total = info["total_supply"]

    # Holders
    holders = [(addr, bal[symbol]) for addr, bal in chain.balances.items() if bal.get(symbol, 0) > 0]
    holders.sort(key=lambda x: -x[1])
    holder_rows = []
    for addr, amt in holders[:50]:
        pct = (amt / total * 100) if total else 0
        holder_rows.append(f"""<tr>
          <td><a href="/address/{addr}" class="addr">{short_addr(addr)}</a></td>
          <td class="mono">{fmt_amount(amt, info.get('decimals', 8))}</td>
          <td class="dim">{pct:.2f}%</td>
        </tr>""")
    if not holder_rows:
        holder_rows = ['<tr><td colspan="3" class="dim">No holders.</td></tr>']

    creator_html = (f'<a href="/address/{info["creator"]}" class="addr">{info["creator"]}</a>'
                    if info["creator"] != "—" else "—")

    body = f"""
    <h1 style="margin-top:0;">{symbol}</h1>
    <div class="panel"><div class="panel-body">
      <div class="kv"><span class="k">Name</span><span class="v">{info['name']}</span></div>
      <div class="kv"><span class="k">Decimals</span><span class="v">{info.get('decimals', 8)}</span></div>
      <div class="kv"><span class="k">Total Supply</span><span class="v">{fmt_amount(total, info.get('decimals', 8))}</span></div>
      <div class="kv"><span class="k">Creator</span><span class="v">{creator_html}</span></div>
      <div class="kv"><span class="k">Holders</span><span class="v">{len(holders)}</span></div>
    </div></div>
    <div class="panel">
      <h2>Top Holders</h2>
      <table><tr><th>Address</th><th>Balance</th><th>%</th></tr>{''.join(holder_rows)}</table>
    </div>
    """
    return page(f"Token {symbol}", body)


@app.route("/nft/<collection>/<token_id>")
def nft_detail(collection, token_id):
    chain = get_chain()
    nft = chain.nfts.get((collection, token_id))
    if not nft:
        return page("NFT Not Found", '<div class="error">NFT not found.</div>'), 404

    traits = nft.get("traits", {})
    trait_chips = []
    for k, v in traits.items():
        cls = "trait legendary" if k == "is_legendary" and v else "trait"
        trait_chips.append(f'<span class="{cls}"><strong>{k}:</strong> {v}</span>')

    entropy_height = nft.get("entropy_source_height")
    entropy_html = ""
    if entropy_height is not None:
        entropy_block = chain.get_block_by_height(entropy_height)
        if entropy_block:
            entropy_html = f"""
            <div class="kv"><span class="k">Entropy Source Block</span>
              <span class="v"><a href="/block/{entropy_height}">#{entropy_height}</a></span></div>
            <div class="kv"><span class="k">Source Entropy Seed</span>
              <span class="v"><span class="entropy">{entropy_block.entropy_seed}</span></span></div>
            """

    body = f"""
    <h1 style="margin-top:0;">{collection} #{token_id}</h1>
    <div class="panel"><div class="panel-body">
      <div class="kv"><span class="k">Collection</span><span class="v">{collection}</span></div>
      <div class="kv"><span class="k">Token ID</span><span class="v">{token_id}</span></div>
      <div class="kv"><span class="k">Owner</span><span class="v"><a href="/address/{nft['owner']}" class="addr">{nft['owner']}</a></span></div>
      <div class="kv"><span class="k">Creator</span><span class="v"><a href="/address/{nft['creator']}" class="addr">{nft['creator']}</a></span></div>
      <div class="kv"><span class="k">Metadata URI</span><span class="v">{nft.get('metadata_uri') or '<span class=dim>none</span>'}</span></div>
      <div class="kv"><span class="k">Minted at Height</span><span class="v"><a href="/block/{nft['minted_at_height']}">#{nft['minted_at_height']}</a></span></div>
      {entropy_html}
    </div></div>
    <div class="panel">
      <h2>Traits</h2>
      <div class="panel-body">{''.join(trait_chips) or '<span class="dim">No traits.</span>'}</div>
    </div>
    """
    return page(f"NFT {collection}#{token_id}", body)


@app.route("/lottery/<lid>")
def lottery_detail(lid):
    chain = get_chain()
    lot = chain.lotteries.get(lid)
    if not lot:
        return page("Lottery Not Found", '<div class="error">Lottery not found.</div>'), 404

    entrant_rows = []
    for addr in lot["entrants"]:
        is_winner = (addr == lot["winner"])
        winmark = " 🏆" if is_winner else ""
        entrant_rows.append(f"""<tr>
          <td><a href="/address/{addr}" class="addr">{short_addr(addr)}</a>{winmark}</td>
        </tr>""")
    if not entrant_rows:
        entrant_rows = ['<tr><td class="dim">No entrants yet.</td></tr>']

    status = "🏆 DRAWN" if lot["drawn"] else (
        "🟢 OPEN" if chain.height + 1 < lot["draw_height"] else "🟡 READY TO DRAW"
    )

    winner_html = (f'<a href="/address/{lot["winner"]}" class="addr">{lot["winner"]}</a>'
                   if lot["winner"] else '<span class="dim">not drawn</span>')

    entropy_html = ""
    if lot.get("entropy_used"):
        entropy_html = f"""
        <div class="kv"><span class="k">Draw Block</span><span class="v"><a href="/block/{lot['draw_height']}">#{lot['draw_height']}</a></span></div>
        <div class="kv"><span class="k">Entropy Used</span><span class="v"><span class="entropy">{lot['entropy_used']}</span></span></div>
        """

    body = f"""
    <h1 style="margin-top:0;">Lottery: {lid}</h1>
    <div class="panel"><div class="panel-body">
      <div class="kv"><span class="k">Status</span><span class="v">{status}</span></div>
      <div class="kv"><span class="k">Organizer</span><span class="v"><a href="/address/{lot['organizer']}" class="addr">{short_addr(lot['organizer'])}</a></span></div>
      <div class="kv"><span class="k">Prize</span><span class="v">{fmt_amount(lot['prize_amount'])} {lot['token']}</span></div>
      <div class="kv"><span class="k">Ticket Price</span><span class="v">{fmt_amount(lot['ticket_price'])} ENTRO</span></div>
      <div class="kv"><span class="k">Entry Pool</span><span class="v">{fmt_amount(lot['entry_pool'])} ENTRO</span></div>
      <div class="kv"><span class="k">Draw Height</span><span class="v">#{lot['draw_height']} (current: #{chain.height})</span></div>
      <div class="kv"><span class="k">Entrants</span><span class="v">{len(lot['entrants'])}</span></div>
      <div class="kv"><span class="k">Winner</span><span class="v">{winner_html}</span></div>
      {entropy_html}
    </div></div>
    <div class="panel">
      <h2>Entrants ({len(lot['entrants'])})</h2>
      <table>{''.join(entrant_rows)}</table>
    </div>
    """
    return page(f"Lottery {lid}", body)


@app.route("/entropy")
def entropy_stream():
    chain = get_chain()
    rows = []
    for h in reversed(chain.main_chain[-30:]):
        b = chain.block_index[h]
        if b.height == 0:
            continue
        rows.append(f"""
        <tr>
          <td><a href="/block/{b.height}">#{b.height}</a></td>
          <td><span class="entropy">{b.entropy_seed}</span></td>
          <td class="dim">{time_ago(b.timestamp)}</td>
        </tr>""")

    body = f"""
    <h1 style="margin-top:0;">Entropy Beacon</h1>
    <p class="dim">Every block contributes 32 bytes of verifiable randomness, derived deterministically from the previous block's hash and the miner's chosen nonce. Native lottery and random-mint transactions consume these seeds.</p>
    <div class="panel">
      <table>
        <tr><th>Height</th><th>Entropy Seed</th><th>Time</th></tr>
        {''.join(rows)}
      </table>
    </div>
    """
    return page("Entropy Beacon", body)


@app.route("/search")
def search():
    q = request.args.get("q", "").strip()
    chain = get_chain()
    if not q:
        return page("Search", '<div class="error">Empty query.</div>'), 400
    if q.isdigit():
        return _redirect(f"/block/{q}")
    if len(q) == 64 and all(c in "0123456789abcdef" for c in q.lower()):
        # Block or tx hash
        if q in chain.block_index:
            return _redirect(f"/block/{q}")
        return _redirect(f"/tx/{q}")
    if q.startswith("E") and 25 <= len(q) <= 40:
        return _redirect(f"/address/{q}")
    if q.upper() in chain.tokens or q.upper() == NATIVE_TOKEN:
        return _redirect(f"/token/{q.upper()}")
    if q in chain.lotteries:
        return _redirect(f"/lottery/{q}")
    return page("Not Found", f'<div class="error">No match for: <span class="mono">{q}</span></div>'), 404


def _redirect(path):
    from flask import redirect
    return redirect(path)


# --------------- JSON-RPC routes ---------------

@app.route("/rpc/info")
def rpc_info():
    chain = get_chain()
    return jsonify({
        "height": chain.height,
        "tip_hash": chain.tip_hash,
        "total_work": chain.cumulative_work[chain.tip_hash],
        "difficulty": chain.tip.difficulty,
        "tokens": list(chain.tokens.keys()),
        "nft_count": len(chain.nfts),
        "lottery_count": len(chain.lotteries),
        "entropy_seed": chain.tip.entropy_seed,
    })


@app.route("/rpc/block/<int:height>")
def rpc_block(height):
    chain = get_chain()
    b = chain.get_block_by_height(height)
    if not b:
        abort(404)
    return jsonify(b.to_dict())


@app.route("/rpc/block_by_hash/<h>")
def rpc_block_by_hash(h):
    chain = get_chain()
    b = chain.get_block_by_hash(h)
    if not b:
        abort(404)
    return jsonify(b.to_dict())


@app.route("/rpc/balance/<addr>")
def rpc_balance(addr):
    chain = get_chain()
    return jsonify(dict(chain.balances.get(addr, {})))


@app.route("/rpc/tokens")
def rpc_tokens():
    chain = get_chain()
    return jsonify({sym: info for sym, info in chain.tokens.items()})


@app.route("/rpc/nft/<collection>/<token_id>")
def rpc_nft(collection, token_id):
    chain = get_chain()
    nft = chain.nfts.get((collection, token_id))
    if not nft:
        abort(404)
    return jsonify(nft)


@app.route("/rpc/lottery/<lid>")
def rpc_lottery(lid):
    chain = get_chain()
    lot = chain.lotteries.get(lid)
    if not lot:
        abort(404)
    return jsonify(lot)


@app.route("/rpc/entropy/<int:height>")
def rpc_entropy(height):
    chain = get_chain()
    b = chain.get_block_by_height(height)
    if not b:
        abort(404)
    return jsonify({"height": height, "seed": b.entropy_seed})


@app.route("/rpc/submit_tx", methods=["POST"])
def rpc_submit_tx():
    """Add a tx to local mempool. Note: this only affects the explorer's
    own copy of the chain; for the tx to actually propagate, your node
    needs to be the one being connected to. In practice you'd POST txs
    to a real running node's RPC, not the explorer's. This endpoint is
    primarily for testing the wire format."""
    chain = get_chain()
    data = request.get_json()
    if not data or "tx" not in data:
        return jsonify({"ok": False, "reason": "missing tx field"}), 400
    try:
        tx = Transaction.from_dict(data["tx"])
    except Exception as e:
        return jsonify({"ok": False, "reason": f"bad tx: {e}"}), 400
    ok, reason = chain.submit_transaction(tx)
    return jsonify({"ok": ok, "reason": reason, "tx_hash": tx.hash()})


# ============================================================
# Entry point
# ============================================================

def main():
    global chain_view
    ap = argparse.ArgumentParser()
    ap.add_argument("--datadir", default="./node1",
                    help="Path to a node's data directory (containing chaindata/)")
    ap.add_argument("--port", type=int, default=8080)
    ap.add_argument("--host", default="127.0.0.1")
    args = ap.parse_args()

    chain_view = ChainView(args.datadir)
    print(f"Explorer reading from {args.datadir}")
    print(f"Open: http://{args.host}:{args.port}/")
    app.run(host=args.host, port=args.port, debug=False)


if __name__ == "__main__":
    main()
