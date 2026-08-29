#!/bin/sh
set -eu

fail() {
    printf 'render-install: %s\n' "$*" >&2
    exit 1
}

root=$(git rev-parse --show-toplevel 2>/dev/null) || fail 'run from the ops Git repository'
cd "$root"

template=script/install.sh
fingerprint_file=internal/release/signing-fingerprint
key_file=internal/release/signing-key.asc
output=${1:-dist/install}

for command in cat gpg awk grep mkdir mktemp dirname chmod mv rm sh; do
    command -v "$command" >/dev/null 2>&1 || fail "$command is required"
done

[ -f "$template" ] && [ ! -L "$template" ] || fail 'installer template is not a safe regular file'
[ -f "$fingerprint_file" ] && [ ! -L "$fingerprint_file" ] || fail 'signing fingerprint file is not a safe regular file'
[ -f "$key_file" ] && [ ! -L "$key_file" ] || fail 'signing public key file is not a safe regular file'

fingerprint=$(cat "$fingerprint_file")
case "$fingerprint" in
    ''|*[!0-9A-F]*) fail 'invalid signing fingerprint' ;;
esac
[ "${#fingerprint}" -eq 40 ] || fail 'invalid signing fingerprint'

shown=$(gpg --no-options --batch --no-tty --with-colons --show-keys "$key_file" 2>/dev/null) || fail 'invalid signing public key'
printf '%s\n' "$shown" | awk -F: -v fingerprint="$fingerprint" '
    $1 == "sub" { active=($2 != "r" && $2 != "e" && tolower($12) ~ /s/); next }
    active && $1 == "fpr" && toupper($10) == fingerprint { found=1 }
    $1 != "fpr" { active=0 }
    END { exit !found }
' || fail 'pinned signing subkey is absent, expired, revoked, or not signing-capable'

output_dir=$(dirname "$output")
mkdir -p "$output_dir"
tmp=$(mktemp "$output.tmp.XXXXXXXX") || fail 'could not create temporary output'
cleanup() {
    rm -f -- "$tmp"
}
trap cleanup EXIT HUP INT TERM

fingerprint_marker="fingerprint='@OPS_SIGNING_FINGERPRINT@'"
key_marker='@OPS_SIGNING_PUBLIC_KEY@'

awk \
    -v fingerprint="$fingerprint" \
    -v fingerprint_marker="$fingerprint_marker" \
    -v key_marker="$key_marker" \
    -v key_file="$key_file" '
    $0 == fingerprint_marker {
        printf "fingerprint=\047%s\047\n", fingerprint
        fingerprints++
        next
    }
    index($0, "@OPS_SIGNING_FINGERPRINT@") &&
    index($0, "release signing trust is not configured") {
        guards++
        next
    }
    $0 == key_marker {
        while ((getline line < key_file) > 0) print line
        close(key_file)
        keys++
        next
    }
    { print }
    END {
        if (fingerprints != 1 || guards != 1 || keys != 1) exit 2
    }
' "$template" > "$tmp" || fail 'template trust markers are invalid'

if grep -q '@OPS_SIGNING_' "$tmp"; then
    fail 'unresolved trust marker remains in rendered installer'
fi

sh -n "$tmp" || fail 'rendered installer is not valid POSIX shell'
chmod 0755 "$tmp"
mv -- "$tmp" "$output"
trap - EXIT HUP INT TERM

printf 'Rendered installer: %s\n' "$output"
