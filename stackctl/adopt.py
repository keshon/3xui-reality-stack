"""Adopt existing inputs after live verification, without provisioning accounts."""
from .config import fingerprint, normalize
from .engine import Engine, now
from .envio import read_env


def preview(store):
    marker = store.json(store.directory / "adoption.json", {})
    pending = marker.get("status") == "preparing"
    if store.config_path.exists() and not pending:
        raise ValueError("This installation is already managed. Use stack plan / stack apply.")
    if not (store.root / ".env").is_file():
        raise ValueError("Adoption requires an existing deployed .env. Use stack wizard for a fresh installation.")
    deployed = read_env(store.root / ".env")
    for key in ("PANEL_USER", "PANEL_PASS", "PANEL_PATH", "SUB_PATH", "JSON_PATH", "PUBLIC_DOMAIN"):
        if not deployed.get(key):
            raise ValueError("Adoption needs existing deployed access settings; no credentials will be generated.")
    if store.json(store.journal_path, {}).get("status", "complete") != "complete":
        raise ValueError("Finish or recover the current configuration operation before adoption.")
    if store.json(store.directory / "upgrade.json", {}).get("status") in ("applying", "rolling_back", "rollback_failed"):
        raise ValueError("Finish the source upgrade or rollback before adoption.")
    config = normalize(store.load())
    # Existing values must keep their literal meaning. Defaults absent from
    # the old .env remain represented in the managed configuration only.
    for key, value in deployed.items():
        if key != "USERS" and config["env"].get(key) != value:
            raise ValueError(f"Existing setting {key} needs normalization; review it in the wizard before adoption.")
    outputs = store.output_hashes()
    token = fingerprint({"config": config, "outputs": outputs})
    if pending:
        if marker.get("token") != token:
            raise ValueError("Inputs changed during an interrupted adoption. Preserve and review the saved state.")
    else:
        Engine(store).plan(candidate=config)
    return config, token


def adopt(store, expected, checker, emit=print):
    with store.lock() as fd:
        config, token = preview(store)
        if token != expected:
            raise ValueError("Inputs changed after the adoption preview. Review again.")
        # Acceptance and health checks are read-only. A failed check must not
        # mark an unverified installation as successfully managed.
        checker(config, fd)
        verified, verified_token = preview(store)
        if verified_token != token:
            raise ValueError("Inputs changed during verification. Review again.")
        marker = store.directory / "adoption.json"
        store.write_json(marker, {"status": "preparing", "token": token})
        store.preserve_inputs()
        store.write_json(store.config_path, verified)
        store.write_json(store.applied_path, {"version": 1, "config": verified,
                         "output_hashes": store.output_hashes(), "applied_at": now(), "adopted": True})
        store.write_json(marker, {"status": "complete", "token": token})
        emit("Existing deployment adopted. No accounts, input files, services or kernel settings were changed.")
