#!/usr/bin/env python3
"""Prepare a private environment once, then launch the console.

Only the standard library is imported here. This file deliberately parses on
older Python versions so that an unsupported interpreter gets a useful error.
"""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]


def environment_python(root):
    return root / ".venv" / ("Scripts/python.exe" if os.name == "nt" else "bin/python")


def signature(root):
    return hashlib.sha256(str(root).encode() + (root / "pyproject.toml").read_bytes()).hexdigest()


def ready(python, root):
    # Check actual imports/version as well as the marker. A half-installed or
    # manually damaged environment must be repairable by the same command.
    import tomllib
    project = tomllib.loads((root / "pyproject.toml").read_text(encoding="utf-8"))["project"]
    textual_version = next(s.split("==", 1)[1] for s in project["dependencies"] if s.startswith("textual=="))
    result = subprocess.run(
        [str(python), "-c", "import importlib.metadata as m; import textual; import stackctl.ui; "
         "import sys; assert m.version('textual') == sys.argv[1]; assert m.version(sys.argv[2]) == sys.argv[3]",
         textual_version, project["name"], project["version"]],
        cwd=root, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    return result.returncode == 0


def create_environment(root):
    try:
        import venv
        venv.EnvBuilder(with_pip=True).create(root / ".venv")
    except (ImportError, OSError, subprocess.CalledProcessError, SystemExit):
        # Debian/Ubuntu split ensurepip out of the Python package. Install the
        # matching support package only for their system interpreter as root.
        # Their patched venv can call sys.exit() instead of raising an error.
        package = "python%d.%d-venv" % sys.version_info[:2]
        can_install = (sys.platform == "linux" and os.geteuid() == 0
                       and Path("/etc/debian_version").exists()
                       and str(Path(sys.executable).resolve()).startswith("/usr/bin/")
                       and shutil.which("apt-get"))
        if not can_install:
            raise RuntimeError("Cannot create the Python environment. On Debian/Ubuntu install "
                               + package + ", then run the launcher again.") from None
        print("Installing Python environment support…", flush=True)
        subprocess.run(["apt-get", "update"], check=True)
        subprocess.run(["apt-get", "install", "-y", package], check=True)
        import venv
        venv.EnvBuilder(with_pip=True).create(root / ".venv")


def prepare(root, lock_fd=None):
    python = environment_python(root)
    marker = root / ".venv" / ".stack-ready"
    expected = signature(root)
    if python.is_file() and marker.is_file() and marker.read_text(encoding="utf-8") == expected and ready(python, root):
        return python
    print("Preparing the console for this checkout (first run or dependency change)…", flush=True)
    # Re-running venv creation is safe after interruption and restores pip if
    # setup was interrupted before its installation completed.
    if not python.is_file():
        create_environment(root)
    else:
        pip = subprocess.run([str(python), "-m", "pip", "--version"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if pip.returncode:
            create_environment(root)
    marker.unlink(missing_ok=True)
    kwargs = {"pass_fds": (lock_fd,)} if os.name != "nt" and lock_fd is not None else {}
    subprocess.run([str(python), "-m", "pip", "install", "--disable-pip-version-check", "--no-input", "-e", str(root)], cwd=root, check=True, **kwargs)
    if not ready(python, root):
        raise RuntimeError("Dependencies could not be verified. Run the launcher again to retry setup.")
    temporary = marker.with_suffix(".tmp")
    temporary.write_text(expected, encoding="utf-8")
    os.replace(temporary, marker)
    print("Console ready. Future launches reuse this environment.", flush=True)
    return python


def main(argv=None):
    if sys.version_info < (3, 11):
        print("Python 3.11+ is required; this interpreter is %s. Install a supported Python and run again." % sys.version.split()[0], file=sys.stderr)
        return 1
    # Use the same OS lock as deployment/configuration, without importing the
    # package we are about to install. The lock file must never be unlinked.
    directory = ROOT / ".stack"
    directory.mkdir(mode=0o700, exist_ok=True)
    with open(directory / "lock", "a+b") as lock:
        try:
            if os.name == "nt":
                import msvcrt
                lock.seek(0); lock.write(b"0"); lock.flush(); lock.seek(0)
                msvcrt.locking(lock.fileno(), msvcrt.LK_NBLCK, 1)
            else:
                import fcntl
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except OSError:
            print("Another stack process is preparing or applying. Wait for it, then run again.", file=sys.stderr)
            return 1
        try:
            python = prepare(ROOT, lock.fileno())
        except KeyboardInterrupt:
            print("\nSetup stopped. Run the same command to continue.", file=sys.stderr)
            return 130
        except (OSError, RuntimeError, subprocess.CalledProcessError) as error:
            print("Setup failed: %s\nRun the same command to retry. Your server configuration was not changed." % error, file=sys.stderr)
            return 1
        finally:
            if os.name == "nt":
                lock.seek(0); msvcrt.locking(lock.fileno(), msvcrt.LK_UNLCK, 1)
            else:
                fcntl.flock(lock, fcntl.LOCK_UN)
    command = [str(python), "-m", "stackctl", "--root", str(ROOT)] + list(sys.argv[1:] if argv is None else argv)
    # Replace the bootstrap on Unix: Ctrl+C/Q belongs to the application and
    # its process-group handling, rather than a waiting parent launcher.
    if os.name != "nt":
        os.execv(str(python), command)
    try:
        return subprocess.call(command, cwd=ROOT)
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
