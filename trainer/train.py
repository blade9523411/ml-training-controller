#!/usr/bin/env python3
"""Simulated ML trainer with checkpoint support."""

import argparse
import json
import os
import sys
import time
from pathlib import Path

CHECKPOINT_FILE = "checkpoint.json"


def read_checkpoint(checkpoint_dir):
    """Return (last_completed_epoch, last_loss), or (0, None) if no checkpoint."""
    if not checkpoint_dir:
        return 0, None
    path = Path(checkpoint_dir) / CHECKPOINT_FILE
    if not path.exists():
        return 0, None
    data = json.loads(path.read_text())
    epoch = data.get("epoch", 0)
    loss = data.get("loss")
    print(f"[trainer] Resuming from checkpoint: epoch={epoch} loss={loss:.4f}", flush=True)
    return epoch, loss


def write_checkpoint(checkpoint_dir, epoch, loss):
    """Persist a checkpoint so a retry attempt can resume from this epoch."""
    if not checkpoint_dir:
        return
    path = Path(checkpoint_dir) / CHECKPOINT_FILE
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps({"epoch": epoch, "loss": round(loss, 6)}))


def main():
    parser = argparse.ArgumentParser(description="Simulated ML trainer with checkpoint support")
    parser.add_argument("--epochs", type=int, default=5, help="Total number of training epochs")
    parser.add_argument("--lr", type=float, default=0.001, help="Learning rate")
    parser.add_argument("--fail", action="store_true", help="Simulate failure after all epochs")
    parser.add_argument("--fail-at-epoch", type=int, default=0,
                        help="Simulate failure after completing this epoch (0 = no failure)")
    parser.add_argument("--checkpoint-dir", default="",
                        help="Checkpoint directory (overrides CHECKPOINT_DIR env var)")
    args = parser.parse_args()

    # Controller injects CHECKPOINT_DIR; --checkpoint-dir is for local manual testing.
    checkpoint_dir = args.checkpoint_dir or os.environ.get("CHECKPOINT_DIR", "")
    should_fail = args.fail or os.environ.get("FAIL_TRAINING", "").strip() in ("1", "true", "yes")

    print(
        f"[trainer] Starting: epochs={args.epochs} lr={args.lr} "
        f"checkpoint_dir={checkpoint_dir!r}",
        flush=True,
    )

    start_epoch, _ = read_checkpoint(checkpoint_dir)

    if start_epoch >= args.epochs:
        print(f"[trainer] All {args.epochs} epochs already done in a prior run", flush=True)
        print("[trainer] Training complete", flush=True)
        return

    for epoch in range(start_epoch + 1, args.epochs + 1):
        time.sleep(1)
        loss = 1.0 / epoch
        acc = 1.0 - loss

        # Write checkpoint before any potential failure so a retry can resume here.
        write_checkpoint(checkpoint_dir, epoch, loss)

        print(f"[trainer] Epoch {epoch}/{args.epochs}  loss={loss:.4f}  acc={acc:.4f}", flush=True)

        if args.fail_at_epoch > 0 and epoch == args.fail_at_epoch:
            print(
                f"[trainer] ERROR: simulated failure at epoch {epoch}",
                file=sys.stderr,
                flush=True,
            )
            sys.exit(1)

    if should_fail:
        print("[trainer] ERROR: simulated training failure", file=sys.stderr, flush=True)
        sys.exit(1)

    print("[trainer] Training complete", flush=True)


if __name__ == "__main__":
    main()
