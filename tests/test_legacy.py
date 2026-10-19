import importlib.util
from pathlib import Path
import sys
import urllib.error

import pytest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import panel


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), ROOT / "scripts" / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_empty_users_file_does_not_resurrect_stale_users(tmp_path):
    file = tmp_path / "users.txt"
    file.write_text("")
    assert panel.users({"USERS_FILE": str(file), "USERS": "old_user"}) == []


def test_http_500_is_never_empty_inventory():
    class Broken(panel.Panel):
        def __init__(self): pass
        def req(self, *_):
            raise urllib.error.HTTPError("http://local", 500, "bad", {}, None)
    with pytest.raises(RuntimeError, match="500"):
        Broken().inbounds()


def test_missing_user_requires_successful_inventory():
    class Empty(panel.Panel):
        def __init__(self): self.calls = []
        def get(self, path):
            self.calls.append(path)
            assert path == "/panel/api/clients/list"
            return {"success": True, "obj": []}
    p = Empty()
    assert p.client("alice") is None
    assert p.calls == ["/panel/api/clients/list"]


def test_known_user_read_failure_never_becomes_missing():
    class Broken(panel.Panel):
        def __init__(self): pass
        def get(self, path):
            if path.endswith("/list"):
                return {"success": True, "obj": [{"email": "alice"}]}
            raise RuntimeError("API failure")
    with pytest.raises(RuntimeError, match="API failure"):
        Broken().client("alice")


def test_bad_node_file_is_not_treated_as_single_server(tmp_path):
    file = tmp_path / "nodes.txt"
    file.write_text("de only-two-fields\n")
    with pytest.raises(ValueError):
        panel.read_nodes(path=str(file), env={"ROLE": "master"})


def test_existing_user_expiry_not_renewed(monkeypatch):
    module = load("reconcile-clients")
    env = {"ROLE": "standalone", "USERS": "alice", "USERS_FILE": "", "USER_EXPIRY_DAYS": "30", "LANES": "vless-reality"}
    class Fake:
        def __init__(self, *_): pass
        def login(self): return self
        def inbounds(self): return [{"remark": "vless-reality", "id": 1}]
        def client(self, *_): return {"client": {"expiryTime": 12345, "totalGB": 0}, "inboundIds": [1]}
        def update_client(self, *_): pytest.fail("existing expiry must not be rewritten")
    monkeypatch.setattr(module, "env_file", lambda: env)
    monkeypatch.setattr(module, "Panel", Fake)
    monkeypatch.setattr(panel, "read_nodes", lambda **_: [])
    monkeypatch.setattr(module, "APPLY", True)
    with pytest.raises(SystemExit) as result:
        module.main()
    assert result.value.code == 0


def test_node_rotation_uses_update_and_probe():
    module = load("managed-apply")
    entry = {"name": "de", "url": "https://ops-de.example.test/p", "token": "NEW", "domain": "www-de.example.test"}
    class Fake:
        def __init__(self): self.calls = []
        def node_list(self): return [{"id": 7, "name": "de", "scheme": "https", "address": "ops-de.example.test", "port": 443, "basePath": "/p"}]
        def post(self, path, body=None):
            self.calls.append((path, body))
            return {"success": True, "obj": {"status": "online"}}
    module.read_nodes = lambda **_: [entry]
    fake = Fake()
    module.nodes(fake, {})
    assert fake.calls[0][0] == "/panel/api/nodes/update/7"
    assert fake.calls[0][1]["apiToken"] == "NEW"
    assert fake.calls[1][0] == "/panel/api/nodes/probe/7"
