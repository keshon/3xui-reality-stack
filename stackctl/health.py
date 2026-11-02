"""Shared process, HTTPS and authenticated-transfer checks. No recovery actions."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys

from .envio import read_env


def invoke(args, timeout):
    try:
        return subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        return subprocess.CompletedProcess(args, -1, "", "")


def core_running():
    r = invoke(["docker", "inspect", "-f", "{{.State.Running}}", "3x-ui"], 10)
    if r.returncode or r.stdout.strip() != "true":
        return False
    r = invoke(["docker", "top", "3x-ui", "-eo", "pid,comm"], 10)
    return r.returncode == 0 and any(
        len(fields) == 2 and fields[0].isdigit()
        and re.fullmatch(r"xray(?:-linux-[\w-]+)?", fields[1])
        for fields in (line.split() for line in r.stdout.splitlines()))


def measure(root, values):
    core = bool(core_running())
    if values.get("ROLE", "standalone") == "node":
        return {"ok": core, "core_process_ok": core,
                "fallback_https": {"skipped": "check node management HTTPS from the master"},
                "probe": {"skipped": "node has no local subscription; probe from master/external host"}}
    domain = values.get("PUBLIC_DOMAIN")
    if domain:
        r = invoke(["curl", "--silent", "--fail", "--noproxy", "*", "--max-time", "15",
                    "--output", os.devnull, "--write-out", "%{http_code}", "https://" + domain + "/"], 20)
        fallback = {"ok": r.returncode == 0 and r.stdout == "200",
                    "http": int(r.stdout) if r.stdout.isdigit() else 0, "curl_exit": r.returncode}
    else:
        fallback = {"ok": False, "reason": "public domain is not configured"}
    r = invoke([sys.executable, str(Path(root) / "scripts/probe-tunnels.py")], 180)
    try:
        probe = json.loads(r.stdout)
        results = probe.get("results", [])
        good = r.returncode == 0 and bool(results) and all(x.get("ok") is True for x in results)
    except (ValueError, AttributeError, TypeError):
        probe = {"ok": False, "reason": "probe failed or exceeded 180 seconds"}
        good = False
    return {"ok": core and fallback["ok"] and good, "core_process_ok": core,
            "fallback_https": fallback, "probe": probe}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args(argv)
    result = measure(args.root, read_env(args.root / ".env"))
    print(json.dumps(result))
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
