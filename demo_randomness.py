"""
Demo for native randomness-consuming transactions:
  - Lottery (3-stage flow: create -> enter -> draw)
  - NFT random mint (traits derived from a past block's entropy)

This is the thing that makes Entropy Chain actually live up to its name.
The randomness beacon isn't a feature people have to opt into via
oracle - it's just there, in every block, free for anyone to consume.

Run: python demo_randomness.py
"""

import os
import shutil
import consensus

# Same fast settings as the main demo
consensus.ARGON2_MEMORY_KB = 1024
consensus.GENESIS_DIFFICULTY = 100

from wallet import Wallet
from blockchain import EntropyChain, pick_winner_from_seed, derive_random_traits
from miner import Miner
from transactions import (
    make_transfer, make_nft_create,
    make_lottery_create, make_lottery_enter, make_lottery_draw,
    make_nft_random_mint,
    NATIVE_TOKEN,
)


def divider(title: str):
    print("\n" + "=" * 64)
    print(f"  {title}")
    print("=" * 64)


def mine_one(miner, chain):
    block = miner.mine(max_attempts=200_000, log_every=100_000)
    assert block is not None
    ok, reason = chain.add_block(block)
    assert ok, f"block rejected: {reason}"
    return block


def main():
    for p in ["./chaindata", "./wallet.json"]:
        if os.path.isdir(p):
            shutil.rmtree(p)
        elif os.path.isfile(p):
            os.remove(p)

    divider("ENTROPY CHAIN  -  Randomness-Consuming Transactions")

    chain = EntropyChain()
    wallet = Wallet()

    organizer = wallet.create_address("organizer")
    alice = wallet.create_address("alice")
    bob = wallet.create_address("bob")
    carol = wallet.create_address("carol")
    dave = wallet.create_address("dave")
    artist = wallet.create_address("artist")

    print(f"Organizer: {organizer}")
    print(f"Alice:     {alice}")
    print(f"Bob:       {bob}")
    print(f"Carol:     {carol}")
    print(f"Dave:      {dave}")
    print(f"Artist:    {artist}")

    miner = Miner(chain, organizer)

    # --- Bootstrap: organizer mines blocks to fund themselves ---
    divider("Bootstrap: organizer mines 4 blocks for funding")
    for _ in range(4):
        mine_one(miner, chain)
    print(f"Organizer balance: {chain.get_balance(organizer) / 10**8:.2f} ENTRO")

    # Send everyone some seed money so they can pay tx fees + ticket prices
    divider("Distribute starting funds (10 ENTRO each)")
    for who in [alice, bob, carol, dave, artist]:
        tx = make_transfer(organizer, who, amount=10 * 10**8, fee=1)
        wallet.sign_tx(tx, organizer)
        chain.submit_transaction(tx)
    mine_one(miner, chain)
    for label, addr in [("Alice", alice), ("Bob", bob), ("Carol", carol),
                        ("Dave", dave), ("Artist", artist)]:
        print(f"  {label}: {chain.get_balance(addr) / 10**8:.2f} ENTRO")

    # ---------------- LOTTERY ----------------
    divider("Lottery: 'jackpot-001' with 50 ENTRO prize, 1 ENTRO tickets")
    draw_height = chain.height + 5  # draw 5 blocks from now
    print(f"Current height: {chain.height}, draw at height: {draw_height}")

    tx = make_lottery_create(
        organizer, "jackpot-001",
        prize_amount=50 * 10**8,
        ticket_price=1 * 10**8,
        draw_height=draw_height,
    )
    wallet.sign_tx(tx, organizer)
    ok, reason = chain.submit_transaction(tx)
    print(f"Create lottery: {reason}")
    mine_one(miner, chain)

    lot = chain.lotteries["jackpot-001"]
    print(f"Prize locked: {lot['prize_amount'] / 10**8:.2f} ENTRO")
    print(f"Organizer balance after lock: {chain.get_balance(organizer) / 10**8:.2f}")

    # Four players enter
    divider("Players enter the lottery")
    for label, addr in [("Alice", alice), ("Bob", bob),
                        ("Carol", carol), ("Dave", dave)]:
        tx = make_lottery_enter(addr, "jackpot-001")
        wallet.sign_tx(tx, addr)
        ok, reason = chain.submit_transaction(tx)
        print(f"  {label} enters: {reason}")
    mine_one(miner, chain)

    lot = chain.lotteries["jackpot-001"]
    print(f"Entrants: {len(lot['entrants'])}")
    print(f"Entry pool: {lot['entry_pool'] / 10**8:.2f} ENTRO")

    # Mine until we reach draw_height
    while chain.height < draw_height:
        mine_one(miner, chain)

    print(f"\nDraw block reached: height {chain.height}")
    draw_block = chain.blocks[draw_height]
    print(f"Draw block entropy_seed: {draw_block.entropy_seed}")

    # Pre-compute who SHOULD win (anyone can do this independently!)
    expected_winner = pick_winner_from_seed(
        draw_block.entropy_seed, "jackpot-001", lot["entrants"]
    )
    label_for = {alice: "Alice", bob: "Bob", carol: "Carol", dave: "Dave"}
    print(f"Predicted winner (verifiable by anyone): {label_for[expected_winner]}")

    # Anyone can submit the draw - here Alice does it
    divider("Submit the draw transaction")
    tx = make_lottery_draw(alice, "jackpot-001")
    wallet.sign_tx(tx, alice)
    chain.submit_transaction(tx)
    mine_one(miner, chain)

    lot = chain.lotteries["jackpot-001"]
    print(f"Drawn:  {lot['drawn']}")
    print(f"Winner: {label_for[lot['winner']]}")
    print(f"Entropy used: {lot['entropy_used']}")
    print()
    for label, addr in [("Alice", alice), ("Bob", bob),
                        ("Carol", carol), ("Dave", dave)]:
        print(f"  {label} balance: {chain.get_balance(addr) / 10**8:.2f} ENTRO")

    # ---------------- RANDOM NFT MINT ----------------
    divider("Random NFT mints - traits seeded by past block entropy")

    # Artist creates a collection
    tx = make_nft_create(artist, "void-creatures",
                         "Void Creatures", "Beasts from the entropy void")
    wallet.sign_tx(tx, artist)
    chain.submit_transaction(tx)
    mine_one(miner, chain)

    # Mint 5 random NFTs, each referencing the same recent block's entropy.
    # They get distinct traits because traits are salted with token_id.
    entropy_height = chain.height - 1  # just-mined block's predecessor
    recipients = [alice, bob, carol, dave, organizer]

    for i, recipient in enumerate(recipients, 1):
        token_id = f"creature-{i:03d}"
        tx = make_nft_random_mint(
            artist, "void-creatures", token_id, recipient,
            entropy_height=entropy_height,
            metadata_uri=f"ipfs://void-creatures/{token_id}",
        )
        wallet.sign_tx(tx, artist)
        ok, reason = chain.submit_transaction(tx)
        print(f"  Mint {token_id} -> {label_for.get(recipient, 'Organizer')}: {reason}")

    mine_one(miner, chain)

    print("\nGenerated NFTs and their (verifiable!) random traits:")
    for i in range(1, 6):
        tid = f"creature-{i:03d}"
        nft = chain.nfts[("void-creatures", tid)]
        traits = nft["traits"]
        legendary = " ⭐LEGENDARY" if traits["is_legendary"] else ""
        print(f"  {tid}: rarity={traits['rarity']:>3}  power={traits['power']:>3}  "
              f"element={traits['element']:>5}  color=#{traits['color_seed']}{legendary}")

    # ---------------- FINAL ----------------
    divider("Summary")
    print(f"Chain height:           {chain.height}")
    print(f"Total blocks:           {len(chain.blocks)}")
    print(f"Lotteries run:          {len(chain.lotteries)}")
    print(f"NFTs minted (random):   "
          f"{sum(1 for nft in chain.nfts.values() if 'entropy_source_height' in nft)}")
    print(f"\nThe lottery winner and every NFT's traits are deterministic")
    print(f"functions of block entropy seeds that were committed BEFORE")
    print(f"any of these transactions existed. No oracle required.")


if __name__ == "__main__":
    main()
