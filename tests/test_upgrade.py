from copy import deepcopy
import hashlib
import json
from pathlib import Path
import sqlite3
import subprocess
import sys

import pytest

from stackctl.upgrade import Docker, Release, Upgrade, UpgradeError, digest
from stackctl.engine import Engine


def inventory(root):
    files = {str(p.relative_to(root)).replace("\\", "/"): hashlib.sha256(p.read_bytes()).hexdigest()
             for p in root.rglob("*") if p.is_file() and p.name != "RELEASE-MANIFEST.json"}
    (root / "RELEASE-MANIFEST.json").write_text(json.dumps({"base_commit": "a" * 40, "files": files}))


class Backend:
    def __init__(self, root):
        self.root = root
        self.checks = 0
        self.restarts = []
        self.affected = {"traefik": ["traefik"]}
        self.fail = set()
        self.on_verify = lambda n: None

    def check(self, release, changed):
        return deepcopy(self.affected)

    def recreate(self, services):
        self.restarts.append(deepcopy(services))

    def verify(self):
        self.checks += 1
        self.on_verify(self.checks)
        if self.checks in self.fail:
            raise UpgradeError("verification failed")


@pytest.fixture
def deployed(tmp_path):
    root, source = tmp_path / "live", tmp_path / "release"
    root.mkdir(); source.mkdir()
    files = {
        "compatibility.json": json.dumps({"schema": 1, "panel_image": "panel:test", "traefik_image": "traefik:test", "xray_versions": ["26.7.28"]}),
        "pyproject.toml": '[project]\nrequires-python = ">=3.11"\ndependencies = []\n',
        "install.sh": "#!/bin/sh\n",
        "README.md": "old source\n",
        "scripts/probe-tunnels.py": "# probe\n",
        "scripts/panel.py": "# panel\n",
        **{f"{p}/docker-compose.yml": "services: {}\n" for p in ("traefik", "decoy", "xui")},
        "traefik/docker-compose.dns01.yml": "services: {}\n",
    }
    for folder in (root, source):
        for name, data in files.items():
            path = folder / name; path.parent.mkdir(parents=True, exist_ok=True); path.write_text(data)
        inventory(folder)
    (source / "README.md").write_text("new source\n")
    (source / "scripts/new.py").write_text("# new\n")
    inventory(source)
    (root / ".env").write_text("PUBLIC_DOMAIN=example.test\nPANEL_PASS=literal-secret\n")
    (root / "users.txt").write_text("alice\n")
    (root / "xui/db").mkdir()
    with sqlite3.connect(root / "xui/db/x-ui.db") as db:
        db.execute("create table accounts (name text)")
        db.execute("insert into accounts values ('alice')")
    backend = Backend(root)
    return root, source, Upgrade(root, backend), backend


def test_preview_does_not_write_or_restart(deployed):
    root, source, upgrade, backend = deployed
    plan = upgrade.plan(source)
    text = upgrade.describe(plan)
    assert plan["changed"] == ["README.md", "scripts/new.py"]
    assert "traefik/traefik" in text
    assert "literal-secret" not in text
    assert not (root / ".stack").exists()
    assert backend.restarts == []


def test_upgrade_and_rollback_preserve_live_data(deployed):
    root, source, upgrade, backend = deployed
    protected = (root / ".env").read_bytes()
    plan = upgrade.plan(source)
    backup = upgrade.apply(source, plan["token"], emit=lambda _: None)
    assert (backup / "rollback.py").is_file()
    # Recovery must remain importable even when the old release lacks Upgrade.
    result = subprocess.run([sys.executable, "-c",
        "import sys; sys.path.insert(0, sys.argv[1]); import stackctl.upgrade as u; print(u.__file__)",
        str(backup / "recovery")], capture_output=True, text=True, timeout=15)
    assert result.returncode == 0, result.stderr
    assert str(backup / "recovery") in result.stdout
    assert (root / "README.md").read_text() == "new source\n"
    assert (backup / "data/.env").read_bytes() == protected
    with sqlite3.connect(backup / "data/xui/db/x-ui.db") as db:
        assert db.execute("select name from accounts").fetchall() == [("alice",)]
    # Changes after the snapshot must survive a source rollback.
    with sqlite3.connect(root / "xui/db/x-ui.db") as db:
        db.execute("insert into accounts values ('bob')")
    upgrade.rollback(backup)
    assert (root / "README.md").read_text() == "old source\n"
    assert not (root / "scripts/new.py").exists()
    assert (root / ".env").read_bytes() == protected
    with sqlite3.connect(root / "xui/db/x-ui.db") as db:
        assert db.execute("select count(*) from accounts").fetchone()[0] == 2
    assert backend.restarts == [{"traefik": ["traefik"]}] * 2


def test_failed_verification_automatically_restores_source(deployed):
    root, source, upgrade, backend = deployed
    backend.fail = {2}
    with pytest.raises(UpgradeError, match="previous source/services restored"):
        upgrade.apply(source, upgrade.plan(source)["token"], emit=lambda _: None)
    assert (root / "README.md").read_text() == "old source\n"
    assert not (root / "scripts/new.py").exists()
    assert json.loads(upgrade.journal.read_text())["status"] == "rolled_back"


def test_failed_rollback_remains_recoverable_and_blocks_apply(deployed, store, config):
    root, source, upgrade, backend = deployed
    backend.fail = {2, 3}
    with pytest.raises(UpgradeError, match="Rollback is incomplete"):
        upgrade.apply(source, upgrade.plan(source)["token"], emit=lambda _: None)
    journal = json.loads(upgrade.journal.read_text())
    assert journal["status"] == "rollback_failed"
    with pytest.raises(UpgradeError, match="interrupted source upgrade"):
        upgrade.plan(source)
    upgrade.rollback(journal["backup"])
    assert json.loads(upgrade.journal.read_text())["status"] == "rolled_back"
    store.save(config)
    store.write_json(store.directory / "upgrade.json", {"status": "rollback_failed"})
    with pytest.raises(ValueError, match="interrupted source upgrade"):
        Engine(store).plan()


