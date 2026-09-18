#!/bin/bash
# Bootstrap and validate ONLY the disposable Arch QEMU guest created by ci.yml.
# Never run on a workstation. All tests remain the ordinary UID 1000 user.
set -euo pipefail
trap 'echo "Guest command failed at line $LINENO: $BASH_COMMAND" >&2' ERR

test "$(systemd-detect-virt --vm)" = kvm
test "$(cat /proc/sys/kernel/hostname)" = ops-ci
test "$(id -un)" = ops-ci
test "$(id -u)" = 1000
. /etc/os-release
test "$ID" = arch
test -x /opt/go1.26.7/bin/go
cd /home/ops-ci/source
mkdir -p "$HOME/ci-logs"

printf '::group::Bootstrap disposable Arch VM\n'
# Full upgrade before dependencies; never install Arch's rolling Go package.
sudo pacman -Syu --noconfirm
sudo pacman -S --needed --noconfirm \
    archlinux-keyring pacman gnupg libarchive bubblewrap flatpak \
    git gcc openssh python util-linux diffutils
sudo install -d -m 0700 -o ops-ci -g ops-ci /run/user/1000
# Bootstrap is over: remove the cloud-init sudo grant before running tests.
sudo rm /etc/sudoers.d/90-cloud-init-users
if sudo -n true 2>/dev/null; then
    echo 'Unexpected sudo access after bootstrap' >&2
    exit 1
fi
export PATH=/opt/go1.26.7/bin:/usr/bin GOENV=off GOTOOLCHAIN=local
export GOROOT=/opt/go1.26.7 GOPATH=/home/ops-ci/go
export GOCACHE=/home/ops-ci/.cache/go-build GOMODCACHE=/home/ops-ci/go/pkg/mod
export XDG_RUNTIME_DIR=/run/user/1000
printf '::endgroup::\n'

# Keep verbose output and audit even if go test itself fails.
audit_test() {
    local log=$1 status=0
    shift
    # Cap each log at 16 MiB while continuing to drain the command's output.
    # Exceeding the cap fails CI, rather than silently losing skip evidence.
    "$@" 2>&1 | python3 -c '
import sys
limit = 16 * 1024 * 1024
size = 0
with open(sys.argv[1], "wb") as log:
    for chunk in iter(lambda: sys.stdin.buffer.read1(65536), b""):
        size += len(chunk)
        if size <= limit:
            log.write(chunk)
            sys.stdout.buffer.write(chunk)
            sys.stdout.buffer.flush()
if size > limit:
    sys.exit("CI test output exceeded 16 MiB limit")
' "$HOME/ci-logs/$log" || status=$?
    if grep -E '^[[:space:]]*--- SKIP:' "$HOME/ci-logs/$log" | grep -v -- '--- SKIP: TestOfficialArchIntegration '; then
        echo "Unexpected test skip in $log" >&2
        return 1
    fi
    test "$status" = 0 || return "$status"
    echo "Unexpected-skip audit ($log): PASS"
}

printf '::group::Verify exact source and native Arch dependencies\n'
test "$(git rev-parse HEAD)" = "$(cat "$HOME/checkout-sha")"
git diff --exit-code HEAD
test -z "$(git status --porcelain)"
printf 'Exact source checkout: %s\n' "$(git rev-parse HEAD)"
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

printf '::group::Prove non-root namespaces and bwrap isolation\n'
uname -a
sysctl user.max_user_namespaces user.max_mnt_namespaces user.max_net_namespaces
if test -e /proc/sys/kernel/unprivileged_userns_clone; then
    sysctl kernel.unprivileged_userns_clone
fi
unshare --user --map-root-user -- /bin/true
echo 'Non-root unshare: PASS'
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
assert Path('/proc/self/mountinfo').is_file()
assert any(line.split()[4] == '/proc' and ' - proc ' in line for line in Path('/proc/self/mountinfo').read_text().splitlines())
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
# Go reports an empty GOENV filename when environment-file loading is disabled.
test -z "$(go env GOENV)"
test "$(go env GOTOOLCHAIN)" = local
go version
printf 'GOENV=%s GOTOOLCHAIN=%s\n' "$GOENV" "$GOTOOLCHAIN"
go env GOVERSION GOENV GOTOOLCHAIN CGO_ENABLED
go mod verify
printf '::endgroup::\n'

printf '::group::Native Arch regressions\n'
audit_test native-arch.log go test -v -count=1 ./internal/resolve -run '^TestReal(PacmanProviderPrintFormatInIsolatedDatabase|VerCmpArchVersionSemantics)$'
grep -E '^--- PASS: TestRealPacmanProviderPrintFormatInIsolatedDatabase ' "$HOME/ci-logs/native-arch.log"
grep -E '^--- PASS: TestRealVerCmpArchVersionSemantics ' "$HOME/ci-logs/native-arch.log"
printf '::endgroup::\n'

printf '::group::Native Flatpak tests\n'
audit_test native-flatpak.log go test -v -count=1 ./internal/flatpak
printf '::endgroup::\n'

printf '::group::Formatting and vet\n'
test -z "$(gofmt -l .)"
go vet ./...
echo 'Formatting and vet: PASS'
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
echo 'Build and shell validation: PASS'
