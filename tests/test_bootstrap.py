"""Exercise the real Compose orchestration without starting any containers."""
import os
from pathlib import Path
import shutil
import subprocess
import sys

import pytest

from stackctl.config import normalize
from stackctl.envio import update_env
from conftest import ROOT


def test_node_management_not_exposed_until_access_is_verified(store, config):
    bash = Path("C:/Program Files/Git/bin/bash.exe") if os.name == "nt" else Path("/bin/bash")
    if not bash.is_file():
        pytest.skip("Bash not available")
    config["env"]["ROLE"] = "node"
    config["users"] = []
    config = normalize(config)
    store.render(config)
    for name in ("bootstrap.sh", "scripts/env.sh", "scripts/config-env.py", "traefik/dynamic/tls.yml.tmpl"):
        target = store.root / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(ROOT / name, target)
    (store.root / "decoy/site").mkdir(parents=True)
    (store.root / "decoy/site/index.html").write_text("Example")
    (store.root / "xui").mkdir()
    binary = store.root / "bin"
    binary.mkdir()
    fake = binary / "docker"
    fake.write_text('''#!/usr/bin/env bash
if [ "$1" = network ]; then
  if [ "$#" -gt 3 ]; then echo 172.28.0.0/16; fi
  exit 0
fi
echo "$MANAGEMENT_BIND" >> "$TEST_DOCKER_CALLS"
''', newline="\n")
    fake.chmod(0o755)
    env = os.environ.copy()
    env.update(PYTHONPATH=str(ROOT), TEST_DOCKER_CALLS=str(store.root / "calls"))
    env["PATH"] = str(binary) + os.pathsep + str(Path(sys.executable).parent) + os.pathsep + env["PATH"]

    def bootstrap():
        (store.root / "calls").unlink(missing_ok=True)
        result = subprocess.run([str(bash), "bootstrap.sh"], cwd=store.root, env=env, capture_output=True, text=True, timeout=30)
        assert result.returncode == 0, result.stdout + result.stderr
        return (store.root / "calls").read_text().splitlines()

    assert set(bootstrap()) == {"127.0.0.1"}
    result = subprocess.run([sys.executable, str(store.root / "scripts/config-env.py"), "--mark-panel-ready"], env=env, capture_output=True, text=True)
    assert result.returncode == 0, result.stderr
    assert set(bootstrap()) == {"0.0.0.0"}
    # A pending credential change closes the public management listener again.
    update_env(store.root / ".env", {"PANEL_PASS": "new secret"})
    assert set(bootstrap()) == {"127.0.0.1"}
