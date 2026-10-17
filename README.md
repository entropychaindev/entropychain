# Entropy Chain ($ENTRO)

A CPU-mineable blockchain that produces verifiable randomness as a side product.

## What makes it different

Most "new" blockchains are EVM clones with a different name. Entropy Chain is built on three opinions:

1. **Proof-of-Entropy consensus.** Mining uses Argon2id, the memory-hard winner of the Password Hashing Competition. Each attempt requires ~16 MB of working memory, which kills GPU parallelism (a GPU has thousands of cores but limited per-core memory) and is expensive to ASIC. Pure CPU mining for the foreseeable future.

2. **Every block contributes verifiable randomness.** Each block carries an `entropy_seed` deterministically derived from `prev_hash + miner_nonce`. Other nodes verify the construction, so miners can't lie about it. Native transactions can consume past entropy seeds for things like NFT trait randomness, lotteries, or fair selection. The chain produces randomness-as-a-service.

3. **No general-purpose VM.** Tokens, NFTs, and transfers are native typed transactions. No Solidity to write, no reentrancy bugs, no surprise gas costs. Faster, simpler to audit, easier to validate.

## Architecture

```
crypto_utils.py    secp256k1 keys, addresses (Base58Check), signatures
consensus.py       Argon2id PoW, difficulty retargeting, entropy derivation
transactions.py    Six native tx types: transfer, token create/mint, NFT create/mint/transfer
block.py           Block structure, merkle root
blockchain.py      State machine, validation, persistence
miner.py           Block assembly + PoW search
wallet.py          Keypair management
demo.py            End-to-end walkthrough
```

## Native transaction types

| Type | Purpose |
|------|---------|
| `transfer` | Move ENTRO or any token between addresses |
| `token_create` | Mint a new fungible token (ENTRO-20 style) |
| `token_mint` | Issue more of a token you control |
| `nft_create` | Register a new NFT collection |
| `nft_mint` | Mint an NFT in a collection you control, with traits |
| `nft_transfer` | Move an NFT |

## Quickstart

```bash
pip install argon2-cffi ecdsa base58
python demo.py
```

The demo creates two wallets, mines blocks, transfers ENTRO, creates a fungible token (CHAOS), spins up an NFT collection, and mints an NFT whose traits are seeded by the block's entropy beacon.

## Tuning

In `consensus.py`:
- `ARGON2_MEMORY_KB` — memory cost per PoW attempt. Higher = more GPU/ASIC resistance, slower mining. Default 16 MB. Demo uses 1 MB so it runs fast.
- `TARGET_BLOCK_TIME` — desired seconds between blocks (default 30).
- `DIFFICULTY_WINDOW` — blocks between difficulty retargets (default 20).

In `blockchain.py`:
- `INITIAL_REWARD` — block subsidy (default 50 ENTRO with 8 decimals).
- `HALVING_INTERVAL` — blocks between halvings (default 100,000).

## What's not built yet (Phase 2)

- **P2P networking.** Right now this is a single-node chain. Real consensus needs gossip, peer discovery, and chain reorg handling. A simple approach: libp2p or just websockets between peers, longest-chain rule with the existing PoW.
- **Block explorer / wallet UI.** A small Flask + Jinja app over the JSON state would be straightforward.
- **JSON-RPC API.** For dApps and external integrations.
- **Storage upgrade.** SQLite or LevelDB instead of one big chain.json file.
- **Native lottery / random-pick transactions** that explicitly consume the entropy beacon. The seeds are already there; it's just one more typed transaction.

## Why this is honest about itself

This is a working reference implementation. It will not match Bitcoin or Ethereum on throughput, security under adversarial conditions, or peer-reviewed cryptographic depth. What it does: prove out the consensus design, show the native-primitives model, and give you something runnable and modifiable.
