"""
End-to-end demo of Entropy Chain.

Walks through:
  1. Create two wallets (Alice and Bob)
  2. Mine a few blocks for Alice (she gets the rewards)
  3. Alice transfers some ENTRO to Bob
  4. Alice creates a token "CHAOS"
  5. Alice mints a custom NFT collection and gives one to Bob
  6. Show final state

Run with: python demo.py
"""

import os
import shutil
import consensus

# Lower memory cost for the demo so it actually finishes in this
# sandbox in reasonable time. In production you'd leave this at 16+ MB.
consensus.ARGON2_MEMORY_KB = 1024  # 1 MB for demo
consensus.GENESIS_DIFFICULTY = 100  # very easy for fast demo

from wallet import Wallet
from blockchain import EntropyChain
from miner import Miner
from transactions import (
    make_transfer, make_token_create, make_token_mint,
    make_nft_create, make_nft_mint, make_nft_transfer,
    NATIVE_TOKEN,
)


def divider(title: str):
    print("\n" + "=" * 60)
    print(f"  {title}")
    print("=" * 60)


def main():
    # Wipe any prior state so demo is reproducible
    for p in ["./chaindata", "./wallet.json"]:
        if os.path.isdir(p):
            shutil.rmtree(p)
        elif os.path.isfile(p):
            os.remove(p)

    divider("ENTROPY CHAIN  -  CPU-mineable, randomness-producing blockchain")

    chain = EntropyChain()
    wallet = Wallet()

    alice = wallet.create_address("alice")
    bob = wallet.create_address("bob")
    print(f"Alice: {alice}")
    print(f"Bob:   {bob}")

    miner = Miner(chain, alice)

    # --- Mine 3 blocks ---
    divider("Mining 3 blocks (Alice is the miner)")
    for _ in range(3):
        block = miner.mine(max_attempts=200_000)
        assert block is not None, "mining failed"
        ok, reason = chain.add_block(block)
        assert ok, f"block rejected: {reason}"
        print(f"   entropy_seed contributed: {block.entropy_seed[:32]}...")

    print(f"\nAlice's ENTRO balance: {chain.get_balance(alice) / 10**8:.2f}")
    print(f"Bob's ENTRO balance:   {chain.get_balance(bob) / 10**8:.2f}")

    # --- Alice sends Bob 10 ENTRO ---
    divider("Alice sends Bob 10 ENTRO")
    tx = make_transfer(alice, bob, amount=10 * 10**8, fee=10)
    wallet.sign_tx(tx, alice)
    ok, reason = chain.submit_transaction(tx)
    print(f"submit transfer: {reason}")

    block = miner.mine(max_attempts=200_000)
    chain.add_block(block)
    print(f"Alice: {chain.get_balance(alice) / 10**8:.2f} ENTRO")
    print(f"Bob:   {chain.get_balance(bob) / 10**8:.2f} ENTRO")

    # --- Alice creates a fungible token ---
    divider("Alice creates token CHAOS (1,000,000 supply)")
    tx = make_token_create(alice, "CHAOS", "Chaos Token",
                           total_supply=1_000_000 * 10**8)
    wallet.sign_tx(tx, alice)
    chain.submit_transaction(tx)
    block = miner.mine(max_attempts=200_000)
    chain.add_block(block)
    print(f"CHAOS registered: {chain.tokens.get('CHAOS')}")
    print(f"Alice CHAOS balance: {chain.get_balance(alice, 'CHAOS') / 10**8:,.0f}")

    # --- Alice sends Bob some CHAOS ---
    divider("Alice sends Bob 500 CHAOS")
    tx = make_transfer(alice, bob, amount=500 * 10**8, token="CHAOS")
    wallet.sign_tx(tx, alice)
    chain.submit_transaction(tx)
    block = miner.mine(max_attempts=200_000)
    chain.add_block(block)
    print(f"Alice CHAOS: {chain.get_balance(alice, 'CHAOS') / 10**8:,.0f}")
    print(f"Bob CHAOS:   {chain.get_balance(bob, 'CHAOS') / 10**8:,.0f}")

    # --- NFT collection + mint ---
    divider("Alice creates NFT collection 'EntropyArt' and mints #1 to Bob")
    tx = make_nft_create(alice, "entropy-art", "Entropy Art",
                        "Generative art seeded by block entropy")
    wallet.sign_tx(tx, alice)
    chain.submit_transaction(tx)
    block = miner.mine(max_attempts=200_000)
    chain.add_block(block)

    # Use the latest block's entropy seed as a trait seed - real use of
    # the randomness beacon!
    seed = chain.tip.entropy_seed
    traits = {
        "rarity": int(seed[:4], 16) % 100,
        "color_seed": seed[4:10],
        "generation": 1,
    }
    tx2 = make_nft_mint(alice, "entropy-art", "001", bob,
                       metadata_uri="ipfs://example/001.json",
                       traits=traits)
    wallet.sign_tx(tx2, alice)
    chain.submit_transaction(tx2)
    block = miner.mine(max_attempts=200_000)
    chain.add_block(block)
    print(f"Collection: {chain.nft_collections.get('entropy-art')}")
    print(f"NFT #001:   {chain.nfts.get(('entropy-art', '001'))}")

    # --- Final state ---
    divider("Final state")
    print(f"Chain height:        {chain.height}")
    print(f"Total blocks:        {len(chain.blocks)}")
    print(f"Tokens registered:   {list(chain.tokens.keys())}")
    print(f"NFT collections:     {list(chain.nft_collections.keys())}")
    print(f"NFTs minted:         {len(chain.nfts)}")
    print(f"Alice ENTRO:         {chain.get_balance(alice) / 10**8:,.2f}")
    print(f"Bob ENTRO:           {chain.get_balance(bob) / 10**8:,.2f}")
    print(f"Alice CHAOS:         {chain.get_balance(alice, 'CHAOS') / 10**8:,.2f}")
    print(f"Bob CHAOS:           {chain.get_balance(bob, 'CHAOS') / 10**8:,.2f}")
    print(f"\nLatest entropy seed (public randomness): {chain.tip.entropy_seed}")


if __name__ == "__main__":
    main()