def test_baseline_failure_changes_nothing(deployed):
    root, source, upgrade, backend = deployed
    backend.fail = {1}
    with pytest.raises(UpgradeError):
        upgrade.apply(source, upgrade.plan(source)["token"], emit=lambda _: None)
    assert (root / "README.md").read_text() == "old source\n"
    assert not upgrade.journal.exists()
    assert not (root / ".stack/upgrades").exists()


def test_deployed_source_drift_is_refused(deployed):
    root, source, upgrade, _ = deployed
    (root / "README.md").write_text("local customization")
    with pytest.raises(UpgradeError, match="outside the last release"):
        upgrade.plan(source)


def test_reviewed_plan_cannot_apply_changed_configuration(deployed):
    root, source, upgrade, _ = deployed
    plan = upgrade.plan(source)
    (root / "users.txt").write_text("alice\nbob\n")
    with pytest.raises(UpgradeError, match="changed after preview"):
        upgrade.apply(source, plan["token"], emit=lambda _: None)
    assert (root / "README.md").read_text() == "old source\n"


@pytest.mark.parametrize("name", ["../README.md", "/etc/passwd", "xui/db/x-ui.db", "decoy/site/index.html", "traefik/dynamic/tls.yml", ".env", "stackctl/../../escape", "scripts\\escape.py"])
def test_manifest_rejects_private_or_traversal_paths(deployed, name):
    _, source, _, _ = deployed
    manifest = json.loads((source / "RELEASE-MANIFEST.json").read_text())
    manifest["files"][name] = "0" * 64
    (source / "RELEASE-MANIFEST.json").write_text(json.dumps(manifest))
    with pytest.raises(UpgradeError):
        Release(source)


def test_damaged_release_and_dependency_changes_are_refused(deployed):
    _, source, upgrade, _ = deployed
    (source / "README.md").write_text("tampered")
    with pytest.raises(UpgradeError, match="hash"):
        upgrade.plan(source)
    (source / "pyproject.toml").write_text('[project]\nrequires-python=">=3.11"\ndependencies=["unexpected"]\n')
    inventory(source)
    with pytest.raises(UpgradeError, match="Dependency"):
        upgrade.plan(source)


def test_noop_does_not_backup_or_recreate(deployed):
    root, source, upgrade, backend = deployed
    (source / "README.md").write_text("old source\n")
    (source / "scripts/new.py").unlink()
    inventory(source)
    backend.affected = {}
    assert upgrade.apply(source, upgrade.plan(source)["token"], emit=lambda _: None) is None
    assert not (root / ".stack/upgrades").exists()
    assert backend.restarts == []


def test_rollback_refuses_later_edits(deployed):
    root, source, upgrade, _ = deployed
    backup = upgrade.apply(source, upgrade.plan(source)["token"], emit=lambda _: None)
    (root / "README.md").write_text("later edit")
    with pytest.raises(UpgradeError, match="Rollback is incomplete"):
        upgrade.rollback(backup)
    assert (root / "README.md").read_text() == "later edit"


def test_runtime_compatibility_is_checked_before_compose(deployed, monkeypatch):
    root, source, _, _ = deployed
    def wrong_runtime(args, *a, **kw):
        return "unsupported-panel" if args[1] == "inspect" else "Xray 26.7.28"
    monkeypatch.setattr("stackctl.upgrade.run", wrong_runtime)
    with pytest.raises(UpgradeError, match="compatibility contract"):
        Docker(root).check(Release(source), [])


def test_service_plan_uses_running_hashes_and_mounted_helpers(deployed, monkeypatch):
    root, source, _, _ = deployed
    services = {"traefik": {"traefik": "traefik:test", "certs-dumper": "python:test"},
                "decoy": {"decoy": "nginx:test"}, "xui": {"3x-ui": "panel:test"}}
    def command(args, *a, **kwargs):
        if args[1] == "exec":
            return "Xray 26.7.28"
        if args[1] == "image":
            return "sha256:test"
        if args[1] == "inspect":
            if args[3] == "{{.Config.Image}}":
                return "panel:test"
            if args[3] == "{{.Image}}":
                return "sha256:test"
            return "old" if args[-1] == "decoy" else "wanted"
        assert args[1] == "compose"
        project = Path(args[args.index("--project-directory") + 1]).name
        if "--format" in args:
            return json.dumps({"services": {name: {"image": image, "container_name": name}
                                           for name, image in services[project].items()}})
        assert args[-2:] == ["--hash", "*"]
        return "\n".join(name + " wanted" for name in services[project])
    monkeypatch.setattr("stackctl.upgrade.run", command)
    assert Docker(root).check(Release(source), ["traefik/dump-certs.py"]) == {
        "traefik": ["certs-dumper"], "decoy": ["decoy"]}


def test_cli_upgrade_defaults_to_preview(deployed, monkeypatch, capsys):
    root, source, upgrade, backend = deployed
    from stackctl import cli
    monkeypatch.setattr(cli.sys, "platform", "linux")
    monkeypatch.setattr(cli.os, "geteuid", lambda: 0, raising=False)
    monkeypatch.setattr(cli, "Upgrade", lambda _: upgrade)
    assert cli.main(["--root", str(root), "upgrade", "--from", str(source)]) == 0
    assert "Preview only" in capsys.readouterr().out
    assert backend.restarts == []
    assert not (root / ".stack").exists()
