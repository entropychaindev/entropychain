"""
Genesis configuration for THIS Entropy Chain network.

These constants define the launch identity. Two nodes with different
genesis configs will produce different genesis hashes and refuse to peer.

If you fork the codebase to launch your own Entropy Chain network:
  1. Change GENESIS_MESSAGE
  2. Change GENESIS_TIMESTAMP to your launch date
  3. Change NETWORK_ID in protocol.py to match
  4. Wipe all chaindata directories and start fresh

Do NOT change these values on a running network. The genesis block
is computed deterministically from these constants and any change
will fork your network from itself.
"""

import time

# Human-readable identity baked into the genesis coinbase tx data.
# Becomes part of the merkle root, which becomes part of the genesis hash.
GENESIS_MESSAGE = "EntropyChain testnet-4 — beacon v2 — 133.7M supply · Oct 2026"

# Launch date as a UNIX timestamp. Oct 10 2026 00:00:00 UTC.
# (testnet-2 used 1759449600 = Oct 3 *2025* by mistake, which made the
# first difficulty retarget see a one-year window and cut difficulty 4x.)
# Same instant for everyone who runs this code, anywhere on Earth.
GENESIS_TIMESTAMP = 1791590400.0

# A salt that future operators can use to distinguish testnet generations.
# When this testnet eventually retires, bump this (e.g. "testnet-4") and the
# next testnet's genesis will be different even with the same message.
GENESIS_NETWORK_TAG = "testnet-4"

# Operator's public identifier — purely informational, just shows up in
# the genesis coinbase data. Does not control the chain.
GENESIS_OPERATOR = "ZZBolt / @UnionOfPlanets"
