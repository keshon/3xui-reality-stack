from pathlib import Path
import shutil

import pytest

from stackctl.config import Store

ROOT = Path(__file__).resolve().parents[1]


@pytest.fixture
def store(tmp_path):
    for name in (".env.example", "install.sh"):
        shutil.copyfile(ROOT / name, tmp_path / name)
    return Store(tmp_path)


@pytest.fixture
def config(store):
    value = store.load()
    value["env"].update(BASE_DOMAIN="example.test", PUBLIC_DOMAIN="www.example.test", PANEL_DOMAIN="ops.example.test", CERT_BASENAME="example.test", LETSENCRYPT_EMAIL="admin@example.test", CF_DNS_API_TOKEN="dns-secret", PANEL_USER="literal $user", PANEL_PASS="p'\" $HOME \\ end", PANEL_PATH="panel-123", SUB_PATH="sub-123", JSON_PATH="json-123")
    value["users"] = ["alice", "bob"]
    return value
