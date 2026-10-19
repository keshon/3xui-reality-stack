#!/usr/bin/env bash
# Parse once with Python, transport as NUL-separated data; never eval/source .env.
load_stack_env() {
  local tmp k v
  tmp=$(mktemp) || return 1
  if ! python3 "$ROOT/scripts/config-env.py" --null > "$tmp"; then
    rm -f "$tmp"
    return 1
  fi
  while IFS= read -r -d '' k && IFS= read -r -d '' v; do
    export "$k=$v"
  done < "$tmp"
  rm -f "$tmp"
}
