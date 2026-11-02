"""English terminal UI. Forms edit drafts; only the reviewed plan can deploy."""
from __future__ import annotations

from copy import deepcopy

from textual import on, work
from textual.app import App, ComposeResult
from textual.containers import Horizontal, VerticalScroll
from textual.screen import Screen
from textual.widgets import Button, Checkbox, Footer, Header, Input, Label, Select, Static, TabbedContent, TabPane, TextArea, Log

from .config import Store, fingerprint
from .engine import Engine, plan_text, redactor


def field(key, label, env, secret=False, hint=""):
    yield Label(label)
    yield Input(env.get(key, ""), id="field-" + key, password=secret)
    if hint:
        yield Static(hint, classes="hint", markup=False)


class Home(Screen):
    def compose(self) -> ComposeResult:
        yield Header()
        with VerticalScroll(id="body"):
            yield Static("3x-ui stack", classes="title")
            yield Static("Configure this host, review changes, then apply. Nothing runs in the background.", classes="hint")
            yield Static(id="summary", markup=False)
            yield Button("Server settings", id="settings")
            yield Button("Users", id="users")
            yield Button("Nodes", id="nodes")
            yield Button("Review changes / resume", id="review", variant="primary")
            yield Button("Check live health", id="doctor")
            yield Static(id="error", classes="error", markup=False)
        yield Footer()

    def on_screen_resume(self):
        self.refresh_summary()

    def on_mount(self):
        self.refresh_summary()

    def refresh_summary(self):
        try:
            config = self.app.store.load()
            journal = self.app.store.json(self.app.store.journal_path, {})
            pending = bool(journal and journal.get("status") not in ("complete", "editable"))
            draft = self.app.store.json(self.app.store.directory / "draft.json", {})
            env = config["env"]
            text = f"Role: {env.get('ROLE', 'standalone')}\nPublic host: {env.get('PUBLIC_DOMAIN') or 'not configured'}\n{len(config['users'])} user(s) · {len(config['nodes'])} node(s)"
            if pending:
                text += f"\nUnfinished operation: {journal['status']} · {journal.get('current', 'not started')}\nReview to resume, or unlock settings for recovery."
            elif journal.get("status") == "editable":
                text += "\nRecovery mode: correct settings, then review a full recheck. Existing server changes are kept."
            elif draft:
                text += "\nAn unfinished settings draft is available. Open Server settings to continue."
            self.query_one("#summary", Static).update(text)
            for name in ("settings", "users", "nodes"):
                self.query_one("#" + name, Button).disabled = pending or (name == "nodes" and env.get("ROLE") != "master") or (name == "users" and env.get("ROLE") == "node")
        except (ValueError, OSError) as error:
            self.query_one("#error", Static).update(str(error))

    @on(Button.Pressed)
    def choose(self, event):
        name = event.button.id
        try:
            self.app.store.load()
        except (ValueError, OSError) as error:
            self.query_one("#error", Static).update(str(error))
            return
        if name == "settings":
            self.app.push_screen(Settings())
        elif name == "users":
            self.app.push_screen(Users())
        elif name == "nodes":
            self.app.push_screen(Nodes())
        elif name == "review":
            self.app.push_screen(Review())
        elif name == "doctor":
            self.app.push_screen(Review(doctor=True))


