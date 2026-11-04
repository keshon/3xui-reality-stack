#!/usr/bin/env bash
# Experimental controller binary installation only. Does not deploy a stack.
set -euo pipefail
umask 077

fail() { printf '%s\n' "$1" >&2; exit 1; }
usage() {
    printf '%s\n' 'Usage: bash install-controller.sh --version v0.1.0-go.1 [--prefix /opt/stackctl-go] [--root /opt/xray-stack] [--inspect]'
}

version= prefix=/opt/stackctl-go root=/opt/xray-stack inspect=0
while (($#)); do
    case "$1" in
        --version|--prefix|--root)
            (($# >= 2)) || fail 'Missing option value.'
            case "$1" in
                --version) version=$2 ;;
                --prefix) prefix=$2 ;;
                --root) root=$2 ;;
            esac
            shift 2 ;;
        --inspect) inspect=1; shift ;;
        --help|-h) usage; exit 0 ;;
        *) fail 'Unsupported bootstrap option.' ;;
    esac
done
[[ ${#version} -le 80 && $version =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]] || fail 'An explicit release version is required.'
for path in "$prefix" "$root"; do
    [[ $path == /* && $path != / && $path != *[[:cntrl:]]* && $path != */../* && $path != */.. && $path != */./* && $path != */. && $path != *//* ]] || fail 'Use absolute paths without dot segments or control characters.'
done
prefix=${prefix%/}
root=${root%/}
[[ $prefix != "$root" && $prefix != "$root/"* ]] || fail 'Install the controller outside the deployment checkout.'
[[ $(uname -s) == Linux ]] || fail 'The controller bootstrap supports Linux only.'
case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) fail 'Unsupported Linux architecture.' ;;
esac
for tool in curl sha256sum awk mktemp mkdir chmod mv rm rmdir; do
    command -v "$tool" >/dev/null || fail 'Required bootstrap utility is missing.'
done

# Reject symlinks anywhere in the installation prefix before creating files.
check_prefix() {
    local component= rest=${prefix#/} part
    while [[ -n $rest ]]; do
        part=${rest%%/*}
        component=$component/$part
        [[ ! -L $component ]] || fail 'The installation prefix contains a symlink.'
        if [[ $rest == */* ]]; then rest=${rest#*/}; else rest=; fi
    done
}
check_prefix

asset=stackctl-go_linux_$arch
temporary= stage= lock_owned=0
cleanup() {
    if [[ -n $temporary ]]; then
        rm -f -- "$temporary/$asset" "$temporary/SHA256SUMS"
        rmdir -- "$temporary"
    fi
    if [[ -n $stage ]]; then
        rm -f -- "$stage/stackctl-go" "$stage/SHA256SUMS"
        rmdir -- "$stage"
    fi
    if ((lock_owned)); then rmdir -- "$prefix/.install-lock"; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
temporary=$(mktemp -d)
base=https://github.com/keshon/3xui-reality-stack/releases/download/controller-$version
curl --proto '=https' --proto-redir '=https' --tlsv1.2 --location --fail --silent --show-error --max-time 120 --retry 2 --max-filesize 65536 --output "$temporary/SHA256SUMS" "$base/SHA256SUMS"
expected=$(awk -v asset="$asset" '$2 == asset { if (NF != 2 || length($1) != 64 || $1 ~ /[^0-9a-f]/) exit 1; count++; hash=$1 } END { if (count != 1) exit 1; print hash }' "$temporary/SHA256SUMS") || fail 'Release checksum entry is missing, duplicated or invalid.'
curl --proto '=https' --proto-redir '=https' --tlsv1.2 --location --fail --silent --show-error --max-time 120 --retry 2 --max-filesize 134217728 --output "$temporary/$asset" "$base/$asset"
actual=$(cd "$temporary" && sha256sum "$asset")
[[ ${actual%% *} == "$expected" ]] || fail 'Controller checksum verification failed.'
chmod 0755 "$temporary/$asset"
[[ $("$temporary/$asset" version) == "$version" ]] || fail 'Controller version does not match the requested release.'

check_prefix
mkdir -p -- "$prefix"
mkdir -- "$prefix/.install-lock" 2>/dev/null || fail 'Another installation holds the controller install lock; inspect it before retrying.'
lock_owned=1
target=$prefix/$version
[[ ! -L $target ]] || fail 'Refusing a symlinked release directory.'
if [[ -e $target ]]; then
    [[ -d $target && -f $target/stackctl-go && -x $target/stackctl-go && ! -L $target/stackctl-go ]] || fail 'Existing release directory is not a controller installation.'
    actual=$(cd "$target" && sha256sum stackctl-go)
    [[ ${actual%% *} == "$expected" ]] || fail 'Existing release differs; refusing to overwrite it.'
    [[ -f $target/SHA256SUMS && ! -L $target/SHA256SUMS && $(< "$target/SHA256SUMS") == "$expected  stackctl-go" ]] || fail 'Existing release checksum manifest differs; refusing to overwrite it.'
else
    stage=$(mktemp -d "$prefix/.install.XXXXXX")
    mv -- "$temporary/$asset" "$stage/stackctl-go"
    printf '%s  stackctl-go\n' "$expected" > "$stage/SHA256SUMS"
    chmod 0755 "$stage"
    mv -T -- "$stage" "$target"
    stage=
fi
printf 'Controller available: %s\n' "$target/stackctl-go"
if ((inspect)); then
    "$target/stackctl-go" inspect --root "$root"
else
    printf 'Inspect an existing checkout with: %q inspect --root %q\n' "$target/stackctl-go" "$root"
fi
