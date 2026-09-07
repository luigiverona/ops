#!/bin/sh
# Destructive only inside a fresh disposable CI container; never run on a workstation.
set -eu

test -f /.dockerenv
test "$(id -u)" = 0
test -f /candidate/ops

assert_pristine() {
    for command in git paru gh ssh flatpak; do
        if command -v "$command" >/dev/null 2>&1; then
            echo "runtime unexpectedly contains $command" >&2
            exit 1
        fi
    done
    for package in git openssh github-cli paru base-devel flatpak; do
        if pacman -Q "$package" >/dev/null 2>&1; then
            echo "runtime unexpectedly contains $package" >&2
            exit 1
        fi
    done
}

assert_pristine
# sudo is a supported baseline prerequisite, not an ops-managed package.
# This is container preparation, never hidden work inside ops.
pacman -Syu --noconfirm sudo
assert_pristine
useradd --create-home ops-test
install -m 0755 /candidate/ops /usr/local/bin/ops
install -m 0755 /candidate/resolve.test /usr/local/bin/ops-resolve-test

runuser -u ops-test -- sh <<'TEST'
set -eu
cd /home/ops-test
test ! -e .config/ops/apps.toml
test ! -e .ssh
before=$(pacman -Qq)
ops-resolve-test -test.v -test.run '^TestReal(PacmanProviderPrintFormatInIsolatedDatabase|VerCmpArchVersionSemantics)$'

check_doctor() {
    status=0
    ops doctor >doctor.out 2>&1 || status=$?
    cat doctor.out
    test "$status" = 1
    grep -Eq '^  git +missing$' doctor.out
    grep -Eq '^  ssh +missing$' doctor.out
    grep -Eq '^  github +missing$' doctor.out
    grep -Eq '^  flatpak +not required$' doctor.out
    grep -Eq '^  flathub +not required$' doctor.out
}

check_doctor
test ! -e .config/ops/apps.toml
test ! -e .ssh
umask 077
mkdir -p .config/ops
printf 'version = 2\n' >.config/ops/apps.toml
config_before=$(sha256sum .config/ops/apps.toml)
check_doctor

# A real controlling TTY exercises the public first-run path. Decline before sudo.
printf 'n\n' | script -q -e -c /usr/local/bin/ops /dev/null >plan.out 2>&1
tr -d '\r' <plan.out >plan.clean
cat plan.clean
for package in git github-cli openssh; do
    grep -Eq "^  $package +install +pacman$" plan.clean
done
grep -q 'Prepare this workstation?' plan.clean
grep -q 'Workstation preparation skipped.' plan.clean
if grep -Eq 'paru|base-devel|flatpak|flathub|executable file not found' plan.clean; then
    echo 'unexpected hidden prerequisite in empty-app plan' >&2
    exit 1
fi
test "$(pacman -Qq)" = "$before"
test "$(sha256sum .config/ops/apps.toml)" = "$config_before"
test ! -e .ssh
TEST

assert_pristine
