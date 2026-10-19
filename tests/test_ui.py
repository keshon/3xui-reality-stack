import pytest
from textual.widgets import Input, Static, TextArea

from stackctl.ui import StackApp, Settings, Users, Review, Nodes
from stackctl.engine import Engine


async def test_settings_validation_and_literal_secrets(store, config):
    store.save(config)
    app = StackApp(store)
    async with app.run_test(size=(100, 40)) as pilot:
        await pilot.click("#settings")
        await pilot.pause()
        assert isinstance(app.screen, Settings)
        app.screen.query_one("#field-PANEL_PATH", Input).value = "new-panel"
        app.screen.query_one("#field-PANEL_PASS", Input).value = "literal '$HOME' \\ password "
        await pilot.pause()
        assert store.json(store.directory / "draft.json")["config"]["env"]["PANEL_PASS"] == "literal '$HOME' \\ password "
        # Activate Save directly: access fields are in a different tab.
        app.screen.save()
        await pilot.pause()
        assert store.load()["env"]["PANEL_PATH"] == "/new-panel"
        assert store.load()["env"]["PANEL_PASS"] == "literal '$HOME' \\ password "
        assert not (store.root / ".env").exists()


async def test_back_preserves_draft_and_reopen_restores(store, config):
    store.save(config)
    app = StackApp(store)
    async with app.run_test(size=(90, 30)) as pilot:
        await pilot.click("#settings")
        app.screen.query_one("#field-PUBLIC_DOMAIN", Input).value = "new.example.test"
        await pilot.pause()
        await pilot.press("escape")
        await pilot.click("#settings")
        assert app.screen.query_one("#field-PUBLIC_DOMAIN", Input).value == "new.example.test"


async def test_users_editor_and_review_no_deploy(store, config):
    store.save(config)
    app = StackApp(store)
    async with app.run_test(size=(90, 32)) as pilot:
        await pilot.click("#users")
        app.screen.query_one("#people", TextArea).text = "alice\nbob\ncarol"
        app.screen.save()
        await pilot.pause()
        await pilot.click("#review")
        assert isinstance(app.screen, Review)
        assert "Deployment plan" in str(app.screen.query_one("#plan", Static).render())
        assert not store.journal_path.exists()


async def test_small_terminal_and_node_form(store, config):
    config["env"]["ROLE"] = "master"
    store.save(config)
    app = StackApp(store)
    async with app.run_test(size=(80, 24)) as pilot:
        await pilot.click("#nodes")
        assert isinstance(app.screen, Nodes)
        for key, value in {"name": "de", "url": "ops-de.example.test/panel", "token": "node-secret", "domain": "www-de.example.test"}.items():
            app.screen.query_one("#node-" + key, Input).value = value
        app.screen.save()
        await pilot.pause()
        assert store.load()["nodes"][0]["url"] == "https://ops-de.example.test/panel"


async def test_failed_operation_can_unlock_settings_from_review(store, config):
    store.save(config)
    def fail(*_): raise RuntimeError("DNS mismatch")
    with pytest.raises(RuntimeError):
        Engine(store, fail).apply(lambda _: None)
    app = StackApp(store)
    async with app.run_test(size=(90, 32)) as pilot:
        assert app.screen.query_one("#settings").disabled
        await pilot.click("#review")
        assert app.screen.query_one("#recover").display
        app.screen.recover()
        await pilot.pause()
        assert not app.screen.query_one("#settings").disabled
        await pilot.click("#settings")
        app.screen.query_one("#field-PUBLIC_DOMAIN", Input).value = "fixed.example.test"
        app.screen.save()
        await pilot.pause()
        assert store.load()["env"]["PUBLIC_DOMAIN"] == "fixed.example.test"


async def test_stop_safely_saves_interrupted_operation(store, config, monkeypatch):
    import threading
    import time
    import stackctl.ui as ui
    store.save(config)
    entered = threading.Event()
    engine = None
    def runner(*_):
        entered.set()
        while not engine.cancel.is_set():
            time.sleep(0.01)
        raise InterruptedError("Stopped by operator")
    engine = Engine(store, runner)
    monkeypatch.setattr(ui, "Engine", lambda _: engine)
    app = StackApp(store)
    async with app.run_test(size=(90, 32)) as pilot:
        await pilot.click("#review")
        app.screen.start()
        for _ in range(40):
            await pilot.pause(0.05)
            if entered.is_set(): break
        assert entered.is_set()
        app.screen.stop()
        for _ in range(40):
            await pilot.pause(0.05)
            if not app.screen.running: break
        assert not app.screen.running
        assert store.json(store.journal_path)["status"] == "interrupted"
        assert app.active_engine is None
