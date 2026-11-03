import copy
import importlib.util
import json
from pathlib import Path
import sys

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))
spec = importlib.util.spec_from_file_location("legacy_import", Path(__file__).resolve().parents[1] / "scripts/import-legacy.py")
legacy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(legacy)
UUID = "0913e4b1-739e-4dbe-8847-bca1154f0d60"


def snapshot():
    rows = []
    for protocol, stream, client in [
        ("vless", {"security": "reality"}, {"email": "vtr-alice", "id": UUID}),
        ("trojan", {"network": "httpupgrade"}, {"email": "th-alice", "password": "old-trojan"}),
        ("vless", {"network": "xhttp"}, {"email": "vx-alice", "id": "other"}),
    ]:
        rows.append({"protocol": protocol, "stream_settings": json.dumps(stream),
                     "settings": json.dumps({"clients": [{**client, "subId": "unchanged"}]})})
    return {"inbounds": rows}


def test_collects_credentials_by_transport_and_preserves_subscription():
    account, = legacy.accounts(snapshot())
    assert (account["uuid"], account["password"], account["subId"]) == (UUID, "old-trojan", "unchanged")
    assert set(account["retire"]) == {"th-alice", "vx-alice"}


@pytest.mark.parametrize("field,value", [("subId", "different"), ("expiryTime", 100), ("enable", False)])
def test_refuses_conflicting_legacy_identity_or_limits(field, value):
    data = snapshot()
    settings = json.loads(data["inbounds"][1]["settings"])
    settings["clients"][0][field] = value
    data["inbounds"][1]["settings"] = json.dumps(settings)
    with pytest.raises(ValueError):
        legacy.accounts(data)


class Panel:
    def __init__(self, fail_verify=False):
        self.rows = {name: {"client": {"email": name, "uuid": UUID if name == "vtr-alice" else "other"},
                            "inboundIds": []} for name in ["vtr-alice", "th-alice", "vx-alice"]}
        self.deleted = []
        self.fail_verify = fail_verify

    def clients(self):
        return [v["client"] for v in self.rows.values()]

    def client(self, name):
        return copy.deepcopy(self.rows.get(name))

    def update_client(self, source, client):
        self.rows[client["email"]] = {"client": copy.deepcopy(client), "inboundIds": []}
        self.rows.pop(source, None) if source != client["email"] else None
        return {"success": True}

    def attach(self, name, target):
        self.rows[name]["inboundIds"] = target
        if self.fail_verify:
            self.rows[name]["client"]["subId"] = "wrong"
        return {"success": True}

    def post(self, path):
        name = path.rsplit("/", 1)[1]
        self.deleted.append(name)
        self.rows.pop(name)
        return {"success": True}


def test_merges_and_can_resume_without_rotating_authentication():
    panel = Panel()
    plan = legacy.accounts(snapshot())
    legacy.migrate(panel, plan, [1, 6, 7])
    auth = panel.rows["alice"]["client"]["auth"]
    legacy.migrate(panel, plan, [1, 6, 7])
    assert panel.rows["alice"]["client"]["auth"] == auth
    assert set(panel.rows) == {"alice"}


def test_verification_failure_retains_original_trojan_and_xhttp():
    panel = Panel(fail_verify=True)
    with pytest.raises(RuntimeError, match="verification failed"):
        legacy.migrate(panel, legacy.accounts(snapshot()), [1, 6, 7])
    assert panel.deleted == []
