"""Both the terminal UI and automation call the same configuration/engine."""
import argparse
import os
from pathlib import Path
import sys

from .config import Store, fingerprint
from .engine import Engine, Step, plan_text, redactor
from .upgrade import Upgrade, UpgradeError
from .adopt import adopt, preview as adoption_preview


def check_deployed(store, config, emit, engine):
    engine.command = lambda *_: [sys.executable, "scripts/managed-apply.py", "acceptance"]
    engine.run_process(Step("doctor", "Read deployed panel and subscription state", 600), config, emit)
    if config["env"].get("ROLE", "standalone") != "node":
        engine.command = lambda *_: [sys.executable, "-m", "stackctl.health", "--root", str(store.root)]
        engine.run_process(Step("health", "Check process, fallback HTTPS and authenticated transfers", 225), config, emit)
        emit("Panel, process, fallback HTTPS and authenticated Xray transfers passed. MTProto/AmneziaWG remain untested.")
    else:
        emit("Node management passed. Probe its traffic lanes from the master or an external host.")


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
    # Health checks must not race changes or user revocations.
    with store.lock() as fd:
        engine.lock_fd = fd
        check_deployed(store, deployed, emit, engine)


def main(argv=None):
    parser = argparse.ArgumentParser(description="Configure and deploy a 3x-ui host. Run without a command to open the console.")
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1], help="stack repository directory")
    parser.add_argument("command", nargs="?", choices=("wizard", "plan", "apply", "resume", "recover", "status", "doctor", "upgrade", "rollback", "adopt"), default="wizard")
    parser.add_argument("--yes", action="store_true", help="apply without an interactive confirmation after printing the plan")
    parser.add_argument("--from", dest="source", type=Path, help="separate clean release checkout or manifest directory for upgrade")
    parser.add_argument("--backup", type=Path, help="latest source-upgrade backup to roll back")
    args = parser.parse_args(argv)
    store = Store(args.root)
    try:
        if not (store.root / "install.sh").is_file():
            raise ValueError("--root must point to the 3xui-reality-stack checkout.")
        if args.command in ("upgrade", "rollback"):
            if sys.platform != "linux" or os.geteuid() != 0:
                raise UpgradeError("Source upgrades and their previews run on the deployed Linux host as root.")
            upgrade = Upgrade(store.root)
            if args.command == "upgrade":
                if not args.source:
                    raise UpgradeError("Use stack upgrade --from /path/to/separate-release.")
                plan = upgrade.plan(args.source)
                print(upgrade.describe(plan))
                if args.yes:
                    upgrade.apply(args.source, expected=plan["token"])
                else:
                    print("Preview only. Add --yes to apply this release after reviewing the plan.")
            else:
                if not args.backup:
                    raise UpgradeError("Use stack rollback --backup /path/from-the-upgrade-output.")
                directory = args.backup.resolve()
                if not directory.is_relative_to((store.directory / "upgrades").resolve()):
                    raise UpgradeError("Choose a backup under this deployment's .stack/upgrades directory.")
                record = store.json(directory / "rollback.json", {})
                if record.get("root") != str(store.root):
                    raise UpgradeError("Backup belongs to another deployment or is incomplete.")
                print("Restore source from: " + str(directory))
                print("Services to recreate: " + ", ".join(f"{p}/{s}" for p, names in record["services"].items() for s in names))
                print("Live database and deployment settings will be preserved.")
                if args.yes:
                    upgrade.rollback(directory)
                    print("Previous source/services restored and verified.")
                else:
                    print("Preview only. Add --yes to roll back.")
        elif args.command == "adopt":
            if sys.platform != "linux" or os.geteuid() != 0:
                raise ValueError("Adoption runs on the deployed Linux host as root.")
            config, revision = adoption_preview(store)
            print(f"Adopt existing inputs: {len(config['users'])} managed users, {len(config['nodes'])} nodes.")
            print("Other panel accounts remain unmanaged. No provisioning, restarts, host tuning or input rewrites.")
            if args.yes:
                engine = Engine(store)
                def checker(config, fd):
                    engine.lock_fd = fd
                    check_deployed(store, config, print, engine)
                adopt(store, revision, checker)
            else:
                print("Preview only. Add --yes to verify and adopt this deployment.")
        elif args.command == "wizard":
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
    except UpgradeError as error:
        print("Error: " + str(error), file=sys.stderr)
        return 1
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
