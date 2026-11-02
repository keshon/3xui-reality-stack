import os
from pathlib import Path
import sqlite3
import tarfile

import pytest

from stackctl.backup import create_backup, snapshot_data, main


def test_snapshot_reads_committed_wal_and_includes_managed_settings(tmp_path):
    root, dest = tmp_path / "live", tmp_path / "snapshot"
    (root / "xui/db").mkdir(parents=True)
    (root / ".stack").mkdir()
    (root / ".stack/config.json").write_text('{"managed": true}')
    (root / ".env").write_text("PASSWORD=secret")
    (root / "xui/db/other-state.json").write_text("{}")
    with sqlite3.connect(root / "xui/db/x-ui.db") as live:
        live.execute("pragma journal_mode=wal")
        live.execute("create table users(name text)")
        live.execute("insert into users values ('alice')")
        live.commit()
        assert (root / "xui/db/x-ui.db-wal").exists()
        snapshot_data(root, dest)
    assert (dest / ".stack/config.json").read_text() == '{"managed": true}'
    assert not (dest / "xui/db/x-ui.db-wal").exists()
    assert (dest / "xui/db/other-state.json").read_text() == "{}"
    with sqlite3.connect(dest / "xui/db/x-ui.db") as backup:
        assert backup.execute("pragma integrity_check").fetchone()[0] == "ok"
        assert backup.execute("select name from users").fetchall() == [("alice",)]


def test_archive_rotation_is_scoped_and_excludes_logs_and_upgrade_history(tmp_path):
    root, directory = tmp_path / "live", tmp_path / "archives"
    (root / ".stack/upgrades").mkdir(parents=True)
    (root / ".stack/config.json").write_text("{}")
    (root / ".stack/upgrades/keep-out").write_text("old source")
    (root / "traefik/data/logs").mkdir(parents=True)
    (root / "traefik/data/logs/access.log").write_text("private traffic log")
    directory.mkdir()
    unrelated = directory / "backup-unrelated.tar.gz"
    unrelated.write_text("keep")
    results = [create_backup(root, directory, keep=2) for _ in range(3)]
    assert results[-1].exists()
    assert sum(p.exists() for p in results) == 2
    assert unrelated.read_text() == "keep"
    with tarfile.open(results[-1]) as tar:
        names = tar.getnames()
    assert ".stack/config.json" in names
    assert not any("upgrades" in n or "access.log" in n for n in names)
    if os.name != "nt":
        assert directory.stat().st_mode & 0o777 == 0o700
        assert results[-1].stat().st_mode & 0o777 == 0o600


def test_backup_cli_preserves_literal_directory_and_retention_environment(tmp_path, monkeypatch):
    root = tmp_path / "live"
    root.mkdir()
    (root / ".env").write_text("BACKUP_DIR=archives\nBACKUP_KEEP=7\n")
    monkeypatch.setenv("BACKUP_DIR", "private backup $literal")
    monkeypatch.setenv("BACKUP_KEEP", "1")
    assert main(["--root", str(root)]) == 0
    assert main(["--root", str(root)]) == 0
    assert len(list((root / "private backup $literal").glob("backup-*.tar.gz"))) == 1
    assert not (root / "archives").exists()


def test_bad_retention_is_rejected_before_creating_backup_directory(tmp_path):
    with pytest.raises(ValueError):
        create_backup(tmp_path, tmp_path / "archives", keep=0)
    assert not (tmp_path / "archives").exists()


def test_symlink_input_cannot_escape_into_backup(tmp_path):
    root = tmp_path / "live"
    root.mkdir()
    outside = tmp_path / "private"
    outside.write_text("never copy")
    try:
        (root / ".env").symlink_to(outside)
    except OSError:
        pytest.skip("Symlink creation unavailable")
    with pytest.raises(ValueError, match="escapes|symlinks"):
        snapshot_data(root, tmp_path / "snapshot")
