import importlib.util
from pathlib import Path
import subprocess
import sys

import pytest


@pytest.fixture
def launcher(store, monkeypatch):
    path = Path(__file__).resolve().parents[1] / "scripts/launch.py"
    spec = importlib.util.spec_from_file_location("stack_launch", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    (store.root / "pyproject.toml").write_text("demo configuration\n")
    monkeypatch.setattr(module, "ROOT", store.root)
    return module


def test_first_launch_then_offline_reuse(launcher, store, monkeypatch):
    calls = []
    def create(root):
        calls.append("create")
        path = launcher.environment_python(root)
        path.parent.mkdir(parents=True)
        path.write_text("interpreter")
    def run(command, **kwargs):
        calls.append(command)
        return subprocess.CompletedProcess(command, 0)
    monkeypatch.setattr(launcher, "create_environment", create)
    monkeypatch.setattr(launcher, "ready", lambda *_: True)
    monkeypatch.setattr(launcher.subprocess, "run", run)
    python = launcher.prepare(store.root)
    assert calls[0] == "create"
    assert "install" in calls[1]
    assert (store.root / ".venv/.stack-ready").is_file()
    calls.clear()
    assert launcher.prepare(store.root) == python
    assert calls == []  # no pip or downloads on a normal repeat launch


def test_failed_install_retries_without_false_ready_marker(launcher, store, monkeypatch):
    python = launcher.environment_python(store.root)
    python.parent.mkdir(parents=True)
    python.write_text("interpreter")
    fail = True
    def run(command, **kwargs):
        nonlocal fail
        if "install" in command and fail:
            fail = False
            raise subprocess.CalledProcessError(1, command)
        return subprocess.CompletedProcess(command, 0)
    monkeypatch.setattr(launcher.subprocess, "run", run)
    monkeypatch.setattr(launcher, "ready", lambda *_: True)
    with pytest.raises(subprocess.CalledProcessError):
        launcher.prepare(store.root)
    assert not (store.root / ".venv/.stack-ready").exists()
    assert launcher.prepare(store.root) == python
    assert (store.root / ".venv/.stack-ready").is_file()


def test_dependency_change_or_broken_import_triggers_repair(launcher, store, monkeypatch):
    python = launcher.environment_python(store.root)
    python.parent.mkdir(parents=True)
    python.write_text("interpreter")
    marker = store.root / ".venv/.stack-ready"
    marker.write_text(launcher.signature(store.root))
    probes = iter([False, True])
    calls = []
    monkeypatch.setattr(launcher, "ready", lambda *_: next(probes))
    monkeypatch.setattr(launcher.subprocess, "run", lambda cmd, **kw: (calls.append(cmd) or subprocess.CompletedProcess(cmd, 0)))
    launcher.prepare(store.root)
    assert any("install" in cmd for cmd in calls)
    calls.clear()
    (store.root / "pyproject.toml").write_text("changed configuration")
    monkeypatch.setattr(launcher, "ready", lambda *_: True)
    launcher.prepare(store.root)
    assert any("install" in cmd for cmd in calls)
    assert marker.read_text() == launcher.signature(store.root)


def test_setup_respects_deployment_lock(launcher, store):
    with store.lock():
        assert launcher.main(["status"]) == 1


def test_old_python_gets_actionable_error(launcher, monkeypatch, capsys):
    monkeypatch.setattr(launcher.sys, "version_info", (3, 10, 0))
    assert launcher.main([]) == 1
    assert "Python 3.11+" in capsys.readouterr().err
