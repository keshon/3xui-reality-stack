"""Consistent deployment-state snapshots shared by backups and source upgrades."""
import argparse
from datetime import datetime, timezone
import os
from pathlib import Path
import re
import shutil
import sqlite3
import tarfile
import tempfile
import time
import uuid

from .envio import read_env

CONFIG_FILES = (".env", "users.txt", "nodes.txt", ".stack/config.json", ".stack/applied.json",
                ".stack/operation.json", ".stack/adoption.json")
STATE_FILES = (*CONFIG_FILES, ".stack/panel-ready", "CLIENT-CREDENTIALS.txt", "RELEASE-MANIFEST.json")
STATE_DIRECTORIES = ("shared/certs", "traefik/data/acme", "xui/db")
DB_FILES = {"x-ui.db", "x-ui.db-wal", "x-ui.db-shm", "x-ui.db-journal"}


def safe_path(root, name):
    path = root / name
    if not path.resolve().is_relative_to(root.resolve()):
        raise ValueError("Backup input escapes the deployment directory.")
    for ancestor in (path, *path.parents):
        if ancestor == root:
            break
        if ancestor.is_symlink():
            raise ValueError("Backup inputs must not traverse symlinks.")
    return path


def snapshot_data(root, destination):
    root, destination = Path(root).resolve(), Path(destination)
    destination.mkdir(parents=True, exist_ok=True, mode=0o700)
    destination.chmod(0o700)
    for name in STATE_FILES:
        source = safe_path(root, name)
        if source.is_file():
            target = destination / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, target)
    for name in STATE_DIRECTORIES:
        source = safe_path(root, name)
        if not source.is_dir():
            continue
        if any(p.is_symlink() for p in source.rglob("*")):
            raise ValueError("Backup data contains symlinks; back it up explicitly.")
        ignore = (lambda directory, names: DB_FILES.intersection(names)) if name == "xui/db" else None
        shutil.copytree(source, destination / name, ignore=ignore)
    database = safe_path(root, "xui/db/x-ui.db")
    if database.is_file():
        target = destination / "xui/db/x-ui.db"
        target.parent.mkdir(parents=True, exist_ok=True)
        with sqlite3.connect(database.as_uri() + "?mode=ro", uri=True) as source, sqlite3.connect(target) as backup:
            deadline = time.monotonic() + 60
            def progress(*_):
                if time.monotonic() > deadline:
                    raise ValueError("SQLite backup exceeded 60 seconds.")
            source.backup(backup, pages=256, progress=progress, sleep=0.1)
        target.chmod(0o600)


def create_backup(root, directory, keep=7):
    root, directory = Path(root).resolve(), Path(directory).resolve()
    if keep < 1 or directory == Path(directory.anchor):
        raise ValueError("Use a backup directory below the filesystem root and a positive retention count.")
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    directory.chmod(0o700)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8]
    archive = directory / f"backup-{stamp}.tar.gz"
    with tempfile.TemporaryDirectory(prefix=".snapshot-", dir=directory) as temporary:
        staging = Path(temporary)
        assert staging.resolve().parent == directory
        data = staging / "data"
        snapshot_data(root, data)
        if not any(p.is_file() for p in data.rglob("*")):
            raise ValueError("Nothing to back up.")
        packed = staging / "archive.tar.gz"
        with tarfile.open(packed, "w:gz") as tar:
            for path in sorted(data.iterdir()):
                tar.add(path, arcname=path.name)
        packed.chmod(0o600)
        os.replace(packed, archive)
    # Rotate only this command's and the legacy script's archive names.
    pattern = re.compile(r"backup-(?:\d{8}-\d{6}|\d{8}T\d{6}Z-[0-9a-f]{8})\.tar\.gz")
    archives = sorted((p for p in directory.iterdir() if pattern.fullmatch(p.name)
                       and p.is_file() and not p.is_symlink() and p.resolve().parent == directory),
                      key=lambda p: (p == archive, p.stat().st_mtime_ns, p.name), reverse=True)
    for old in archives[keep:]:
        old.unlink()
    return archive


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument("--directory", type=Path)
    parser.add_argument("--keep", type=int)
    args = parser.parse_args(argv)
    from .config import Store
    root = args.root.resolve()
    try:
        values = read_env(root / ".env")
        directory = args.directory or Path(os.environ.get("BACKUP_DIR") or values.get("BACKUP_DIR") or "backups")
        if not directory.is_absolute():
            directory = root / directory
        keep = args.keep if args.keep is not None else int(os.environ.get("BACKUP_KEEP") or values.get("BACKUP_KEEP") or 7)
        with Store(root).lock():
            archive = create_backup(root, directory, keep)
        print("State backup: " + str(archive))
        print("Includes managed settings and a consistent SQLite snapshot. Database restoration is manual.")
        return 0
    except (OSError, ValueError, sqlite3.Error):
        print("Backup failed. Check the input paths, permissions, database and retention settings.")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
