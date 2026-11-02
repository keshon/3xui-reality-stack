import json
import subprocess

import pytest

from stackctl import health


@pytest.mark.parametrize("core,http,probe,exit_code,ok", [
    (True, "200", {"results": [{"ok": True}]}, 0, True),
    (False, "200", {"results": [{"ok": True}]}, 0, False),
    (True, "503", {"results": [{"ok": True}]}, 0, False),
    (True, "200", {"results": [{"ok": False}]}, 0, False),
    (True, "200", {"results": []}, 0, False),
    (True, "200", {"results": [{"ok": True}]}, 1, False),
])
def test_shared_health_keeps_failure_classes_separate(tmp_path, monkeypatch, core, http, probe, exit_code, ok):
    monkeypatch.setattr(health, "core_running", lambda: core)
    def invoke(args, timeout):
        return subprocess.CompletedProcess(args, 0 if args[0] == "curl" else exit_code,
                                          http if args[0] == "curl" else json.dumps(probe), "secret error")
    monkeypatch.setattr(health, "invoke", invoke)
    result = health.measure(tmp_path, {"PUBLIC_DOMAIN": "example.test"})
    assert result["ok"] is ok
    assert result["core_process_ok"] is core
    assert result["fallback_https"]["http"] == int(http)
    assert result["probe"] == probe
    assert "secret error" not in json.dumps(result)


def test_probe_timeout_does_not_become_healthy(tmp_path, monkeypatch):
    monkeypatch.setattr(health, "core_running", lambda: True)
    monkeypatch.setattr(health, "invoke", lambda args, timeout: subprocess.CompletedProcess(args, -1, "", ""))
    result = health.measure(tmp_path, {"PUBLIC_DOMAIN": "example.test"})
    assert result["ok"] is False
    assert result["probe"]["ok"] is False


def test_node_does_not_claim_authenticated_coverage(tmp_path, monkeypatch):
    monkeypatch.setattr(health, "core_running", lambda: True)
    monkeypatch.setattr(health, "invoke", lambda *_: pytest.fail("node must not run a local subscription probe"))
    result = health.measure(tmp_path, {"ROLE": "node"})
    assert "skipped" in result["probe"]
    assert "skipped" in result["fallback_https"]


def test_doctor_uses_live_inputs_and_shared_checks(store, config, monkeypatch):
    from stackctl import cli
    store.render(config)
    store.save(config)
    draft = store.load()
    draft["env"]["PANEL_PASS"] = "unapplied-draft"
    store.write_json(store.config_path, draft)
    monkeypatch.setattr(cli.sys, "platform", "linux")
    monkeypatch.setattr(cli.os, "geteuid", lambda: 0, raising=False)
    class Checks:
        lock_fd = None
        seen = []
        def run_process(self, step, deployed, emit):
            assert self.lock_fd is not None
            assert deployed["env"]["PANEL_PASS"] == config["env"]["PANEL_PASS"]
            self.seen.append((step.name, self.command(step, deployed)))
    checks = Checks()
    output = []
    cli.doctor(store, output.append, engine=checks)
    assert [name for name, _ in checks.seen] == ["doctor", "health"]
    assert "stackctl.health" in checks.seen[1][1]
    assert "authenticated Xray transfers passed" in output[-1]
