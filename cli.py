"""
Entropy Chain CLI Wallet.

Talks to a running node's RPC for chain queries and tx submission.
Manages an encrypted wallet locally for signing.

Usage:
    python cli.py [--rpc URL] [--wallet PATH] <command> [args...]

Examples:
    python cli.py info
    python cli.py new-address --label alice
    python cli.py addresses
    python cli.py balance EXYZ...
    python cli.py send EXYZ... 10
    python cli.py create-token CHAOS "Chaos Token" 1000000
    python cli.py mint-nft my-collection 001 EXYZ... --uri ipfs://...
    python cli.py random-mint-nft creatures 042 EXYZ... --entropy-height 100
    python cli.py create-lottery jackpot-1 --prize 50 --ticket 1 --draw-in 10
    python cli.py enter-lottery jackpot-1
    python cli.py draw-lottery jackpot-1

Amounts are entered in whole ENTRO. Internally they're scaled by 10^8 (decimals).
"""

import getpass
import json
import os
import sys
import urllib.error
import urllib.request

import click

from wallet import Wallet, WalletLockedError
from transactions import (
    NATIVE_TOKEN, Transaction,
    make_transfer, make_token_create, make_token_mint,
    make_nft_create, make_nft_mint, make_nft_transfer,
    make_nft_random_mint,
    make_lottery_create, make_lottery_enter, make_lottery_draw,
)


DECIMALS = 8
SCALE = 10 ** DECIMALS


def to_units(whole: float) -> int:
    """Convert a human-readable amount (e.g. 1.5) to integer units."""
    return int(round(float(whole) * SCALE))


def from_units(units: int, decimals: int = DECIMALS) -> str:
    whole = units // (10 ** decimals)
    frac = units % (10 ** decimals)
    if frac == 0:
        return f"{whole:,}"
    s = f"{whole:,}.{frac:0{decimals}d}".rstrip("0").rstrip(".")
    return s


# ============================================================
# RPC client
# ============================================================

class RPC:
    def __init__(self, base_url: str):
        self.base = base_url.rstrip("/")

    def _req(self, path: str, method: str = "GET", body: dict | None = None) -> dict:
        url = f"{self.base}{path}"
        data = None
        headers = {"Accept": "application/json"}
        if body is not None:
            data = json.dumps(body).encode()
            headers["Content-Type"] = "application/json"
        req = urllib.request.Request(url, data=data, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=10) as resp:
                return json.loads(resp.read().decode())
        except urllib.error.HTTPError as e:
            try:
                err_body = json.loads(e.read().decode())
            except Exception:
                err_body = {"error": e.reason}
            err_body["_status"] = e.code
            return err_body
        except urllib.error.URLError as e:
            click.echo(f"Cannot reach node at {self.base}: {e.reason}", err=True)
            sys.exit(2)

    def info(self):
        return self._req("/rpc/info")

    def balance(self, addr: str, token: str | None = None):
        if token:
            return self._req(f"/rpc/balance/{addr}/{token}")
        return self._req(f"/rpc/balance/{addr}")

    def submit(self, tx_dict: dict):
        return self._req("/rpc/submit_tx", method="POST", body={"tx": tx_dict})

    def tokens(self):
        return self._req("/rpc/tokens")

    def nft(self, collection: str, token_id: str):
        return self._req(f"/rpc/nft/{collection}/{token_id}")

    def lottery(self, lid: str):
        return self._req(f"/rpc/lottery/{lid}")

    def entropy(self, height: int):
        return self._req(f"/rpc/entropy/{height}")

    def mempool(self):
        return self._req("/rpc/mempool")


# ============================================================
# Wallet helpers
# ============================================================

def open_wallet(path: str, require_unlock: bool = False) -> Wallet:
    w = Wallet(path)
    if require_unlock and w.is_encrypted and not w.is_unlocked:
        passphrase = os.environ.get("ENTROPY_PASSPHRASE")
        if not passphrase:
            passphrase = getpass.getpass("Wallet passphrase: ")
        if not w.unlock(passphrase):
            click.echo("Wrong passphrase.", err=True)
            sys.exit(1)
    return w


