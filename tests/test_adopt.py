from copy import deepcopy

import pytest

from stackctl.adopt import adopt, preview
from stackctl.config import fingerprint, normalize
from stackctl.engine import Engine


def existing(store, config):
    store.render(normalize(config))
    return {name: (store.root / name).read_bytes() for name in (".env", "users.txt", "nodes.txt")}


def test_adoption_preserves_inputs_and_avoids_initial_reprovision(store, config):
    before = existing(store, config)
    imported, token = preview(store)
    assert not store.config_path.exists()
    verified = []
    adopt(store, token, lambda c, fd: verified.append(c), emit=lambda _: None)
    assert verified == [imported]
    assert store.json(store.applied_path)["adopted"] is True
    assert Engine(store).plan()[1] == []
    for name, data in before.items():
        assert (store.root / name).read_bytes() == data
        assert (store.directory / "imported" / name).is_file()
    changed = deepcopy(store.load())
    changed["users"].append("charlie")
    store.save(changed)
    names = [step.name for step in Engine(store).plan()[1]]
    assert names == ["clients", "verify", "acceptance"]


def test_failed_verification_does_not_claim_adoption(store, config):
    before = existing(store, config)
    _, token = preview(store)
    def fail(*_):
        raise RuntimeError("health failed")
    with pytest.raises(RuntimeError, match="health failed"):
        adopt(store, token, fail, emit=lambda _: None)
    assert not store.config_path.exists()
    assert not store.applied_path.exists()
    assert not (store.directory / "adoption.json").exists()
    assert all((store.root / n).read_bytes() == b for n, b in before.items())


def test_adoption_rechecks_inputs_after_health(store, config):
    existing(store, config)
    _, token = preview(store)
    def edit(*_):
        (store.root / "users.txt").write_text("alice\nbob\ncharlie\n")
    with pytest.raises(ValueError, match="changed during verification"):
        adopt(store, token, edit, emit=lambda _: None)
    assert not store.config_path.exists()


@pytest.mark.parametrize("config_written", [False, True])
def test_interrupted_adoption_can_resume_without_apply(store, config, config_written):
    existing(store, config)
    imported, token = preview(store)
    store.write_json(store.directory / "adoption.json", {"status": "preparing", "token": token})
    if config_written:
        store.write_json(store.config_path, imported)
    with pytest.raises(ValueError, match="interrupted adoption"):
        Engine(store).plan(candidate=imported)
    adopt(store, token, lambda *_: None, emit=lambda _: None)
    assert Engine(store).plan()[1] == []
    assert fingerprint(store.load()) == fingerprint(imported)


def test_adoption_refuses_semantic_normalization_and_missing_access(store, config):
    existing(store, config)
    from stackctl.envio import update_env
    update_env(store.root / ".env", {"PUBLIC_DOMAIN": "WWW.EXAMPLE.TEST"})
    with pytest.raises(ValueError, match="normalization"):
        preview(store)
    update_env(store.root / ".env", {"PUBLIC_DOMAIN": "www.example.test", "PANEL_PASS": ""})
    with pytest.raises(ValueError, match="no credentials will be generated"):
        preview(store)
