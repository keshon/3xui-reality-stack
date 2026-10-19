"""Both the terminal UI and automation call the same configuration/engine."""
import argparse
import os
from pathlib import Path
import sys

from .config import Store, fingerprint
from .engine import Engine, Step, plan_text, redactor


def doctor(store, emit=print, engine=None):
    config = store.load()
    if sys.platform != "linux" or os.geteuid() != 0:
        raise ValueError("Live checks run on the deployed Linux host as root.")
    if not (store.root / ".env").exists():
        raise ValueError("No deployed .env exists. Review and apply the configuration first.")
    engine = engine or Engine(store)
    # Use the deployed files, not un-applied edits, for health checks.
    from .envio import read_env
    deployed = {**config, "env": read_env(store.root / ".env")}
    step = Step("doctor", "Read deployed panel and subscription state", 600)
    engine.command = lambda *_: [sys.executable, "scripts/managed-apply.py", "acceptance"]
    # Health checks must not race changes or user revocations.
    with store.lock() as fd:
        engine.lock_fd = fd
        engine.run_process(step, deployed, emit)
    emit("Panel/configuration checks passed. UDP and client tunnel handshakes remain unverified.")


def main(argv=None):
    parser = argparse.ArgumentParser(description="Configure and deploy a 3x-ui host. Run without a command to open the console.")
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1], help="stack repository directory")
    parser.add_argument("command", nargs="?", choices=("wizard", "plan", "apply", "resume", "recover", "status", "doctor"), default="wizard")
    parser.add_argument("--yes", action="store_true", help="apply without an interactive confirmation after printing the plan")
    args = parser.parse_args(argv)
    store = Store(args.root)
    try:
        if not (store.root / "install.sh").is_file():
            raise ValueError("--root must point to the 3xui-reality-stack checkout.")
        if args.command == "wizard":
            from .ui import StackApp
            StackApp(store).run()
        elif args.command == "status":
            journal = store.json(store.journal_path, {})
            applied = store.json(store.applied_path, {})
            print("Configuration: " + ("saved" if store.config_path.exists() else "not saved; run stack wizard"))
            print("Last apply: " + applied.get("applied_at", "none"))
            print("Operation: " + journal.get("status", "none"))
            if journal and journal.get("status") != "complete":
                print("Next: stack resume (last step: " + journal.get("current", "not started") + ")")
        elif args.command == "plan":
            print(plan_text(Engine(store)))
        elif args.command == "recover":
            print("Recovery keeps existing server changes, archives checkpoints, and unlocks settings. The next Apply rechecks all deployment stages. This is not rollback.")
            if not args.yes and (not sys.stdin.isatty() or input("Type recover to continue: ") != "recover"):
                return 1
            Engine(store).recover()
            print("Settings unlocked. Open stack wizard, correct the input, then review and apply.")
        elif args.command in ("apply", "resume"):
            engine = Engine(store)
            journal = store.json(store.journal_path, {})
            if args.command == "resume" and (not journal or journal.get("status") == "complete"):
                raise ValueError("No unfinished operation. Use stack plan / stack apply.")
            print(plan_text(engine))
            config, _, _ = engine.plan()
            revision = fingerprint(config)
            if not args.yes:
                if not sys.stdin.isatty():
                    raise ValueError("Use --yes for a non-interactive apply.")
                if input("Apply this plan? Type apply: ") != "apply":
                    return 0
            engine.apply(expected=revision)
        elif args.command == "doctor":
            doctor(store)
    except KeyboardInterrupt:
        print("\nStopped. Any deployment progress is saved; run stack resume.", file=sys.stderr)
        return 130
    except Exception as error:
        try:
            message = redactor(store.load())(str(error))
        except Exception:
            message = "Could not read configuration. Check the local files and their permissions."
        print("Error: " + message, file=sys.stderr)
        return 1
    return 0