def resolve_sender(w: Wallet, addr_or_label: str | None) -> str:
    """Resolve --from. If None, use first address. If a label, look it up."""
    addrs = w.addresses()
    if not addrs:
        click.echo("Wallet has no addresses. Run `new-address` first.", err=True)
        sys.exit(1)
    if not addr_or_label:
        return addrs[0]
    if addr_or_label in addrs:
        return addr_or_label
    # Try label lookup (requires unlock)
    if not w.is_unlocked:
        click.echo("Need to unlock wallet to resolve label.", err=True)
        sys.exit(1)
    for addr in addrs:
        if w.get_label(addr) == addr_or_label:
            return addr
    click.echo(f"No address or label matching: {addr_or_label}", err=True)
    sys.exit(1)


def submit_or_die(rpc: RPC, tx: Transaction) -> dict:
    """Submit a tx; print result and exit with non-zero on failure."""
    res = rpc.submit(tx.to_dict())
    if res.get("ok"):
        click.echo(f"  ✓ submitted: {res.get('tx_hash')}")
    else:
        click.echo(f"  ✗ rejected: {res.get('reason', res)}", err=True)
        sys.exit(1)
    return res


# ============================================================
# CLI
# ============================================================

DEFAULT_RPC = "http://127.0.0.1:8001"


@click.group(context_settings={"help_option_names": ["-h", "--help"]})
@click.option("--rpc", default=DEFAULT_RPC, envvar="ENTROPY_RPC",
              help="Node RPC URL (env: ENTROPY_RPC)")
@click.option("--wallet", "wallet_path", default="./wallet.json", envvar="ENTROPY_WALLET",
              help="Path to wallet file (env: ENTROPY_WALLET)")
@click.pass_context
def cli(ctx, rpc, wallet_path):
    """Entropy Chain CLI wallet."""
    ctx.ensure_object(dict)
    ctx.obj["rpc"] = RPC(rpc)
    ctx.obj["wallet_path"] = wallet_path


# ---------------- node info ----------------

@cli.command()
@click.pass_context
def info(ctx):
    """Show node + chain status."""
    r = ctx.obj["rpc"].info()
    click.echo(f"Height:        {r['height']}")
    click.echo(f"Tip hash:      {r['tip_hash']}")
    click.echo(f"Difficulty:    {r['difficulty']:,}")
    click.echo(f"Total work:    {r['total_work']:,}")
    click.echo(f"Tokens:        {len(r['tokens'])}  ({', '.join(r['tokens']) or '—'})")
    click.echo(f"NFTs:          {r['nft_count']}")
    click.echo(f"Lotteries:     {r['lottery_count']}")
    click.echo(f"Mempool:       {r['mempool_size']} pending")
    click.echo(f"Peers:         {r['peer_count']}")
    click.echo(f"Latest seed:   {r['entropy_seed']}")


# ---------------- wallet management ----------------

@cli.command(name="new-address")
@click.option("--label", default="", help="Optional label for this address")
@click.option("--encrypt", is_flag=True, help="If wallet is brand new, encrypt with passphrase")
@click.pass_context
def new_address(ctx, label, encrypt):
    """Generate a new keypair."""
    path = ctx.obj["wallet_path"]
    w = Wallet(path)
    passphrase = None
    if encrypt and not w.exists:
        # Brand-new encrypted wallet
        p1 = getpass.getpass("New passphrase: ")
        p2 = getpass.getpass("Confirm passphrase: ")
        if p1 != p2:
            click.echo("Passphrases don't match.", err=True)
            sys.exit(1)
        if len(p1) < 6:
            click.echo("Passphrase must be at least 6 characters.", err=True)
            sys.exit(1)
        passphrase = p1
    elif w.is_encrypted and not w.is_unlocked:
        if not w.unlock(getpass.getpass("Wallet passphrase: ")):
            click.echo("Wrong passphrase.", err=True)
            sys.exit(1)
    addr = w.create_address(label=label, passphrase=passphrase)
    click.echo(f"New address: {addr}")
    if label:
        click.echo(f"Label:       {label}")
    if w.is_encrypted:
        click.echo("Wallet is encrypted at rest.")


