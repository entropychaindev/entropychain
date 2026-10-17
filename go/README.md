# Entropy Chain — Go node

A Go implementation of the Entropy Chain full node, miner and wallet CLI.
It is **wire-, hash- and API-compatible** with the Python reference node in
the parent directory: the two can run side by side on the same network,
sync each other's blocks, and relay each other's transactions.

```
entropyd            full node: P2P (websocket), JSON RPC, multi-threaded miner
entropy             wallet CLI (port of cli.py): keys, send, tokens, NFTs, lotteries
EntropyChainMiner   desktop miner + wallet (Wails), replaces the PySide6 app
```

**Network:** testnet-4 (genesis `4af17f69…`, not yet launched; the live public network is testnet-3). The public testnet runs with
the relaxed Argon2 cost, so seed nodes must use `--testnet-fast`, and the
desktop app enables it automatically.

## Build

Requires Go 1.23+.

```bash
cd go
go build -o bin/ ./cmd/...
```

Cross-compile release binaries (single static files, no runtime needed):

```bash
GOOS=windows GOARCH=amd64 go build -ldflags "-s -w -X main.version=v0.1.0" -o bin/entropyd.exe ./cmd/entropyd
GOOS=linux   GOARCH=amd64 go build -ldflags "-s -w -X main.version=v0.1.0" -o bin/entropyd ./cmd/entropyd
```

## Run

Flags mirror `node.py`:

```bash
bin/entropyd --port 9001 --datadir ./node1 --mine --threads 4
bin/entropyd --port 9002 --datadir ./node2 --seeds 127.0.0.1:9001
```

| Flag | Default | |
|---|---|---|
| `--port` | 9001 | P2P websocket port |
| `--datadir` | `./node_data` | holds `chaindata/chain.db` and `wallet.json` |
| `--seeds` | | `host:port,host:port` |
| `--mine` / `--threads` | off / 1 | each thread uses 16 MB per hash |
| `--miner-address` | first wallet address | |
| `--rpc-port` / `--rpc-host` | 8001 / 127.0.0.1 | `--rpc-port 0` disables |
| `--rpc-cors` | | e.g. `"*"` to let a browser wallet call the RPC directly |
| `--import-python` | | migrate a Python `chain.json` (fully re-validated) |
| `--testnet-fast` | | 1 MB Argon2 for local dev (same as the Python flag) |

The wallet file format is the same as `wallet.py` (plaintext or
PBKDF2 + AES-GCM encrypted), so existing wallets work as-is.

### Migrating a Python node

```bash
bin/entropyd --datadir ./node1 --import-python ../node1/chaindata/chain.json
```

Every block is re-validated (PoW, signatures, state) under the Go rules.

### CLI

```bash
bin/entropy new-address --label main
bin/entropy balance <address>
bin/entropy send <recipient> 12.5 --fee 10
bin/entropy --rpc https://rpc.entropychain.io info
```

`ENTROPY_RPC`, `ENTROPY_WALLET` and `ENTROPY_PASSPHRASE` are honoured, as in `cli.py`.