class Settings(Screen):
    BINDINGS = [("escape", "back", "Back (keep draft)")]

    def compose(self):
        self.original = self.app.store.load()
        self.revision = fingerprint(self.original)
        draft = self.app.store.json(self.app.store.directory / "draft.json", {})
        self.config = deepcopy(draft.get("config") if draft.get("base") == self.revision else self.original)
        env = self.config["env"]
        yield Header()
        with VerticalScroll(id="body"):
            yield Static("Server settings", classes="title")
            yield Static("Drafts are saved locally as you type. Save validates settings; it does not deploy.", classes="hint")
            with TabbedContent():
                with TabPane("Server"):
                    yield Label("Host role")
                    yield Select([("Single server", "standalone"), ("Master + traffic", "master"), ("Traffic node", "node")], value=env.get("ROLE", "standalone"), allow_blank=False, id="role")
                    for key, label in (("BASE_DOMAIN", "DNS zone"), ("PUBLIC_DOMAIN", "Public hostname"), ("PANEL_DOMAIN", "Admin hostname"), ("LETSENCRYPT_EMAIL", "Certificate email")):
                        yield from field(key, label, env)
                    yield from field("CF_DNS_API_TOKEN", "Cloudflare DNS token", env, True)
                    yield Static("Traffic nodes receive users from the master. DNS must point here before Apply.", classes="hint")
                with TabPane("Access"):
                    yield Checkbox("Show access values", id="show-access")
                    yield from field("PANEL_USER", "Panel login", env, True, "Enter the exact value. Leave empty to generate on Save.")
                    yield from field("PANEL_PASS", "Panel password", env, True, "Paste literally, including $, quotes and backslashes. No escaping needed.")
                    for key, label in (("PANEL_PATH", "Panel path"), ("SUB_PATH", "Subscription path"), ("JSON_PATH", "JSON subscription path")):
                        yield from field(key, label, env, True, "Leading / is added automatically. Leave empty to generate on Save.")
                with TabPane("Protocols"):
                    for key, label in (("TROJAN_PATH", "Trojan path"), ("REALITY_FINGERPRINT", "Reality fingerprint"), ("MTPROTO_PORT", "MTProto TCP port"), ("AWG_PORT", "AmneziaWG UDP port"), ("AWG_MTU", "AmneziaWG MTU"), ("LANES", "Enabled lanes"), ("USER_LIMIT_GB", "Traffic per user (GiB, 0 = unlimited)"), ("USER_EXPIRY_DAYS", "Lifetime of NEW users (days, 0 = never)")):
                        yield from field(key, label, env)
                with TabPane("Advanced"):
                    for key, label in (("ACME_MODE", "Certificate challenge (dns / http)"), ("CERT_BASENAME", "Certificate file basename"), ("PROXY_SUBNET", "Docker proxy subnet"), ("AWG_SUBNET", "AmneziaWG subnet address"), ("MANAGEMENT_BIND", "Management bind address (blank = automatic)"), ("MANAGEMENT_PORT", "Management HTTPS port (default 2053)")):
                        yield from field(key, label, env)
                    yield Static("Existing unknown .env settings are preserved. Live domain/role migrations require the runbook.", classes="hint")
            yield Static(id="error", classes="error", markup=False)
            with Horizontal(classes="actions"):
                yield Button("Save settings", id="save", variant="primary")
                yield Button("Back", id="back")
        yield Footer()

    def capture(self):
        for widget in self.query(Input):
            self.config["env"][widget.id.removeprefix("field-")] = widget.value
        self.config["env"]["ROLE"] = str(self.query_one("#role", Select).value)

    @on(Checkbox.Changed, "#show-access")
    def reveal(self, event):
        for key in ("PANEL_USER", "PANEL_PASS", "PANEL_PATH", "SUB_PATH", "JSON_PATH"):
            self.query_one("#field-" + key, Input).password = not event.value

    @on(Input.Changed)
    @on(Select.Changed)
    def remember(self):
        if not self.is_mounted:
            return
        self.capture()
        try:
            with self.app.store.lock():
                pending = self.app.store.json(self.app.store.journal_path, {})
                if pending and pending.get("status") not in ("complete", "editable"):
                    return
                self.app.store.write_json(self.app.store.directory / "draft.json", {"base": self.revision, "config": self.config})
        except (OSError, ValueError) as error:
            self.query_one("#error", Static).update(str(error))

    @on(Button.Pressed, "#save")
    def save(self):
        self.capture()
        candidate = deepcopy(self.config)
        if candidate["env"]["ROLE"] == "node":
            candidate["users"] = []
        try:
            self.app.store.save(candidate, expected=self.revision)
            with self.app.store.lock():
                (self.app.store.directory / "draft.json").unlink(missing_ok=True)
            self.app.pop_screen()
        except (ValueError, OSError) as error:
            self.query_one("#error", Static).update(str(error))

    @on(Button.Pressed, "#back")
    def action_back(self):
        self.app.pop_screen()


