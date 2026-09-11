# VM acceptance from pre-ops

This is a manual gate after source validation, not a claim that VM acceptance
has run. Do not publish or sign a release based only on unit/container tests.
These commands install a locally built unsigned candidate only on a disposable
test clone. They do not exercise or change the public installer, release hosting,
or production latest pointer.

The reusable domain is `opsinitialstate`; its internal `pre-ops` snapshot is
shut off. Do not delete/redefine that snapshot, flatten its disk, run sysprep on
it, or install prerequisites into the baseline. The procedure restores it and
clones the restored disk, leaving the original shut off. Use an unused clone
name; an existing candidate VM may contain work and must not be overwritten.

## Build, revert, clone, boot

Run from the repository root in Bash or a POSIX `sh` session; the blocks below
use that shell syntax, not interactive Fish syntax. Put the existing Go 1.26.7
compiler on PATH first. These commands require your normal libvirt
system-connection authorization.

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

# Review the setup summary and complete sudo, Git, SSH, and GitHub interaction.
# Expect exit 0 and the ending: Workstation ready.
ssh -t "ops01@$OPS_VM_IP" ops

# Both must exit 0. The second run intentionally has no TTY.
OPS_DOCTOR_OUTPUT=$(ssh "ops01@$OPS_VM_IP" 'ops doctor')
printf '%s\n' "$OPS_DOCTOR_OUTPUT"
test "$OPS_DOCTOR_OUTPUT" = 'Workstation healthy.'
OPS_SECOND_OUTPUT=$(ssh "ops01@$OPS_VM_IP" ops)
printf '%s\n' "$OPS_SECOND_OUTPUT"
test "$OPS_SECOND_OUTPUT" = 'Workstation already ready.'

