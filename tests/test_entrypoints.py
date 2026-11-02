import io
import os
from pathlib import Path
import shutil
import subprocess

import pytest

from stackctl.engine import Engine, Step
from conftest import ROOT


def bash():
    binary = Path("C:/Program Files/Git/bin/bash.exe") if os.name == "nt" else Path("/bin/bash")
    if not binary.is_file():
        pytest.skip("Bash not available")
    return str(binary)


def executable(directory, name, body):
    file = directory / name
    file.write_text("#!/usr/bin/env bash\n" + body, newline="\n")
    file.chmod(0o755)


@pytest.mark.parametrize("script", ["install.sh", "bootstrap.sh"])
def test_legacy_entrypoints_refuse_managed_deployment_before_changes(store, config, script):
    store.save(config)
    store.render(store.load())
    shutil.copyfile(ROOT / script, store.root / script)
    binary = store.root / "bin"
    binary.mkdir()
    executable(binary, "id", "echo 0\n")
    executable(binary, "docker", "echo docker-should-not-run; exit 9\n")
    env = os.environ.copy()
    env.pop("STACK_MANAGED_APPLY", None)
    env["PATH"] = str(binary) + os.pathsep + env["PATH"]
    result = subprocess.run([bash(), script], cwd=store.root, env=env, capture_output=True, text=True, timeout=15)
    assert result.returncode != 0
    assert "managed installation" in result.stdout + result.stderr
    assert "docker-should-not-run" not in result.stdout + result.stderr


def test_engine_marks_its_internal_stage_runner(store, config, monkeypatch):
    store.save(config)
    seen = []
    class Process:
        stdout = io.StringIO("")
        returncode = 0
        def poll(self): return 0
    def popen(command, **kwargs):
        seen.append(kwargs["env"]["STACK_MANAGED_APPLY"])
        return Process()
    monkeypatch.setattr(subprocess, "Popen", popen)
    Engine(store).run_process(Step("verify", "verify"), store.load(), lambda _: None)
    assert seen == ["1"]


def test_tuning_preview_leaves_defaults_and_never_applies(tmp_path):
    binary = tmp_path / "bin"
    binary.mkdir()
    executable(binary, "id", "echo 1000\n")
    executable(binary, "sysctl", "if [ \"$1\" = -n ]; then echo 'reno cubic bbr'; else echo unexpected-apply; exit 9; fi\n")
    executable(binary, "modprobe", "echo unexpected-module-load; exit 9\n")
    env = os.environ.copy()
    env["PATH"] = str(binary) + os.pathsep + env["PATH"]
    result = subprocess.run([bash(), str(ROOT / "scripts/host-tune.sh"), "--print"], env=env,
                            capture_output=True, text=True, timeout=15)
    assert result.returncode == 0, result.stderr
    keys = {line.split("=", 1)[0].strip() for line in result.stdout.splitlines() if "=" in line}
    assert keys == {"net.core.rmem_max", "net.core.wmem_max", "net.ipv4.tcp_mtu_probing",
                    "net.core.default_qdisc", "net.ipv4.tcp_congestion_control"}
    assert "unexpected" not in result.stdout + result.stderr
