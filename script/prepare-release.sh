#!/bin/sh
set -eu

required_go=go1.26.7

fail() {
    printf 'prepare-release: %s\n' "$*" >&2
    exit 1
}

usage() {
    printf '%s\n' 'usage: script/prepare-release.sh VERSION [CI_CANDIDATE_PATH_OR_HTTPS_URL]' >&2
    exit 2
}

[ "$#" -ge 1 ] && [ "$#" -le 2 ] || usage
version=$1
candidate=${2:-}
printf '%s\n' "$version" | awk -F. 'NF == 3 && $1 ~ /^[0-9]+$/ && $2 ~ /^[0-9]+$/ && $3 ~ /^[0-9]+$/ { ok=1 } END { exit !ok }' || usage
tag=v$version

for command in git go awk sha256sum cmp gpg mktemp install mv rm; do
    command -v "$command" >/dev/null 2>&1 || fail "$command is required"
done

root=$(git rev-parse --show-toplevel 2>/dev/null) || fail 'run from the ops Git repository'
cd "$root"
[ -z "$(git status --porcelain=v1 --untracked-files=all)" ] || fail 'repository is not clean'
head=$(git rev-parse HEAD)
tag_commit=$(git rev-parse --verify "refs/tags/$tag^{commit}" 2>/dev/null) || fail "exact intended tag $tag does not exist"
[ "$head" = "$tag_commit" ] || fail "$tag does not identify the current commit"
[ "$(git describe --tags --exact-match HEAD 2>/dev/null)" = "$tag" ] || fail "HEAD is not exactly tagged $tag"

fingerprint_file=internal/release/signing-fingerprint
[ -f "$fingerprint_file" ] && [ ! -L "$fingerprint_file" ] || fail 'repository signing fingerprint is not a safe regular file'
fingerprint=$(awk '
    NR == 1 && length($0) == 40 && $0 !~ /[^0-9A-F]/ {
        value=$0
        next
    }
    { invalid=1 }
    END {
        if (NR == 1 && !invalid) print value
        else exit 1
    }
' "$fingerprint_file") || fail 'repository signing fingerprint must contain exactly one uppercase 40-hex fingerprint'

export GOENV=off
export GOTOOLCHAIN=local
unset GOFLAGS GOOS GOARCH CGO_ENABLED
[ "$(go env GOVERSION)" = "$required_go" ] || fail "release compiler must be exactly $required_go"
case "$(go version)" in
    "go version $required_go "*) ;;
    *) fail "release compiler identity is not $required_go" ;;
esac

go mod verify || fail 'module verification failed'
test -z "$(gofmt -l .)" || fail 'Go source is not formatted'
go vet -mod=readonly ./... || fail 'go vet failed'
go test -mod=readonly -count=1 ./... || fail 'tests failed'

output=dist/release-$tag
[ ! -e "$output" ] && [ ! -L "$output" ] || fail "$output already exists"
mkdir -p dist
stage=$(mktemp -d "dist/.ops-release.XXXXXXXX") || fail 'could not create release staging directory'
cleanup() {
    rm -rf -- "$stage"
}
trap cleanup EXIT HUP INT TERM

binary=$stage/ops-linux-x86_64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly -trimpath -buildvcs=true \
    -ldflags "-s -w -X github.com/luigiverona/ops/internal/version.Value=$version" \
    -o "$binary" ./cmd/ops || fail 'production build failed'
[ "$("$binary" --version)" = "ops $version" ] || fail 'production binary reports an unexpected version'