# Empty apps must not have pulled in optional capabilities.
ssh "ops01@$OPS_VM_IP" '! pacman -Q paru && ! pacman -Q base-devel && ! pacman -Q flatpak'
virsh -c qemu:///system snapshot-info opsinitialstate pre-ops
```

The setup no-op check requires the remote checks used by setup to be available
and any existing agent/session state to be suitable. A new SSH session may
change those conditions. Investigate a reported session issue separately from
persistent configuration; do not weaken the exact no-op expectation or interpret
Doctor's healthy result as proof of current authentication.

Record the source commit, compiler, candidate SHA-256, guest package state,
command exit codes, and setup/final output. Do not put passwords, tokens, or
private keys in logs. Unrelated SSH/GitHub keys must be preserved automatically,
without per-key prompts. Use a disposable GitHub test account: authentication
and managed-key registration are real external changes covered by setup approval.

Repeat on fresh clones with pacman-only, AUR-only, Flatpak-only, and mixed app
lists to exercise real package sources, source review, services, and session
integration. Each successful run must be followed by doctor and a no-op run.
Source/provider drift must stop the affected build for renewed review, not be
worked around by installing a helper. Retain the clean baseline and its snapshot
for future acceptance; cleanup of test clones/account keys is a separate,
explicitly reviewed operation.

## Scenario checklist

Use fresh disposable clones for independent first-run cases. Choose currently
available exact identifiers with the tools/sites in [configuration](configuration.md#finding-identifiers)
and record the source declarations and revisions used. The following are
acceptance procedures, not recorded test results.

| Scenario | Procedure and required observations |
| --- | --- |
| Pristine baseline, missing config | Before creating apps.toml, run Doctor without a TTY. Expect exit 1, missing-config guidance and missing managed capabilities; no file creation, package changes, sudo, or login |
| Empty config | Use `version = 2` alone, then repeat with all three arrays empty. Git, SSH, and GitHub setup still occurs; no undeclared paru, base-devel, or Flatpak capability is installed |
| pacman-only | Declare an official package. Check one interactive full system upgrade precedes official installs, the exact package is installed, and no unnecessary AUR/Flatpak capability is added |
| AUR-only | Declare an AUR package without manually installing Git, a helper, or compilers. Check paginated source review, package/base/revision context, default-no approval, and successful source-specific installation |
| Flatpak-only | Verify user-scoped installation from Flathub. On a prepared workstation with Flatpak and the remote already ready, adding only a Flatpak application must not announce or perform a system upgrade |
| Mixed sources | Declare pacman, AUR, and Flatpak applications, including one with a required service where applicable. Verify every declaration, service, and source; there is no automatic source fallback |
| Top-level decline | Record package inventory and managed-file contents before running ops; answer `n` at `Continue? [Y/n]`. Expect exit 0 and `No changes made.`; no sudo, login, installs, or file changes |
| AUR local skip | Use `q` in the source view; separately complete review and answer `n` or press Enter at build/install approval. No build, signing-key import, or build-dependency installation for that skipped application follows; other approved work continues. If its declaration remains unmet, expect exit 1 and incomplete setup, not global interruption or `No changes made.` |
| Doctor healthy | After convergence, expect exactly `Workstation healthy.` and exit 0 without a TTY. Repeat without a reachable agent and with network unavailable when no missing applications need source queries; persistent health must not require an unlocked key or live GitHub SSH authentication |
| Doctor unhealthy | On a separate clone, leave a managed capability, identity setting, application, or required service unmet. Expect exit 1 with specific issues and no repair. Malformed config or inability to complete local inspection exits 2, with inspection failure distinguished from confirmed missing state |
| Idempotent second run | With setup's remote/session checks available, expect exactly `Workstation already ready.` and exit 0 without a TTY; no approval, upgrade, login, key registration, or other mutation |
| Package reason integrity | Compare `pacman -Qqe` (explicit), `pacman -Qqd` (dependency), and `pacman -Qqm` (foreign) before/after. Declared packages and pre-existing explicit packages stay explicit; automatically required build packages retain dependency reasons. Include a declared official build dependency and an AUR package with required sibling outputs |
| Git/SSH/GitHub configuration | Verify Git name/email, the managed `~/.ssh/ops` key and GitHub SSH boundary, strict GitHub host trust, and managed-key registration when needed. Successful setup checks SSH access. A second run must not duplicate registration |
| Unrelated identity preservation | Seed an unrelated SSH host configuration, local key, loaded agent identity, and test-account GitHub key. Compare file contents/effective non-GitHub configuration and key fingerprints before/after. All remain present, with no preservation prompts or implicit key deletion/unloading |
| Partial operation failure | On a clone, use a controlled failing application operation. Check useful cause/recovery text, continued unrelated work where safe, and final observed state. A successful child exit with missing final state must still report incomplete setup; failure plus observed readiness must retain the operation issue and a nonzero exit |
| Final inspection unavailable | If safely reproducible on a clone, make an inspection dependency unavailable after an operation. Expect inability to verify final state and exit 2, not guessed absence or a ready ending. Restore access and inspect with Doctor |
| Source outage vs invalid declaration | Compare malformed TOML (exit 2 before workstation work), a syntactically valid identifier confirmed absent by pacman/AUR, and an unavailable source query (Doctor exits 1). Only confirmed absence warrants declaration-correction guidance. An inconclusive Flatpak lookup, including an HTTP 404, is unavailable rather than confirmed absence |
| Failure evidence | For eligible captured command failures, check short sanitized evidence and omission/withholding notices when applicable. Do not expect exact byte/line limits, duplicated streamed output, or replay of untrusted AUR build logs. Never put real secrets into a diagnostic fixture |

The default setup summary begins with `Workstation setup` and uses applicable
sections such as `Install`, `Configure`, and `Enable and start`. Configuration
items name Git identity, SSH for GitHub, and GitHub authentication/registration
separately. Internal lifecycle headings must not appear. Check one top-level
ops approval; native pacman, sudo, passphrase, and GitHub device interactions
still belong to their respective tools.

For AUR review, visit all pages, go back with `b`, and verify PKGBUILD and
auxiliary tracked files are reviewable. `.SRCINFO` remains authoritative
validated metadata and is not displayed as source content. Before approval,
verify disclosure of normal-user execution and access to user files, required
public signing-key imports where applicable, and selected additional sibling
outputs. Review approval is a trust decision, not a sandbox.

## Cancellation and terminal behavior

Use a real controlling terminal on a clone. Test Ctrl-C at top-level approval,
Git identity input, AUR source review, and AUR build/install approval. Also test
SIGTERM and EOF at an ops-owned prompt. Record exit 2, prompt responsiveness,
restoration of terminal settings/source-review screen, and absence of later work.
Native child interactions should terminate without leaving subsequent ops work
running. Do not force-stop a real package transaction merely to create a test;
use the existing controlled PTY tests for that boundary.

Cancellation before setup mutation ends with
`Interrupted. No workstation changes made.` After mutation has begun, expect
`Interrupted. Earlier changes may remain. Run ops doctor before retrying.`
There must be one interruption conclusion, no ready/incomplete success footer,
and no final reinspection or replay of previously captured failure evidence.
EOF is an input failure and must not grant approval or masquerade as Ctrl-C.
The source viewer's `q` is a local skip, not process cancellation.

## Recording validation

For each scenario record **passed**, **failed**, or **not run**, with the candidate
commit/hash, environment, exit status, observed output/state, and any limitation.
Distinguish native VM observations from simulated unit/PTY tests. A documentation
update or passing `script/test-minimal-arch.sh` does not establish VM acceptance:
that container script covers pristine inspection and a real-terminal decline,
not systemd, device authentication, real AUR builds, or full convergence.

Reference: [libvirt virsh snapshot commands](https://www.libvirt.org/manpages/virsh.html).
