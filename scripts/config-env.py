#!/usr/bin/env python3
"""Shared dotenv adapter. --null emits NUL-separated key/value pairs."""
import argparse
import hashlib
import json
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from stackctl.envio import read_env, update_env, atomic_write


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--file", type=Path, default=Path(__file__).resolve().parents[1] / ".env")
    parser.add_argument("--null", action="store_true")
    parser.add_argument("--set", nargs=2, metavar=("KEY", "VALUE"))
    parser.add_argument("--panel-revision", action="store_true")
    parser.add_argument("--mark-panel-ready", action="store_true")
    args = parser.parse_args()
    if args.set:
        update_env(args.file, {args.set[0]: args.set[1]})
    elif args.panel_revision or args.mark_panel_ready:
        env = read_env(args.file)
        revision = hashlib.sha256(json.dumps([env.get(k, "") for k in ("PANEL_USER", "PANEL_PASS", "PANEL_PATH")]).encode()).hexdigest()
        if args.mark_panel_ready:
            atomic_write(args.file.parent / ".stack" / "panel-ready", revision + "\n")
        else:
            print(revision)
    elif args.null:
        for key, value in read_env(args.file).items():
            sys.stdout.buffer.write(key.encode() + b"\0" + value.encode() + b"\0")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError) as error:
        sys.exit(str(error))
