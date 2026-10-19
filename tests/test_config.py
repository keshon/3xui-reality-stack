from copy import deepcopy
import os
import subprocess
import sys

import pytest

from stackctl.config import normalize, path_prefix, fingerprint, node_url
from stackctl.envio import parse_env, render_env, read_env, atomic_write


@pytest.mark.parametrize("value", ["simple", " space at both ends ", "$HOME ${USER} $$", "'single' and \"double\"", "C:\\path\\file", "trailing\\", "\\\"'\\$", "пароль # test=1", "\\\\\\$\\\\\"", "", "#"])
def test_literal_secret_roundtrip(value):
    assert parse_env(render_env({"PASSWORD": value})) == {"PASSWORD": value}


@pytest.mark.parametrize("text", ['A="unclosed', "A=1\nA=2", "bad key=x", "A='x'junk", 'A="x\\ny"'])
def test_invalid_env_fails_without_secret_in_error(text):
    with pytest.raises(ValueError):
        parse_env(text)


def test_legacy_env_literal_dollar():
    assert parse_env("A=$HOME\nB='x\\'y'\nC=hello # comment\n") == {"A": "$HOME", "B": "x'y", "C": "hello"}


@pytest.mark.parametrize("value,expected", [("panel", "/panel"), ("/panel/", "/panel"), (" /panel/path/ ", "/panel/path")])
def test_path_normalization(value, expected):
    assert path_prefix(value) == expected


@pytest.mark.parametrize("value", ["/", "https://example.test/p", "/a?x=1", "/../a", "/a b", "/a//b"])
def test_path_errors(value):
    with pytest.raises(ValueError):
        path_prefix(value)


def test_node_url():
    assert node_url("ops-de.example.test/panel-abc/") == "https://ops-de.example.test/panel-abc"
    with pytest.raises(ValueError):
        node_url("http://ops-de.example.test/panel")
    with pytest.raises(ValueError):
        node_url("https://ops-de.example.test")


def test_management_defaults_and_collision(config):
    value = normalize(config)
    assert value["env"]["MANAGEMENT_BIND"] == "127.0.0.1"
    assert value["env"]["MANAGEMENT_PORT"] == "2053"
    config["env"]["ROLE"] = "node"
    config["users"] = []
    assert normalize(config)["env"]["MANAGEMENT_BIND"] == "0.0.0.0"
    config["env"]["MANAGEMENT_PORT"] = "443"
    with pytest.raises(ValueError, match="overlaps"):
        normalize(config)


def test_save_preserves_password_and_original_inputs(store, config):
    original = "PANEL_PASS='$old'\n"
    atomic_write(store.root / ".env", original)
    saved = store.save(config)
    assert saved["env"]["PANEL_PATH"] == "/panel-123"
    assert store.load()["env"]["PANEL_PASS"] == config["env"]["PANEL_PASS"]
    assert (store.directory / "imported" / ".env").read_text() == original
    # Saving configuration does not overwrite the deployment files.
    assert (store.root / ".env").read_text() == original
    store.render(saved)
    assert read_env(store.root / ".env")["PANEL_PASS"] == config["env"]["PANEL_PASS"]


def test_concurrent_save_and_os_lock(store, config):
    original = fingerprint(store.load())
    store.save(config, expected=original)
    with pytest.raises(ValueError, match="another session"):
        store.save(config, expected=original)
    with store.lock():
        with pytest.raises(ValueError, match="Another stack process"):
            with store.lock():
                pass


def test_validate_overlap_and_nonfinite_limits(config):
    config["env"]["SUB_PATH"] = "panel-123/child"
    with pytest.raises(ValueError, match="overlap"):
        normalize(config)
    config["env"]["SUB_PATH"] = "sub"
    config["env"]["USER_LIMIT_GB"] = "nan"
    with pytest.raises(ValueError, match="finite"):
        normalize(config)


def test_generated_paths_and_credentials_stable_after_save(store, config):
    for key in ("PANEL_PATH", "PANEL_USER", "PANEL_PASS"):
        config["env"][key] = ""
    saved = store.save(config)
    assert normalize(store.load()) == saved


def test_shared_bash_loader_matches_python(store, config):
    from conftest import ROOT
    from pathlib import Path
    bash = Path("C:/Program Files/Git/bin/bash.exe") if os.name == "nt" else Path("/bin/bash")
    if not bash.is_file():
        pytest.skip("Bash not available")
    # The real adapter resolves its default .env from the checkout; provide a
    # tiny temporary layout with the production package linked via PYTHONPATH.
    import shutil
    (store.root / "scripts").mkdir()
    for name in ("config-env.py", "env.sh"):
        shutil.copyfile(ROOT / "scripts" / name, store.root / "scripts" / name)
    store.render(normalize(config))
    env = os.environ.copy()
    env["PYTHONPATH"] = str(ROOT)
    env["PATH"] = str(Path(sys.executable).parent) + os.pathsep + env["PATH"]
    command = 'ROOT="$PWD"; source scripts/env.sh; load_stack_env; python3 -c \'import os,json; print(json.dumps(os.environ["PANEL_PASS"]))\''
    result = subprocess.run([str(bash), "-c", command], cwd=store.root, env=env, capture_output=True, text=True)
    assert result.returncode == 0, result.stderr
    import json
    assert json.loads(result.stdout) == config["env"]["PANEL_PASS"]
