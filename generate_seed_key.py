"""
Pre-generate a mining key for a public seed node BEFORE deploying.

Why this matters:
  When you SSH into a fresh droplet and start the node, it generates a
  random mining wallet. If you lose that wallet.json, you lose all the
  ENTRO that wallet ever earned. By generating it on your laptop first
  and copying it to the droplet, you can:
    1. Back it up securely on your machine before it ever sees the cloud
    2. Publicly document the address as "the official seed node address"
       so users can verify they're connecting to the real seed
    3. Recover the funds if the droplet dies

Usage:
    python generate_seed_key.py --output ./seed-node-1.wallet.json --label "seed-1"

The output is a plaintext wallet.json. You then:
  1. Back it up encrypted somewhere safe (1Password, encrypted USB, etc.)
  2. scp it to the droplet as ./data/wallet.json
  3. Note the address and publish it

For extra safety, you can encrypt the wallet for transit and decrypt
on the droplet with the CLI's `encrypt` command, but the systemd service
currently requires a plaintext wallet at startup.
"""

import argparse
import json
import os
import sys

from crypto_utils import generate_keypair, pubkey_to_address


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--output", required=True, help="Path to write the wallet file")
    ap.add_argument("--label", default="seed-node", help="Address label")
    args = ap.parse_args()

    if os.path.exists(args.output):
        print(f"Refusing to overwrite existing file: {args.output}", file=sys.stderr)
        sys.exit(1)

    priv, pub = generate_keypair()
    addr = pubkey_to_address(pub)

    wallet = {
        addr: {
            "private_key": priv.hex(),
            "public_key": pub.hex(),
            "label": args.label,
        }
    }

    os.makedirs(os.path.dirname(os.path.abspath(args.output)) or ".", exist_ok=True)
    # Restrict permissions immediately
    fd = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump(wallet, f, indent=2)

    print(f"Generated mining key for seed node.")
    print(f"  Address:  {addr}")
    print(f"  Label:    {args.label}")
    print(f"  File:     {args.output}  (mode 600)")
    print()
    print("Next steps:")
    print(f"  1. BACKUP this file somewhere encrypted and offline")
    print(f"  2. scp {args.output} entropy@<droplet-ip>:~/data/wallet.json")
    print(f"  3. Document this address publicly as your seed node identifier")


if __name__ == "__main__":
    main()