class Users(Screen):
    BINDINGS = [("escape", "back", "Back")]

    def compose(self):
        self.config = self.app.store.load()
        self.revision = fingerprint(self.config)
        yield Header()
        with VerticalScroll(id="body"):
            yield Static("Users", classes="title")
            yield Static("One name per line: lowercase letters, digits, underscore; up to 24 characters. Removing a managed name schedules an explicit revocation in the plan.", classes="hint")
            yield TextArea("\n".join(self.config["users"]), id="people")
            yield Static(id="error", classes="error", markup=False)
            with Horizontal(classes="actions"):
                yield Button("Save users", id="save", variant="primary")
                yield Button("Back", id="back")
        yield Footer()

    @on(Button.Pressed, "#save")
    def save(self):
        try:
            self.config["users"] = [line.strip() for line in self.query_one("#people", TextArea).text.splitlines() if line.strip()]
            self.app.store.save(self.config, expected=self.revision)
            self.app.pop_screen()
        except (ValueError, OSError) as error:
            self.query_one("#error", Static).update(str(error))

    @on(Button.Pressed, "#back")
    def action_back(self):
        self.app.pop_screen()


class Nodes(Screen):
    BINDINGS = [("escape", "back", "Back")]

    def compose(self):
        self.config = self.app.store.load()
        self.revision = fingerprint(self.config)
        yield Header()
        with VerticalScroll(id="body"):
            yield Static("Nodes", classes="title")
            yield Static("Deploy each node first, then add its management URL: https://ops-node.example.com:2053/panel-path. This stays reachable when Xray is down. Replacement/retirement use the runbook.", classes="hint")
            yield Select([("New node", "new"), *[(n["name"], n["name"]) for n in self.config["nodes"]]], value="new", allow_blank=False, id="node-picker")
            for name, label in (("name", "Stable node name"), ("url", "Panel URL (include its panel path)"), ("token", "Node API token"), ("domain", "Public hostname")):
                yield Label(label)
                yield Input(id="node-" + name, password=name in ("url", "token"))
            yield Static(id="error", classes="error", markup=False)
            with Horizontal(classes="actions"):
                yield Button("Save node", id="save", variant="primary")
                yield Button("Back", id="back")
        yield Footer()

    @on(Select.Changed, "#node-picker")
    def choose(self, event):
        if not self.is_mounted:
            return
        node = next((n for n in self.config["nodes"] if n["name"] == event.value), {})
        for key in ("name", "url", "token", "domain"):
            self.query_one("#node-" + key, Input).value = node.get(key, "")
        # Stable names bind existing inbounds. Renaming is migration, not editing.
        self.query_one("#node-name", Input).disabled = event.value != "new"

    @on(Button.Pressed, "#save")
    def save(self):
        try:
            node = {k: self.query_one("#node-" + k, Input).value for k in ("name", "url", "token", "domain")}
            selection = self.query_one("#node-picker", Select).value
            if selection == "new" and any(n["name"] == node["name"] for n in self.config["nodes"]):
                raise ValueError("This node name already exists. Select it above to update its token.")
            self.config["nodes"] = [n for n in self.config["nodes"] if n["name"] != selection] + [node]
            self.app.store.save(self.config, expected=self.revision)
            self.app.pop_screen()
        except (ValueError, OSError) as error:
            self.query_one("#error", Static).update(str(error))

    @on(Button.Pressed, "#back")
    def action_back(self):
        self.app.pop_screen()


