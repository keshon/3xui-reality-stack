#!/usr/bin/env python3
"""Bounded server snapshots without account credentials or peer addresses."""
from collections import Counter
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def command(args, timeout=10):
    try:
        r = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
        return r.returncode, r.stdout
    except (OSError, subprocess.TimeoutExpired):
        return -1, ""


def socket_summary(text):
    states, inactivity, peers = Counter(), Counter(), Counter()
    public = False
    for line in text.splitlines():
        if line and not line[0].isspace():
            fields = line.split()
            public = False
            if len(fields) < 5:
                continue
            states[fields[0]] += 1
            public = fields[3].endswith(":443") and fields[0] == "ESTAB"
            if public:
                peers[fields[4].rsplit(":", 1)[0]] += 1
        elif public:
            m = re.search(r"lastrcv:(\d+)", line)
            if m:
                ms = int(m.group(1))
                inactivity["under_1m" if ms < 60000 else "1m_to_1h" if ms < 3600000 else "over_1h"] += 1
    return {"states": dict(states), "public_443_last_receive": dict(inactivity),
            "public_443_distinct_sources": len(peers),
            "public_443_largest_source_counts": [n for _, n in peers.most_common(5)]}


def collect():
    result = {"time_utc": datetime.now(timezone.utc).isoformat(), "schema": 1}
    for name, args in {
        "uptime": ["uptime"], "memory": ["free", "-m"],
        "disk": ["df", "-h", "/"], "memory_pressure": ["cat", "/proc/pressure/memory"],
        "conntrack_host": ["sysctl", "net.netfilter.nf_conntrack_count", "net.netfilter.nf_conntrack_max"],
        "conntrack_xray": ["docker", "exec", "3x-ui", "cat", "/proc/sys/net/netfilter/nf_conntrack_count"],
        "network_counters": ["nstat", "-az", "TcpRetransSegs", "TcpExtTCPTimeouts", "TcpExtListenDrops"],
        "containers": ["docker", "ps", "-a", "--format", "{{.Names}} {{.Status}}"],
        "container_state": ["docker", "inspect", "-f", "{{.Name}} running={{.State.Running}} oom={{.State.OOMKilled}} restarts={{.RestartCount}}", "3x-ui", "traefik"],
    }.items():
        code, output = command(args)
        result[name] = {"exit": code, "output": output[:8192]}
    code, pid = command(["docker", "inspect", "-f", "{{.State.Pid}}", "3x-ui"])
    if code == 0 and pid.strip().isdigit() and int(pid.strip()) > 0:
        code, text = command(["nsenter", "-t", pid.strip(), "-n", "ss", "-Hnti"])
        result["sockets"] = {"exit": code, **socket_summary(text)}
    # Count known symptoms; raw application/journal lines may contain secrets.
    for name, args in {
        "kernel_symptoms": ["journalctl", "-k", "--since", "10 minutes ago", "-n", "1000", "--no-pager"],
        "core_symptoms": ["docker", "logs", "--since", "10m", "--tail", "2000", "3x-ui"],
    }.items():
        try:
            r = subprocess.run(args, capture_output=True, text=True, timeout=10)
            text = r.stdout + r.stderr
            result[name] = {"exit": r.returncode, "counts": {term: text.lower().count(term) for term in (
                "table full", "out of memory", "operation not permitted", "failed to start",
                "too many open files", "timeout", "x-forwarded-for")}}
        except (OSError, subprocess.TimeoutExpired):
            result[name] = {"exit": -1}
    return result


def save(snapshot, directory=None, keep=288):
    if keep < 1:
        raise ValueError("keep must be positive")
    directory = Path(directory) if directory else ROOT / ".stack" / "diagnostics"
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    directory.chmod(0o700)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S%fZ")
    target = directory / f"snapshot-{stamp}.json"
    with tempfile.NamedTemporaryFile(mode="w", dir=directory, delete=False, encoding="utf-8") as f:
        temporary = Path(f.name)
        os.chmod(temporary, 0o600)
        json.dump(snapshot, f, indent=2)
    os.replace(temporary, target)
    for old in sorted(directory.glob("snapshot-*.json"), reverse=True)[keep:]:
        if old.is_file() and not old.is_symlink():
            old.unlink()
    return target


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--write", action="store_true")
    parser.add_argument("--keep", type=int, default=288)
    args = parser.parse_args()
    snapshot = collect()
    print(save(snapshot, keep=args.keep) if args.write else json.dumps(snapshot, indent=2))