## Desktop miner (Wails)

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.10.2   # once
cd desktop
wails build -clean -ldflags "-s -w"   # -> desktop/build/bin/EntropyChainMiner.exe (~12 MB)
wails dev                             # live development; UI also at http://localhost:34115
```

The app embeds a full node, so the wallet tab reads local chain state rather
than calling the public RPC. It uses the same data folder as the old Python
miner (`%LOCALAPPDATA%\EntropyChain\data`), so existing wallets carry over.
The UI is plain HTML/CSS/JS in `desktop/frontend/dist` with no npm build step.

For testing, `ENTROPYCHAIN_DATADIR` overrides the data folder and
`ENTROPYCHAIN_SEEDS=host:port,...` overrides the seed list.

## RPC

All `rpc_server.py` endpoints, with the same response shapes:
`/rpc/info`, `/rpc/balance/<addr>[/<token>]`, `/rpc/tokens`, `/rpc/token/<sym>`,
`/rpc/nft/<col>/<id>`, `/rpc/nfts/<owner>`, `/rpc/collections`,
`/rpc/lottery/<id>`, `/rpc/entropy/<height>`, `/rpc/mempool`,
`/rpc/history/<addr>`, `POST /rpc/submit_tx`.

Added: `/rpc/block/<height>`, `/rpc/block_by_hash/<hash>`, `/rpc/peers`.

## Compatibility

Transaction hashes are SHA-256d of Python's `json.dumps(sort_keys=True)`
output, so `internal/pyjson` reproduces that byte for byte (separators,
`ensure_ascii` escaping, Python float `repr`, int vs float). The test suite
checks the Go code against vectors produced by the Python code itself:

```bash
python testdata/gen_vectors.py   # regenerate after changing Python consensus code
go test ./...
```

## Rule changes vs. the Python node

The Go node fixes consensus bugs in the Python node. Every change **only
rejects** things Python would accept, so a Go node follows any Python chain
that never exercised these bugs (the import path verifies that).

- Transactions in a block are validated **in order** against the state the
  previous ones left. Python checked all of them against the pre-block state,
  so two spends in one block could overdraw an account.
- A transaction that is already on chain cannot be included again (replay).
- Amounts, prices, heights and fees must be JSON integers within int64;
  fees cannot be negative; balance arithmetic is overflow-checked.
- Fields Python reads unconditionally while applying (and would crash on) are
  required: token `name`, lottery `token`/`ticket_price`/`draw_height`.
- `token_mint` recipients must be valid addresses; token symbols must be ASCII.
- A lottery draw must be in a block **after** `draw_height`, so the miner of
  the draw block cannot grind its entropy to choose the winner.
- Lotteries in a non-native token must have `ticket_price` 0 (Python paid the
  ENTRO ticket pool out as token units).
- Coinbase must pay a valid address, carry the block height, and have no fee.
- Block timestamps must exceed the median of the last 11 blocks, and live
  blocks may be at most 2 hours ahead of local time.
- Block hex fields must be canonical lowercase with exact lengths.
- (testnet-3, in both implementations) The first difficulty window does not
  retarget, because it would measure from the fixed genesis timestamp.

### Beacon v2 (testnet-4)

The header's `entropy_seed` = SHA256d(prev_hash ‖ miner_nonce) can be chosen
for free by the block's miner, because the miner nonce is picked before the
proof of work. From testnet-4 the randomness that transactions consume is the
**beacon** = SHA256d("entropy" ‖ block hash), which covers the PoW nonce and
so cannot be known before the block is solved.

- `lottery_draw` picks the winner from the beacon of the draw block, or from
  the aggregate of `draw_blocks` (1–16) consecutive beacons; the draw is only
  valid after the whole window is mined.
- `nft_random_mint` no longer names an `entropy_height` (rejected if present);
  traits come from the beacon of the block after the one that includes it,
  and read as `traits_pending` until that block exists.
- `/rpc/entropy/<h>` returns the beacon as `seed` and the legacy field as
  `header_seed`.

The Python node does not implement beacon v2 and refuses to start.

**Operational note:** while Python miners remain on the network, a Python
miner could still produce a block that exploits one of these bugs; Go nodes
would reject it and the network would split. Move miners and seed nodes to
Go before relying on the chain.

## Layout

```
cmd/entropyd        node binary
cmd/entropy         wallet CLI
internal/params     network identity and consensus constants
internal/pyjson     Python-compatible canonical JSON
internal/crypto     secp256k1, addresses, Base58Check
internal/consensus  Argon2id PoW, difficulty, entropy, rewards
internal/core       transactions and blocks
internal/chain      state machine, validation, reorgs, mempool
internal/store      bbolt persistence (atomic, crash-safe)
internal/miner      candidate assembly and multi-threaded search
internal/p2p        websocket gossip, sync, orphan handling, banning
internal/rpc        HTTP JSON API and client
internal/wallet     wallet.json (plaintext / encrypted)
internal/compat     cross-implementation tests against testdata/vectors.json
desktop/            Wails desktop miner (app.go backend, frontend/dist UI)
```
