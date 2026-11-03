import http.cookiejar
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import sys
import threading
import urllib.error
import urllib.parse

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))
import panel


@pytest.fixture
def endpoint(tmp_path, monkeypatch):
    state = {"logins": 0, "session": None, "api_status": 200, "password": "test-password"}

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_GET(self):
            if self.path.endswith("/panel/api/clients/list"):
                authenticated = self.headers.get("Cookie") == "session=" + str(state["session"])
                status = state["api_status"] if authenticated else 401
                self.send_response(status)
                self.end_headers()
                self.wfile.write(json.dumps({"success": status == 200, "obj": []}).encode())
            else:
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b'<meta name="csrf-token" content="csrf">')

        def do_POST(self):
            assert self.path.endswith("/login")
            values = urllib.parse.parse_qs(self.rfile.read(int(self.headers["Content-Length"])).decode())
            state["logins"] += 1
            success = values.get("password") == [state["password"]]
            self.send_response(200)
            if success:
                state["session"] = str(state["logins"])
                self.send_header("Set-Cookie", "session=" + state["session"] + "; Path=/; HttpOnly")
            self.end_headers()
            self.wfile.write(json.dumps({"success": success}).encode())

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    worker = threading.Thread(target=server.serve_forever, daemon=True)
    worker.start()
    monkeypatch.setattr(panel, "ROOT", str(tmp_path))
    monkeypatch.setattr(panel, "container_ip", lambda: "127.0.0.1")

    def client(password="test-password"):
        value = panel.Panel({"PANEL_PATH": "/private", "PANEL_USER": "test-user", "PANEL_PASS": password})
        value.base = "http://127.0.0.1:" + str(server.server_port) + "/private"
        return value

    yield state, client, tmp_path / ".stack/probe-panel-session.cookies"
    server.shutdown()
    server.server_close()
    worker.join()


def test_probe_reuses_cookie_across_process_like_clients_without_login(endpoint):
    state, client, cache = endpoint
    client().login(reuse_session=True)
    client().login(reuse_session=True).clients()
    assert state["logins"] == 1
    assert "test-password" not in cache.read_text()
    if os.name != "nt":
        assert cache.stat().st_mode & 0o777 == 0o600
        assert cache.parent.stat().st_mode & 0o777 == 0o700


def test_expired_server_session_authenticates_once_then_reuses(endpoint):
    state, client, _ = endpoint
    client().login(reuse_session=True)
    state["session"] = None
    client().login(reuse_session=True)
    client().login(reuse_session=True)
    assert state["logins"] == 2


def test_regular_logins_remain_explicit_and_do_not_create_cache(endpoint):
    state, client, cache = endpoint
    client().login()
    client().login()
    assert state["logins"] == 2
    assert not cache.exists()


def test_changed_credentials_cannot_reuse_old_authenticated_session(endpoint):
    state, client, _ = endpoint
    client().login(reuse_session=True)
    with pytest.raises(SystemExit, match="panel login failed"):
        client("wrong-password").login(reuse_session=True)
    assert state["logins"] == 2


def test_server_error_does_not_trigger_new_login_or_overwrite_cache(endpoint):
    state, client, cache = endpoint
    client().login(reuse_session=True)
    before = cache.read_bytes()
    state["api_status"] = 500
    with pytest.raises(urllib.error.HTTPError) as error:
        client().login(reuse_session=True)
    assert error.value.code == 500
    assert state["logins"] == 1
    assert cache.read_bytes() == before


def test_damaged_cookie_cache_recovers_with_normal_authentication(endpoint):
    state, client, cache = endpoint
    client().login(reuse_session=True)
    lines = cache.read_text().splitlines()
    cache.write_text("\n".join(lines[:2]) + "\ninvalid cookie contents\n")
    client().login(reuse_session=True)
    assert state["logins"] == 2