if [ -n "$candidate" ]; then
    candidate_copy=$stage/ci-candidate
    case "$candidate" in
        https://*)
            command -v curl >/dev/null 2>&1 || fail 'curl is required to download a CI candidate'
            curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 "$candidate" -o "$candidate_copy" || fail 'could not download CI candidate'
            ;;
        http://*) fail 'CI candidate URL must use HTTPS' ;;
        *)
            [ -f "$candidate" ] && [ ! -L "$candidate" ] || fail 'CI candidate is not a safe regular file'
            install -m 0600 -- "$candidate" "$candidate_copy" || fail 'could not stage CI candidate'
            ;;
    esac
    if ! cmp -s "$binary" "$candidate_copy"; then
        printf 'independent  %s\n' "$(sha256sum "$binary" | awk '{print $1}')" >&2
        printf 'CI candidate %s\n' "$(sha256sum "$candidate_copy" | awk '{print $1}')" >&2
        fail 'independent and CI candidate builds disagree; refusing to sign'
    fi
fi

(cd "$stage" && sha256sum ops-linux-x86_64 > checksums.txt) || fail 'could not create final checksum manifest'
[ -z "$(git status --porcelain=v1 --untracked-files=all)" ] || fail 'repository changed during release preparation'

signing_home=${OPS_SIGNING_GNUPGHOME:-}
[ -n "$signing_home" ] && [ -d "$signing_home" ] && [ ! -L "$signing_home" ] || fail 'OPS_SIGNING_GNUPGHOME must be a safe dedicated signing home'

secret=$(gpg --homedir "$signing_home" --no-options --batch --no-tty --with-colons --list-secret-keys "$fingerprint!" 2>/dev/null) || fail 'pinned release-signing subkey is unavailable'
printf '%s\n' "$secret" | awk -F: -v fingerprint="$fingerprint" '
    $1 == "ssb" { active=($2 != "r" && $2 != "e" && tolower($12) ~ /s/); next }
    active && $1 == "fpr" && toupper($10) == fingerprint { found=1 }
    $1 != "fpr" { active=0 }
    END { exit !found }
' || fail 'pinned release-signing subkey is expired, revoked, or not signing-capable'

gpg --homedir "$signing_home" --no-options --local-user "$fingerprint!" --detach-sign --output "$stage/checksums.txt.sig" "$stage/checksums.txt" || fail 'manifest signing failed'
status=''
if status=$(gpg --homedir "$signing_home" --no-options --batch --no-tty --status-fd 1 --trust-model always --no-auto-key-retrieve --verify "$stage/checksums.txt.sig" "$stage/checksums.txt" 2>/dev/null); then
    signature_exit=0
else
    signature_exit=$?
fi
[ "$signature_exit" -eq 0 ] || fail 'generated manifest signature did not verify'
printf '%s\n' "$status" | awk -v fingerprint="$fingerprint" '
    $1 != "[GNUPG:]" { next }
    $2 == "VALIDSIG" { total++; if (toupper($3) == fingerprint) valid++; else invalid=1 }
    $2 == "REVKEYSIG" || $2 == "EXPKEYSIG" || $2 == "EXPSIG" ||
    $2 == "BADSIG" || $2 == "ERRSIG" || $2 == "NO_PUBKEY" ||
    $2 == "NODATA" || $2 == "BADARMOR" || $2 == "KEYEXPIRED" ||
    $2 == "SIGEXPIRED" || $2 == "KEYREVOKED" || $2 == "FAILURE" ||
    $2 == "ERROR" || $2 == "UNEXPECTED" { invalid=1 }
    $2 != "VALIDSIG" && $2 != "NEWSIG" && $2 != "KEY_CONSIDERED" &&
    $2 != "SIG_ID" && $2 != "GOODSIG" && $2 !~ /^TRUST_/ { invalid=1 }
    END { exit !(valid == 1 && total == 1 && !invalid) }
' || fail 'generated manifest signature status is invalid'

rm -f -- "$stage/ci-candidate"
chmod 0755 "$binary"
chmod 0644 "$stage/checksums.txt" "$stage/checksums.txt.sig"
mv -- "$stage" "$output" || fail 'could not publish prepared release directory'
trap - EXIT HUP INT TERM
printf 'Prepared\n  tag             %s\n  commit          %s\n  compiler        %s\n  output          %s\n' "$tag" "$head" "$required_go" "$output"
