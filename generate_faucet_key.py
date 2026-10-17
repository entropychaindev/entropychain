"""
Pre-generate a wallet for the public faucet.

The faucet runs unattended and signs transfer transactions on its own,
so it needs a plaintext wallet at startup. This script:

  1. Generates a fresh keypair on YOUR LAPTOP (not the cloud)
  2. Writes a plaintext wallet.json with mode 600
  3. Tells you the address so you know where to send the initial fund

You then:
  1. Back up the file somewhere encrypted (1Password, encrypted USB, etc.)
  2. scp it to the droplet at ~/data/faucet-wallet.json
  3. Send the faucet address some ENTRO from your laptop's mining wallet
     so it has something to drip
  4. Start the faucet service

Usage:
    python generate_faucet_key.py --output ./faucet-wallet.json --label "faucet-1"
"""

import argparse
import json
import os
import sys

from crypto_utils import generate_keypair, pubkey_to_address


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--output", required=True, help="Path to write the wallet file")
    ap.add_argument("--label", default="faucet", help="Address label")
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
    fd = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as f:
        json.dump(wallet, f, indent=2)

    print(f"Generated faucet wallet.")
    print(f"  Address:  {addr}")
    print(f"  Label:    {args.label}")
    print(f"  File:     {args.output}  (mode 600)")
    print()
    print("Next steps:")
    print(f"  1. BACKUP this file somewhere encrypted and offline")
    print(f"  2. scp {args.output} entropy@<droplet-ip>:~/data/faucet-wallet.json")
    print(f"  3. Send some ENTRO to {addr} so the faucet has funds")
    print(f"     Suggested: 50000 ENTRO (50 drips at 1000 each)")
    print(f"     Use: python cli.py --wallet ./node1/wallet.json send {addr} 50000")
    print(f"  4. Start the faucet service")


if __name__ == "__main__":
    main()