@cli.command()
@click.pass_context
def addresses(ctx):
    """List all addresses in the wallet."""
    w = Wallet(ctx.obj["wallet_path"])
    if not w.exists:
        click.echo("No wallet file. Run `new-address` to create one.")
        return
    addrs = w.addresses()
    if not addrs:
        click.echo("Wallet has no addresses.")
        return
    rpc = ctx.obj["rpc"]
    can_show_labels = (not w.is_encrypted) or w.is_unlocked
    for addr in addrs:
        bal = rpc.balance(addr).get(NATIVE_TOKEN, 0)
        label = (w.get_label(addr) if can_show_labels else "(locked)")
        click.echo(f"  {addr}  {from_units(bal):>15} ENTRO  {label}")


@cli.command()
@click.pass_context
def encrypt(ctx):
    """Encrypt an existing plaintext wallet (or change passphrase)."""
    w = Wallet(ctx.obj["wallet_path"])
    if not w.exists:
        click.echo("No wallet to encrypt.", err=True)
        sys.exit(1)
    if w.is_encrypted and not w.is_unlocked:
        if not w.unlock(getpass.getpass("Current passphrase: ")):
            click.echo("Wrong passphrase.", err=True)
            sys.exit(1)
    p1 = getpass.getpass("New passphrase: ")
    p2 = getpass.getpass("Confirm passphrase: ")
    if p1 != p2:
        click.echo("Passphrases don't match.", err=True)
        sys.exit(1)
    w.encrypt(p1)
    click.echo("Wallet encrypted.")


@cli.command(name="export-key")
@click.argument("address")
@click.pass_context
def export_key(ctx, address):
    """Print the private key for an address. BE CAREFUL."""
    if not click.confirm(f"Print private key for {address}? Anyone seeing it can spend"):
        return
    w = open_wallet(ctx.obj["wallet_path"], require_unlock=True)
    click.echo(w.export_private_key(address))


@cli.command(name="import-key")
@click.argument("private_key_hex")
@click.option("--label", default="")
@click.pass_context
def import_key(ctx, private_key_hex, label):
    """Import an existing private key."""
    w = open_wallet(ctx.obj["wallet_path"], require_unlock=True)
    addr = w.import_private_key(private_key_hex, label=label)
    click.echo(f"Imported: {addr}")


# ---------------- balances ----------------

@cli.command()
@click.argument("address")
@click.option("--token", default=None, help="Specific token symbol (default: all)")
@click.pass_context
def balance(ctx, address, token):
    """Show balance(s) for an address."""
    rpc = ctx.obj["rpc"]
    if token:
        r = rpc.balance(address, token)
        decimals = DECIMALS
        if token.upper() != NATIVE_TOKEN:
            t = rpc._req(f"/rpc/token/{token.upper()}")
            decimals = t.get("decimals", DECIMALS)
        click.echo(f"{token.upper()}: {from_units(r.get('balance', 0), decimals)}")
        return
    bals = rpc.balance(address)
    if not bals:
        click.echo("No balances.")
        return
    tokens = rpc.tokens()
    for tok, amt in bals.items():
        if amt == 0:
            continue
        decimals = tokens.get(tok, {}).get("decimals", DECIMALS) if tok != NATIVE_TOKEN else DECIMALS
        click.echo(f"  {tok:>10}  {from_units(amt, decimals)}")


# ---------------- send ENTRO / token ----------------

