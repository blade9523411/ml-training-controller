#!/usr/bin/env python3
"""Simulated ML trainer for demo and testing."""

import argparse
import os
import sys
import time


def main():
    parser = argparse.ArgumentParser(description="Simulated ML trainer")
    parser.add_argument("--epochs", type=int, default=5, help="Number of training epochs")
    parser.add_argument("--lr", type=float, default=0.001, help="Learning rate")
    parser.add_argument("--fail", action="store_true", help="Simulate a training failure")
    args = parser.parse_args()

    # Support failure via flag or environment variable.
    should_fail = args.fail or os.environ.get("FAIL_TRAINING", "").strip() in ("1", "true", "yes")

    print(f"[trainer] Starting: epochs={args.epochs} lr={args.lr} fail={should_fail}", flush=True)

    for epoch in range(1, args.epochs + 1):
        time.sleep(1)
        loss = 1.0 / epoch
        acc = 1.0 - loss
        print(f"[trainer] Epoch {epoch}/{args.epochs}  loss={loss:.4f}  acc={acc:.4f}", flush=True)

    if should_fail:
        print("[trainer] ERROR: simulated training failure", file=sys.stderr, flush=True)
        sys.exit(1)

    print("[trainer] Training complete", flush=True)


if __name__ == "__main__":
    main()
