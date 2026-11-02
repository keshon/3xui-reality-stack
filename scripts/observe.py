#!/usr/bin/env python3
"""Record diagnostics and authenticated probes; never restart services."""
import fcntl
import json

from diagnostics import ROOT, collect, save
from panel import env_file
from stackctl.health import measure


def main():
    directory = ROOT / ".stack"
    directory.mkdir(mode=0o700, exist_ok=True)
    with (directory / "observe.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            print("observer already running")
            return 0
        snapshot = collect()
        # Save pre-probe evidence even if the probe is killed or interrupted.
        target = save(snapshot)
        health = measure(ROOT, env_file())
        snapshot.update(health)
        ok = health["ok"]
        # Replace our own protected snapshot; retain one file per observation.
        from stackctl.envio import atomic_write
        atomic_write(target, json.dumps(snapshot, indent=2))
        print(json.dumps({"ok": ok, "snapshot": str(target), "probe": snapshot["probe"]}))
        return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