@cli.command()
@click.argument("recipient")
@click.argument("amount", type=float)
@click.option("--token", default=NATIVE_TOKEN, help="Token symbol (default: ENTRO)")
@click.option("--from", "from_", default=None, help="Sender address or label")
@click.option("--fee", type=int, default=10, help="Fee in smallest units (default: 10)")
@click.pass_context
def send(ctx, recipient, amount, token, from_, fee):
    """Transfer ENTRO or a token."""
    w = open_wallet(ctx.obj["wallet_path"], require_unlock=True)
    sender = resolve_sender(w, from_)
    units = to_units(amount)
    tx = make_transfer(sender, recipient, units, token=token.upper(), fee=fee)
    w.sign_tx(tx, sender)
    click.echo(f"Sending {amount} {token.upper()} from {sender} to {recipient}")
    submit_or_die(ctx.obj["rpc"], tx)


# ---------------- tokens ----------------

@cli.command(name="create-token")
@click.argument("symbol")
@click.argument("name")
@click.argument("supply", type=float)
@click.option("--decimals", type=int, default=DECIMALS)
@click.option("--from", "from_", default=None)
@click.option("--fee", type=int, default=100)
@click.pass_context
def create_token(ctx, symbol, name, supply, decimals, from_, fee):
    """Create a new fungible token. SUPPLY is in whole units."""
    w = open_wallet(ctx.obj["wallet_path"], require_unlock=True)
    sender = resolve_sender(w, from_)
    units = int(round(supply * (10 ** decimals)))
    tx = make_token_create(sender, symbol.upper(), name, total_supply=units,
                           decimals=decimals, fee=fee)
    w.sign_tx(tx, sender)
    click.echo(f"Creating token {symbol.upper()} ({name}) supply={supply:,}")
    submit_or_die(ctx.obj["rpc"], tx)


@cli.command(name="mint-token")
@click.argument("symbol")
@click.argument("recipient")
@click.argument("amount", type=float)
@click.option("--from", "from_", default=None)
@click.option("--fee", type=int, default=10)
@click.pass_context
def mint_token(ctx, symbol, recipient, amount, from_, fee):
    """Mint additional tokens (creator only)."""
    w = open_wallet(ctx.obj["wallet_path"], require_unlock=True)
    sender = resolve_sender(w, from_)
    rpc = ctx.obj["rpc"]
    t = rpc._req(f"/rpc/token/{symbol.upper()}")
    decimals = t.get("decimals", DECIMALS)
    units = int(round(amount * (10 ** decimals)))
    tx = make_token_mint(sender, symbol.upper(), recipient, units, fee=fee)
    w.sign_tx(tx, sender)
    click.echo(f"Minting {amount} {symbol.upper()} -> {recipient}")
    submit_or_die(rpc, tx)


# ---------------- NFTs ----------------

@cli.command(name="create-collection")
@click.argument("collection_id")
@click.argument("name")
@click.option("--description", default="")
@click.option("--from", "from_", default=None)
@click.option("--fee", type=int, default=100)
@click.pass_context
def create_collection(ctx, collection_id, name, description, from_, fee):
    """Create a new NFT collection."""
    w = open_wallet(ctx.obj["wallet_path"], require_unlock=True)
    sender = resolve_sender(w, from_)
    tx = make_nft_create(sender, collection_id, name, description=description, fee=fee)
    w.sign_tx(tx, sender)
    click.echo(f"Creating collection: {collection_id} ({name})")
    submit_or_die(ctx.obj["rpc"], tx)


