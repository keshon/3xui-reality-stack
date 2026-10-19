from copy import deepcopy
import json

import pytest

from stackctl.config import fingerprint
from stackctl.engine import Engine, plan_text, redactor
from stackctl.envio import update_env


def test_interruption_resume_and_second_apply_noop(store, config):
    store.save(config)
    seen = []
    fail = True

    def runner(step, config, emit):
        nonlocal fail
        seen.append(step.name)
        if step.name == "lanes" and fail:
            fail = False
            raise KeyboardInterrupt()

    engine = Engine(store, runner)
    with pytest.raises(KeyboardInterrupt):
        engine.apply(lambda _: None)
    journal = store.json(store.journal_path)
    assert journal["status"] == "interrupted"
    assert "panel" in journal["completed"]
    with pytest.raises(ValueError, match="unfinished"):
        store.save(config)
    engine.apply(lambda _: None)
    assert seen.count("panel") == 1
    assert seen.count("lanes") == 2
    count = len(seen)
    engine.apply(lambda _: None)
    assert len(seen) == count
    assert store.json(store.journal_path)["status"] == "complete"


def test_user_changes_do_not_restart_panel(store, config):
    config = store.save(config)
    runner = lambda *_: None
    Engine(store, runner).apply(lambda _: None)
    config["users"].append("carol")
    store.save(config)
    _, steps, _ = Engine(store, runner).plan()
    assert [s.name for s in steps] == ["clients", "verify", "acceptance"]
    config["users"].remove("bob")
    store.save(config)
    assert Engine(store, runner).plan()[2] == ["bob"]


def test_generated_file_drift_stops_apply(store, config):
    store.save(config)
    Engine(store, lambda *_: None).apply(lambda _: None)
    (store.root / "users.txt").write_text("someone_else\n")
    with pytest.raises(ValueError, match="outside stack"):
        Engine(store).plan()


def test_stale_review_is_rejected_before_render(store, config):
    old = store.save(config)
    config["users"].append("carol")
    store.save(config)
    with pytest.raises(ValueError, match="reviewed"):
        Engine(store, lambda *_: None).apply(expected=fingerprint(old))
    assert not (store.root / ".env").exists()


def test_token_persisted_across_interrupted_node_install(store, config):
    config["env"]["ROLE"] = "node"
    config["users"] = []
    store.save(config)

    def runner(step, config, emit):
        if step.name == "token":
            update_env(store.root / ".env", {"NODE_API_TOKEN": "one-time-secret"})
            raise InterruptedError("lost response")

    with pytest.raises(InterruptedError):
        Engine(store, runner).apply(lambda _: None)
    seen = []
    Engine(store, lambda step, cfg, _: seen.append((step.name, cfg["env"].get("NODE_API_TOKEN")))).apply(lambda _: None)
    assert ("token", "one-time-secret") in seen
    assert store.load()["env"]["NODE_API_TOKEN"] == "one-time-secret"


def test_crash_during_render_can_resume(store, config):
    config = store.save(config)
    engine = Engine(store, lambda *_: None)
    _, steps, _ = engine.plan()
    store.write_json(store.journal_path, {"status": "rendering", "config_digest": fingerprint(config), "steps": [s.name for s in steps], "completed": [], "output_hashes": {}, "revoke": []})
    (store.root / ".env").write_text("PARTIAL=1\n")
    engine.apply(lambda _: None)
    assert store.load() == config
    assert store.json(store.journal_path)["status"] == "complete"


def test_config_not_editable_during_apply(store, config):
    store.save(config)

    def runner(*_):
        with pytest.raises(ValueError, match="Another stack process"):
            store.save(config)
    Engine(store, runner).apply(lambda _: None)


def test_plan_and_errors_never_print_known_secrets(store, config):
    store.save(config)
    text = plan_text(Engine(store))
    assert config["env"]["PANEL_PASS"] not in text
    assert config["env"]["CF_DNS_API_TOKEN"] not in text
    assert "/panel-123" not in text
    redact = redactor(config)
    assert "one" not in redact("https://host/sub/one")
    assert "dns-secret" not in redact("bad token dns-secret")


def test_node_replacement_and_multinode_revoke_rejected_before_saving(store, config):
    config["env"]["ROLE"] = "master"
    config["nodes"] = [{"name": "de", "url": "https://ops-de.example.test/p", "token": "node-secret", "domain": "www-de.example.test"}]
    config = store.save(config)
    Engine(store, lambda *_: None).apply(lambda _: None)
    change = deepcopy(config)
    change["nodes"][0]["domain"] = "www-new.example.test"
    with pytest.raises(ValueError, match="replacement runbook"):
        store.save(change)
    assert store.load() == config
    change = deepcopy(config)
    change["users"].remove("bob")
    with pytest.raises(ValueError, match="revocation"):
        store.save(change)
    assert store.load() == config


def test_recovery_unlocks_bad_credentials_and_rechecks_all_steps(store, config):
    config["env"]["ROLE"] = "master"
    config["nodes"] = [{"name": "de", "url": "https://ops-de.example.test:2053/p", "token": "wrong-token", "domain": "www-de.example.test"}]
    store.save(config)
    def fail(step, *_):
        if step.name == "nodes":
            raise RuntimeError("wrong token")
    with pytest.raises(RuntimeError):
        Engine(store, fail).apply(lambda _: None)
    Engine(store).recover()
    change = store.load()
    change["nodes"][0]["token"] = "corrected-token"
    store.save(change)
    assert "Recovery" in plan_text(Engine(store))
    seen = []
    Engine(store, lambda step, *_: seen.append(step.name)).apply(lambda _: None)
    assert "host" in seen and "panel" in seen and "nodes" in seen
    assert store.load()["nodes"][0]["token"] == "corrected-token"
    assert list((store.directory / "history").glob("*.json"))
    assert Engine(store).plan()[1] == []


def test_recovery_allows_preflight_typo_correction(store, config):
    store.save(config)
    def fail(*_): raise RuntimeError("DNS mismatch")
    with pytest.raises(RuntimeError):
        Engine(store, fail).apply(lambda _: None)
    Engine(store).recover()
    config = store.load()
    config["env"]["PUBLIC_DOMAIN"] = "corrected.example.test"
    store.save(config)
    Engine(store, lambda *_: None).apply(lambda _: None)
    assert store.load()["env"]["PUBLIC_DOMAIN"] == "corrected.example.test"