class Review(Screen):
    BINDINGS = [("escape", "back", "Back")]

    def __init__(self, doctor=False):
        super().__init__()
        self.doctor = doctor
        self.engine = None
        self.running = False

    def compose(self):
        self.engine = Engine(self.app.store)
        yield Header()
        with VerticalScroll(id="body"):
            yield Static("Live health check" if self.doctor else "Review deployment", classes="title")
            yield Static(id="plan", markup=False)
            yield Log(id="progress", auto_scroll=True, max_lines=500)
            yield Static(id="error", classes="error", markup=False)
            with Horizontal(classes="actions"):
                yield Button("Run checks" if self.doctor else "Apply reviewed plan", id="apply", variant="primary")
                yield Button("Stop safely", id="stop", disabled=True)
                yield Button("Back", id="back")
            yield Static("Recovery keeps server changes, archives checkpoints, and unlocks settings. The next apply rechecks all stages; it does not roll changes back.", classes="hint", id="recovery-help")
            yield Button("Unlock settings for recovery", id="recover")
        yield Footer()

    def on_mount(self):
        try:
            text = "Read-only panel, process, fallback HTTPS and authenticated Xray transfer checks. MTProto/AmneziaWG remain untested." if self.doctor else plan_text(self.engine)
            self.query_one("#plan", Static).update(text)
            pending = self.app.store.json(self.app.store.journal_path, {})
            self.query_one("#recover", Button).display = bool(not self.doctor and pending and pending.get("status") not in ("complete", "editable"))
            self.query_one("#recovery-help", Static).display = self.query_one("#recover", Button).display
            if not self.doctor:
                config, steps, revoke = self.engine.plan()
                # Bind the displayed plan to this exact revision. Applying may
                # not silently use edits from another terminal.
                self.reviewed = fingerprint(config)
                pending = self.app.store.json(self.app.store.journal_path, {})
                if not steps and pending.get("status", "complete") == "complete":
                    self.query_one("#apply", Button).disabled = True
                if revoke:
                    self.query_one("#apply", Button).label = "Apply and revoke listed users"
        except (ValueError, OSError) as error:
            self.query_one("#error", Static).update(str(error))
            self.query_one("#apply", Button).disabled = True

    @on(Button.Pressed, "#apply")
    def start(self):
        if self.running:
            return
        self.running = True
        for name in ("apply", "back"):
            self.query_one("#" + name, Button).disabled = True
        self.query_one("#stop", Button).disabled = False
        self.query_one("#recover", Button).disabled = True
        self.app.active_engine = self.engine
        self.execute()

    @work(thread=True, exit_on_error=False)
    def execute(self):
        def emit(text):
            self.app.call_from_thread(self.query_one("#progress", Log).write_line, text)
        try:
            if self.doctor:
                from .cli import doctor
                doctor(self.app.store, emit, engine=self.engine)
            else:
                self.engine.apply(emit, expected=self.reviewed)
        except Exception as error:
            try:
                safe = redactor(self.app.store.load())(str(error))
            except Exception:
                safe = "Operation failed. Review the saved local log."
            self.app.call_from_thread(self.query_one("#error", Static).update, safe)
        finally:
            self.app.call_from_thread(self.finished)

    def finished(self):
        self.running = False
        self.app.active_engine = None
        self.query_one("#stop", Button).disabled = True
        self.query_one("#back", Button).disabled = False
        pending = self.app.store.json(self.app.store.journal_path, {})
        recover = self.query_one("#recover", Button)
        recover.display = bool(not self.doctor and pending and pending.get("status") not in ("complete", "editable"))
        recover.disabled = False
        self.query_one("#recovery-help", Static).display = recover.display
        if self.app.quit_after_stop:
            self.app.exit()

    @on(Button.Pressed, "#stop")
    def stop(self):
        self.engine.cancel.set()
        self.query_one("#progress", Log).write_line("Stopping this step. Progress will be saved; use Resume on your next visit.")

    @on(Button.Pressed, "#recover")
    def recover(self):
        try:
            self.engine.recover()
            self.app.pop_screen()
        except (OSError, ValueError) as error:
            self.query_one("#error", Static).update(str(error))

    @on(Button.Pressed, "#back")
    def action_back(self):
        if not self.running:
            self.app.pop_screen()


class StackApp(App):
    TITLE = "3x-ui stack"
    BINDINGS = [("ctrl+q", "quit", "Quit"), ("ctrl+c", "quit", "Quit")]
    CSS = """
    Screen { background: $surface; }
    #body { width: 92; max-width: 100%; height: 1fr; margin: 0 2; padding: 1 2; }
    .title { text-style: bold; margin-bottom: 1; }
    .hint { color: $text-muted; margin-bottom: 1; }
    #summary { margin-bottom: 1; }
    Label { margin-top: 1; }
    Input, Select { margin-bottom: 1; }
    Home Button { width: 36; margin-bottom: 1; }
    .actions { height: auto; margin-top: 1; }
    .actions Button { margin-right: 1; }
    .error { color: $error; height: auto; margin-top: 1; }
    #people { height: 12; }
    #progress { height: 12; border: solid $panel; margin-top: 1; }
    TabPane { padding: 1 0; }
    """

    def __init__(self, store: Store):
        super().__init__()
        self.store = store
        self.active_engine = None
        self.quit_after_stop = False

    def on_mount(self):
        self.push_screen(Home())

    def action_quit(self):
        if self.active_engine is not None:
            self.quit_after_stop = True
            self.active_engine.cancel.set()
        else:
            self.exit()
