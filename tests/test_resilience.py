import importlib.util
import json
from pathlib import Path
import subprocess
import sys

import pytest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import panel
import diagnostics

spec = importlib.util.spec_from_file_location("probe_tunnels", ROOT / "scripts/probe-tunnels.py")
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


@pytest.mark.parametrize("running,top,expected", [
    ((1, ""), (0, "xray"), False),
    ((0, "false"), (0, "xray"), False),
    ((0, "true"), (1, "xray"), False),
    ((0, "true"), (0, "PID COMMAND\n100 x-ui\n"), False),
    ((0, "true"), (0, "PID COMMAND\n100 x-ui\n200 xray-linux-amd6\n"), True),
])
def test_core_health_requires_successful_current_process_inventory(monkeypatch, running, top, expected):
    results = iter([running, top])
    def run(*args, **kwargs):
        if args[0][1] == "top":
            assert args[0][-1] == "pid,comm"  # Docker requires a PID column.
        code, output = next(results)
        return subprocess.CompletedProcess(args, code, output, "")
    monkeypatch.setattr(panel.subprocess, "run", run)
    assert panel.xray_healthy() is expected


def test_core_health_timeout_is_failure(monkeypatch):
    def run(*args, **kwargs):
        raise subprocess.TimeoutExpired(args, 10)
    monkeypatch.setattr(panel.subprocess, "run", run)
    assert not panel.xray_healthy()


def test_probe_keeps_tls_validation_and_subscription_fingerprint():
    lane, ob = probe.outbound("vless://test-id@example.test:443?security=reality&type=tcp&pbk=public-key&fp=safari&sid=1234&flow=xtls-rprx-vision&sni=cover.test")
    assert lane == "reality"
    assert ob["streamSettings"]["realitySettings"]["fingerprint"] == "safari"
    assert ob["settings"]["vnext"][0]["users"][0]["flow"] == "xtls-rprx-vision"
    _, trojan = probe.outbound("trojan://p%40ss@example.test:443?type=httpupgrade&security=tls&sni=cover.test&host=cover.test&path=%2Fupgrade&allowInsecure=1")
    assert trojan["settings"]["servers"][0]["password"] == "p@ss"
    assert trojan["streamSettings"]["tlsSettings"]["allowInsecure"] is False
    assert trojan["streamSettings"]["httpupgradeSettings"]["path"] == "/upgrade"
    _, hy = probe.outbound("hy2://user:secret@example.test:443?sni=cover.test")
    assert hy["streamSettings"]["hysteriaSettings"]["auth"] == "user:secret"


def test_socket_snapshot_aggregates_without_peer_addresses():
    text = "ESTAB 0 0 172.18.0.2:443 198.51.100.42:1234\n\t cubic lastrcv:7200000 bytes_received:1382\nESTAB 0 0 172.18.0.2:40000 172.18.0.3:8443\n\t cubic lastrcv:7200000\n"
    result = diagnostics.socket_summary(text)
    assert result["public_443_last_receive"] == {"over_1h": 1}
    assert result["public_443_largest_source_counts"] == [1]
    assert "198.51.100.42" not in json.dumps(result)


def test_snapshot_retention_preserves_unrelated_files(tmp_path):
    other = tmp_path / "keep.txt"
    other.write_text("keep")
    for i in range(4):
        diagnostics.save({"i": i}, tmp_path, keep=2)
    files = sorted(tmp_path.glob("snapshot-*.json"))
    assert len(files) == 2
    assert [json.loads(p.read_text())["i"] for p in files] == [2, 3]
    assert other.read_text() == "keep"