@cli.command(name="mint-nft")
@click.argument("collection_id")
@click.argument("token_id")
@click.argument("recipient")
@click.option("--uri", default="")
@click.option("--traits", default=None, help="JSON traits dict")
@click.option("--from", "from_", default=None)
@click.option("--fee", type=int, default=5)
@click.pass_context
def mint_nft(ctx, collection_id, token_id, recipient, uri, traits, from_, fee):
    """Mint an NFT with manual (or empty) traits."""
    w = open_wallet(ctx.obj["wallet_path"], require_unlock=True)
    sender = resolve_sender(w, from_)
    traits_dict = json.loads(traits) if traits else {}
    tx = make_nft_mint(sender, collection_id, token_id, recipient,
                       metadata_uri=uri, traits=traits_dict, fee=fee)
    w.sign_tx(tx, sender)
    click.echo(f"Minting {collection_id}#{token_id} -> {recipient}")
    submit_or_die(ctx.obj["rpc"], tx)


@cli.command(name="random-mint-nft")
@click.argument("collection_id")
@click.argument("token_id")
@click.argument("recipient")
@click.option("--entropy-height", type=int, default=None,
              help="Source block for traits (default: current height - 1)")
@click.option("--uri", default="")
@click.option("--from", "from_", default=None)
@click.option("--fee", type=int, default=5)
@click.pass_context
def random_mint_nft(ctx, collection_id, token_id, recipient, entropy_height, uri, from_, fee):
    """Mint an NFT with traits derived from a past block's entropy seed."""
    w = open_wallet(ctx.obj["wallet_path"], require_unlock=True)
    sender = resolve_sender(w, from_)
    rpc = ctx.obj["rpc"]
    if entropy_height is None:
        info = rpc.info()
        entropy_height = info["height"] - 1
        if entropy_height < 1:
            click.echo("Chain has no usable entropy yet (need at least block 1).", err=True)
            sys.exit(1)
    tx = make_nft_random_mint(sender, collection_id, token_id, recipient,
                              entropy_height=entropy_height, metadata_uri=uri, fee=fee)
    w.sign_tx(tx, sender)
    click.echo(f"Random-minting {collection_id}#{token_id} -> {recipient} "
               f"(entropy from block {entropy_height})")
    submit_or_die(rpc, tx)


@cli.command(name="transfer-nft")
@click.argument("collection_id")
@click.argument("token_id")
@click.argument("recipient")
@click.option("--from", "from_", default=None)
@click.option("--fee", type=int, default=1)
@click.pass_context
def transfer_nft(ctx, collection_id, token_id, recipient, from_, fee):
    """Transfer an NFT you own."""
    w = open_wallet(ctx.obj["wallet_path"], require_unlock=True)
    sender = resolve_sender(w, from_)
    tx = make_nft_transfer(sender, collection_id, token_id, recipient, fee=fee)
    w.sign_tx(tx, sender)
    click.echo(f"Transferring {collection_id}#{token_id} -> {recipient}")
    submit_or_die(ctx.obj["rpc"], tx)


# ---------------- lotteries ----------------

@cli.command(name="create-lottery")
@click.argument("lottery_id")
@click.option("--prize", type=float, required=True, help="Prize in ENTRO")
@click.option("--ticket", type=float, required=True, help="Ticket price in ENTRO")
@click.option("--draw-in", type=int, required=True, help="Draw N blocks from now")
@click.option("--from", "from_", default=None)
@click.option("--fee", type=int, default=50)
@click.pass_context
def create_lottery(ctx, lottery_id, prize, ticket, draw_in, from_, fee):
    """Create a new lottery."""
    w = open_wallet(ctx.obj["wallet_path"], require_unlock=True)
    sender = resolve_sender(w, from_)
    rpc = ctx.obj["rpc"]
    info = rpc.info()
    draw_height = info["height"] + draw_in
    tx = make_lottery_create(sender, lottery_id, prize_amount=to_units(prize),
                             ticket_price=to_units(ticket), draw_height=draw_height,
                             fee=fee)
    w.sign_tx(tx, sender)
    click.echo(f"Creating lottery '{lottery_id}': prize={prize} ENTRO, "
               f"ticket={ticket} ENTRO, draw at block {draw_height}")
    submit_or_die(rpc, tx)


