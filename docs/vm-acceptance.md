# Candidate acceptance from pre-ops

This is a manual gate after source validation, not a claim that VM acceptance
has run. Do not publish or sign a release based only on unit/container tests.
The public installer and production latest pointer remain unchanged by this
refactor; these commands deliberately install a locally built unsigned candidate
only on a disposable test clone.

The reusable domain is `opsinitialstate`; its internal `pre-ops` snapshot is
shut off. Do not delete/redefine that snapshot, flatten its disk, run sysprep on
it, or install prerequisites into the baseline. The procedure restores it and
clones the restored disk, leaving the original shut off. Use an unused clone
name; an existing candidate VM may contain work and must not be overwritten.

## Build, revert, clone, boot

Run from the repository root. Put the reviewed Go 1.26.7 compiler on PATH first.
These commands require your normal libvirt system-connection authorization.

```sh
set -eu
export GOENV=off GOTOOLCHAIN=local
test "$(go env GOVERSION)" = go1.26.7
test -z "$(git status --porcelain)"
OPS_ACCEPTANCE_DIR=$(mktemp -d)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly -trimpath \
  -o "$OPS_ACCEPTANCE_DIR/ops" ./cmd/ops
sha256sum "$OPS_ACCEPTANCE_DIR/ops"

virsh -c qemu:///system snapshot-info opsinitialstate pre-ops
virsh -c qemu:///system domstate opsinitialstate
# If running, request a clean shutdown. Do not force-stop the baseline.
if test "$(virsh -c qemu:///system domstate opsinitialstate)" != 'shut off'; then
  virsh -c qemu:///system shutdown opsinitialstate
fi
```

Wait for shutdown and confirm it before continuing. If shutdown fails, resolve
that safely; do not substitute `destroy` or snapshot `--force`.

```sh
test "$(virsh -c qemu:///system domstate opsinitialstate)" = 'shut off'
virsh -c qemu:///system snapshot-revert opsinitialstate pre-ops
test "$(virsh -c qemu:///system domstate opsinitialstate)" = 'shut off'
if virsh -c qemu:///system dominfo ops-candidate >/dev/null 2>&1; then
  echo 'ops-candidate already exists; stop and preserve it' >&2
  exit 1
fi
virt-clone --connect qemu:///system --original opsinitialstate \
  --name ops-candidate --auto-clone
virsh -c qemu:///system start ops-candidate
virsh -c qemu:///system domifaddr ops-candidate --source lease
```

Wait for DHCP/SSH. Keep only the clone running to avoid confusing the two
guests. Discover its address and independently verify its SSH host fingerprint
against the clone console before accepting a new SSH host key. Use the existing
`ops01` SSH key/agent; add `-i /path/to/key` to SSH/SCP if necessary. Do not disable
host-key checking or delete unrelated known_hosts entries.

```sh
OPS_VM_IP=$(virsh -c qemu:///system domifaddr ops-candidate --source lease |
  awk '/ipv4/ {sub(/\/.*/, "", $4); print $4; exit}')
test -n "$OPS_VM_IP"
# Optional console for independent public host fingerprint verification:
# virsh -c qemu:///system console ops-candidate
# Inside guest: ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
ssh -o StrictHostKeyChecking=ask "ops01@$OPS_VM_IP" true
```

## Install candidate, doctor, converge, doctor, no-op

No manual Git, AUR helper, compiler, or Flatpak installation is permitted. The
SSH server and sudo are already part of the supported VM test baseline.

```sh
ssh "ops01@$OPS_VM_IP" '
  test ! -e /usr/local/bin/ops &&
  test ! -e .config/ops/apps.toml &&
  ! command -v git && ! command -v paru &&
  ! pacman -Q base-devel && ! pacman -Q flatpak
'
scp "$OPS_ACCEPTANCE_DIR/ops" "ops01@$OPS_VM_IP:ops-candidate"
ssh "ops01@$OPS_VM_IP" 'sha256sum ops-candidate'
test "$(ssh "ops01@$OPS_VM_IP" 'sha256sum ops-candidate' | cut -d ' ' -f 1)" = \
  "$(sha256sum "$OPS_ACCEPTANCE_DIR/ops" | cut -d ' ' -f 1)"
ssh -t "ops01@$OPS_VM_IP" '
  set -eu
  sudo install -m 0755 ops-candidate /usr/local/bin/.ops-candidate
  sudo mv -T /usr/local/bin/.ops-candidate /usr/local/bin/ops
  umask 077
  mkdir -p .config/ops
  test ! -e .config/ops/apps.toml
  printf "version = 2\n" >.config/ops/apps.toml
  ops --version
'

# Expected status 1: missing capabilities, not fatal status 2.
ssh "ops01@$OPS_VM_IP" 'status=0; ops doctor || status=$?; test "$status" = 1'

# Review the plan and complete sudo, Git, SSH, and GitHub interaction normally.
ssh -t "ops01@$OPS_VM_IP" ops

# Both must exit 0. The second run intentionally has no TTY.
ssh "ops01@$OPS_VM_IP" 'ops doctor'
OPS_SECOND_OUTPUT=$(ssh "ops01@$OPS_VM_IP" ops)
printf '%s\n' "$OPS_SECOND_OUTPUT"
printf '%s\n' "$OPS_SECOND_OUTPUT" | grep -q '^No changes$'

# Empty apps must not have pulled in optional capabilities.
ssh "ops01@$OPS_VM_IP" '! pacman -Q paru && ! pacman -Q base-devel && ! pacman -Q flatpak'
virsh -c qemu:///system snapshot-info opsinitialstate pre-ops
```

Record the source commit, compiler, candidate SHA-256, guest package state,
command exit codes, and plan/final output. Do not put passwords, tokens, or
private keys in logs. Keep unrelated SSH/GitHub keys when prompted. A disposable
GitHub test account avoids touching a production account; authentication and
key registration are real external changes authorized by your interactive plan.

Repeat on fresh clones with pacman-only, AUR-only, Flatpak-only, and mixed app
lists to exercise real package sources, source review, services, and session
integration. Each successful run must be followed by doctor and a no-op run.
Source/provider drift must stop the affected build for renewed review, not be
worked around by installing a helper. Retain the clean baseline and its snapshot
for future acceptance; cleanup of test clones/account keys is a separate,
explicitly reviewed operation.

Reference: [libvirt virsh snapshot commands](https://www.libvirt.org/manpages/virsh.html).
