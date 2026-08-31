#!/usr/bin/env python3
"""Print the resolved account list, one comma-separated line.

users.txt is the editable input; USERS= in .env is what every other script and
the panel actually read. install.sh runs this and writes the answer back into
USERS=, so the two can never disagree - and an unusable name is rejected here,
before it becomes a half-provisioned account.

    python3 scripts/resolve-users.py          # keshon,icehead,...
    python3 scripts/resolve-users.py --list   # one per line, with the source
"""
import sys

from panel import BadUsername, env_file, users, users_file


def main():
    e = env_file()
    try:
        people = users(e)
    except BadUsername as ex:
        sys.exit(f"users list: {ex}")
    except OSError as ex:
        sys.exit(f"users list: {ex}")

    if not people:
        sys.exit("users list is empty - fill in users.txt or USERS= in .env")

    if "--list" in sys.argv:
        src = users_file(e) or f"{'.env'} (USERS=)"
        print(f"{len(people)} account(s) from {src}")
        for who in people:
            print(f"  {who}")
    else:
        print(",".join(people))


if __name__ == "__main__":
    main()
