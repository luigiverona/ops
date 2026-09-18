#!/bin/bash
# Destructive only inside a fresh disposable CI container; never on a workstation.
# Host AppArmor preparation and Docker security options belong to ci.yml.
set -euo pipefail

test -f /.dockerenv
test "$(id -u)" = 0
. /etc/os-release
test "$ID" = arch
test -x /opt/go/bin/go

printf '::group::Bootstrap disposable Arch container\n'
# Full upgrade, never a partial Arch upgrade. Do not install rolling Arch Go.
pacman -Syu --needed --noconfirm \
    archlinux-keyring pacman gnupg libarchive bubblewrap flatpak \
    git gcc openssh python util-linux diffutils
useradd --create-home --uid 1000 ops-ci
install -d -m 0700 -o ops-ci -g ops-ci /run/user/1000
printf '::endgroup::\n'

runuser -u ops-ci -- env \
    HOME=/home/ops-ci PATH=/opt/go/bin:/usr/bin \
    GOENV=off GOTOOLCHAIN=local GOROOT=/opt/go \
    GOPATH=/home/ops-ci/go GOCACHE=/home/ops-ci/.cache/go-build \
    GOMODCACHE=/home/ops-ci/go/pkg/mod XDG_RUNTIME_DIR=/run/user/1000 \
    bash --noprofile --norc <<'TEST'
set -euo pipefail
cd /workspace
# Checkout belongs to the host runner and is deliberately mounted read-only.
git config --global --add safe.directory /workspace

# Keep verbose output and audit even if go test itself fails.
audit_test() {
    local log=$1 status=0
    shift
    "$@" 2>&1 | tee "$HOME/$log" || status=$?
    if grep -E '^[[:space:]]*--- SKIP:' "$HOME/$log" | grep -v -- '--- SKIP: TestOfficialArchIntegration '; then
        echo "Unexpected test skip in $log" >&2
        return 1
    fi
    return "$status"
}

printf '::group::Verify native Arch dependencies\n'
test "$(id -u)" = 1000
id
cat /etc/os-release
for tool in pacman pacman-conf vercmp gpg gpgconf bsdtar bwrap flatpak git gcc ssh ssh-keygen ssh-agent ssh-add python3 script unshare mount cmp; do
    command -v "$tool"
    pacman -Qo "$(command -v "$tool")"
done
for keyring in archlinux.gpg archlinux-trusted archlinux-revoked; do
    test -r "/usr/share/pacman/keyrings/$keyring"
    pacman -Qo "/usr/share/pacman/keyrings/$keyring"
done
pacman --version
bsdtar --version
bwrap --version
flatpak --version
gcc --version
printf '::endgroup::\n'

printf '::group::Prove non-root bwrap isolation and AppArmor attachment\n'
test "$(cat /proc/self/attr/current)" = unconfined
export CI_OUTER_USER_NS CI_OUTER_MOUNT_NS CI_OUTER_NET_NS
CI_OUTER_USER_NS=$(readlink /proc/self/ns/user)
CI_OUTER_MOUNT_NS=$(readlink /proc/self/ns/mnt)
CI_OUTER_NET_NS=$(readlink /proc/self/ns/net)
bwrap --unshare-all --die-with-parent --new-session --ro-bind / / --proc /proc -- python3 - <<'PY'
import fcntl
import os
from pathlib import Path
import socket
import struct

assert os.getuid() == 1000
# PID 1 is bwrap's namespace supervisor; the payload has the stacked child policy.
parent = Path('/proc/1/attr/current').read_text().strip()
child = Path('/proc/self/attr/current').read_text().strip()
print(f'bwrap supervisor AppArmor: {parent}', flush=True)
print(f'bwrap payload AppArmor: {child}', flush=True)
assert parent == 'bwrap (enforce)', parent
assert child == 'bwrap//&unpriv_bwrap (enforce)', child
for kind, outer in [('user', 'CI_OUTER_USER_NS'), ('mnt', 'CI_OUTER_MOUNT_NS'), ('net', 'CI_OUTER_NET_NS')]:
    inner = os.readlink(f'/proc/self/ns/{kind}')
    print(f'{kind}: outside={os.environ[outer]} inside={inner}')
    assert inner != os.environ[outer]
print('uid_map:', Path('/proc/self/uid_map').read_text().strip())
assert [name for _, name in socket.if_nameindex()] == ['lo']
with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
    request = struct.pack('256s', b'lo')
    flags = struct.unpack_from('H', fcntl.ioctl(sock, 0x8913, request), 16)[0]
    address = socket.inet_ntoa(fcntl.ioctl(sock, 0x8915, request)[20:24])
    assert flags & 1, flags  # IFF_UP
    assert address == '127.0.0.1', address
    print(f'private loopback: UP {address}')
print('Non-root bwrap probe: PASS')
PY
printf '::endgroup::\n'

printf '::group::Verify exact Go toolchain\n'
test "$(go env GOVERSION)" = go1.26.7
test "$(go version)" = 'go version go1.26.7 linux/amd64'
test "$GOENV" = off
test "$(go env GOTOOLCHAIN)" = local
go version
printf 'GOENV=%s GOTOOLCHAIN=%s\n' "$GOENV" "$GOTOOLCHAIN"
go env GOENV GOTOOLCHAIN CGO_ENABLED
go mod verify
printf '::endgroup::\n'

printf '::group::Native Arch regressions\n'
audit_test native-arch.log go test -v -count=1 ./internal/resolve -run '^TestReal(PacmanProviderPrintFormatInIsolatedDatabase|VerCmpArchVersionSemantics)$'
grep -E '^--- PASS: TestRealPacmanProviderPrintFormatInIsolatedDatabase ' "$HOME/native-arch.log"
grep -E '^--- PASS: TestRealVerCmpArchVersionSemantics ' "$HOME/native-arch.log"
printf '::endgroup::\n'

printf '::group::Native Flatpak tests\n'
audit_test native-flatpak.log go test -v -count=1 ./internal/flatpak
printf '::endgroup::\n'

printf '::group::Formatting and vet\n'
test -z "$(gofmt -l .)"
go vet ./...
printf '::endgroup::\n'

export GOFLAGS=-v
printf '::group::Full ordinary suite\n'
audit_test test.log go test -count=1 ./...
printf '::endgroup::\n'
printf '::group::Full race suite\n'
audit_test race.log go test -race -count=1 -timeout=30m ./...
printf '::endgroup::\n'
unset GOFLAGS

printf '::group::Build and shell validation\n'
go build ./...
# Preserve the existing Wave D shell-validation behavior (O-01 belongs to Wave F).
sh -n script/install.sh script/prepare-release.sh script/render-install.sh script/publish-release.sh script/test-minimal-arch.sh
bash -n script/test-ci-arch.sh
printf '::endgroup::\n'
TEST