@cli.command(name="enter-lottery")
@click.argument("lottery_id")
@click.option("--from", "from_", default=None)
@click.option("--fee", type=int, default=1)
@click.pass_context
def enter_lottery(ctx, lottery_id, from_, fee):
    """Buy a ticket to a lottery."""
    w = open_wallet(ctx.obj["wallet_path"], require_unlock=True)
    sender = resolve_sender(w, from_)
    tx = make_lottery_enter(sender, lottery_id, fee=fee)
    w.sign_tx(tx, sender)
    click.echo(f"Entering lottery '{lottery_id}' as {sender}")
    submit_or_die(ctx.obj["rpc"], tx)


@cli.command(name="draw-lottery")
@click.argument("lottery_id")
@click.option("--from", "from_", default=None)
@click.option("--fee", type=int, default=1)
@click.pass_context
def draw_lottery(ctx, lottery_id, from_, fee):
    """Submit the draw transaction (anyone can do this once draw block is reached)."""
    w = open_wallet(ctx.obj["wallet_path"], require_unlock=True)
    sender = resolve_sender(w, from_)
    tx = make_lottery_draw(sender, lottery_id, fee=fee)
    w.sign_tx(tx, sender)
    click.echo(f"Submitting draw for lottery '{lottery_id}'")
    submit_or_die(ctx.obj["rpc"], tx)


@cli.command(name="show-lottery")
@click.argument("lottery_id")
@click.pass_context
def show_lottery(ctx, lottery_id):
    """Display lottery state."""
    r = ctx.obj["rpc"].lottery(lottery_id)
    if r.get("error"):
        click.echo(f"Lottery not found: {lottery_id}", err=True)
        sys.exit(1)
    click.echo(f"Lottery:        {lottery_id}")
    click.echo(f"Organizer:      {r['organizer']}")
    click.echo(f"Prize:          {from_units(r['prize_amount'])} {r['token']}")
    click.echo(f"Ticket price:   {from_units(r['ticket_price'])} ENTRO")
    click.echo(f"Entry pool:     {from_units(r['entry_pool'])} ENTRO")
    click.echo(f"Draw at height: {r['draw_height']}")
    click.echo(f"Entrants:       {len(r['entrants'])}")
    for e in r["entrants"]:
        marker = " 🏆" if e == r.get("winner") else ""
        click.echo(f"   - {e}{marker}")
    click.echo(f"Drawn:          {r['drawn']}")
    if r.get("winner"):
        click.echo(f"Winner:         {r['winner']}")
        click.echo(f"Entropy used:   {r['entropy_used']}")


# ---------------- inspection helpers ----------------

@cli.command()
@click.pass_context
def mempool(ctx):
    """Show pending transactions in the node's mempool."""
    r = ctx.obj["rpc"].mempool()
    click.echo(f"Mempool: {r['size']} pending")
    for h in r["tx_hashes"]:
        click.echo(f"  {h}")


@cli.command()
@click.argument("collection_id")
@click.argument("token_id")
@click.pass_context
def show_nft(ctx, collection_id, token_id):
    """Display NFT details."""
    r = ctx.obj["rpc"].nft(collection_id, token_id)
    if r.get("error"):
        click.echo("NFT not found.", err=True)
        sys.exit(1)
    click.echo(f"NFT:            {collection_id}#{token_id}")
    click.echo(f"Owner:          {r['owner']}")
    click.echo(f"Creator:        {r['creator']}")
    click.echo(f"Minted at:      block {r['minted_at_height']}")
    click.echo(f"Metadata URI:   {r.get('metadata_uri') or '—'}")
    if r.get("entropy_source_height") is not None:
        click.echo(f"Entropy source: block {r['entropy_source_height']}")
    traits = r.get("traits", {})
    if traits:
        click.echo("Traits:")
        for k, v in traits.items():
            click.echo(f"   {k}: {v}")


if __name__ == "__main__":
    cli()
