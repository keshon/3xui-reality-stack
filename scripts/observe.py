#!/usr/bin/env python3
"""Record diagnostics and authenticated probes; never restart services."""
import fcntl
import json
from pathlib import Path
import subprocess
import sys

from diagnostics import ROOT, collect, save
from panel import env_file, xray_healthy


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
        snapshot["core_process_ok"] = xray_healthy()
        # Save pre-probe evidence even if the probe is killed or interrupted.
        target = save(snapshot)
        if env_file().get("ROLE", "standalone") == "node":
            snapshot["probe"] = {"skipped": "node has no local subscription; probe from master/external host"}
            ok = snapshot["core_process_ok"]
        else:
            try:
                r = subprocess.run([sys.executable, str(Path(__file__).with_name("probe-tunnels.py"))],
                                   capture_output=True, text=True, timeout=180)
                snapshot["probe"] = json.loads(r.stdout)
                ok = snapshot["core_process_ok"] and r.returncode == 0
            except (OSError, ValueError, subprocess.TimeoutExpired):
                snapshot["probe"] = {"ok": False, "reason": "probe failed or exceeded 180 seconds"}
                ok = False
        # Replace our own protected snapshot; retain one file per observation.
        from stackctl.envio import atomic_write
        atomic_write(target, json.dumps(snapshot, indent=2))
        print(json.dumps({"ok": ok, "snapshot": str(target), "probe": snapshot["probe"]}))
        return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
