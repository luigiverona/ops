# Wave E production implementation validation

Baseline: `50adbe8518aee4108ee7bd7b6532e6a9ad9daf7f`.
Branch: `fix/process-tree-cancellation`.
Production implementation commit: `2299c07`.
Original evidence preservation commit: `03df753`.
Permanent integration tests and this report accompany the subsequent local test
commit. No branch was pushed and no PR or GitHub Actions run was opened.

The [architecture implementation section](../../wave-e-process-ownership-architecture.md#production-implementation--2026-09-19)
describes activation, capability verification, lifetime control, diagnostics,
terminal behavior, dependency choice, and the threat/crash boundaries.
The [security review](security-review.md) has zero unresolved Critical findings
and zero unresolved Important findings caused by this implementation.

## Exact environment and local checks

Every Go invocation used `~/.local/opt/go1.26.7/bin` ahead of PATH,
`GOENV=off`, and `GOTOOLCHAIN=local`; the guest used the identical transferred
Go 1.26.7 distribution. `go version` reported `go1.26.7 linux/amd64`.
Go reports an empty GOENV filename when GOENV=off, and `local` for GOTOOLCHAIN.
The go.mod language directive remains 1.26.0 and was not upgraded.

| Check | Result |
| --- | --- |
| `go test -count=1 ./internal/run` | PASS, final 35.799s |
| `go test -race -count=1 ./internal/run` | Initial default ten-minute timeout in existing `TestWave3ReviewStress`; rerun below passed |
| `go test -race -count=1 -timeout=30m ./internal/run` | PASS, 566.216s; later-added fault-test cases also passed targeted race checks |
| `go test -count=1 ./internal/app` | PASS, final 3.201s |
| `go test -race -count=1 ./internal/app` | PASS, final 6.626s |
| Affected sudo/SSH/release/PGP/archrepo ordinary/race suites | PASS |
| `go test -count=1 ./...` | PASS; all packages, no new ordinary-suite skip |
| `go vet ./...` | PASS |
| Tagged ownership-package vet | PASS |
| `go build ./...` | PASS |
| CGO-disabled ops binary build | PASS |
| `go mod verify` | PASS, all modules verified |
| Fish `set formatted (gofmt -l .); test (count $formatted) -eq 0` | PASS |
| `git diff --check`, including complete baseline diff | PASS |
| Modified Bash/sh scripts syntax | PASS |

Only the required focused race packages and affected/native tests were run
locally; the complete repository race suite was not run locally. I-11's existing
stress implementation is unchanged. An obsolete intermediate race run was
stopped after later code changes, then the final focused package was run afresh.
See [local logs](local-validation.log) for results, including all-package output.

## Native ownership and application coverage

Workstation-native ordinary and race invocations ran as UID 1000 without sudo:

```sh
go test -count=1 -tags ownership_integration ./internal/run ./internal/pgp ./internal/release \
  -run '^(TestNativeOwnership|TestNativeCrashScopeLifetime|TestNativeGPGHelpers|TestNativeUpdaterOwnership|TestOwnershipPTY)$'
```

The combined native coverage and targeted additions all passed. The same current
native set also passed together under the race detector in the disposable guest
after sudo revocation; see [native race log](native-race-vm.log).

| Requirement | Observed result / regression |
| --- | --- |
| Current-PID delegated user scope | Production D-Bus activation passed; no re-exec |
| Actual capabilities | Child mkdir, directory FD, kill write, events read, removal, inert atomic-placement/pidfd probe passed |
| Atomic CgroupFD placement | Frozen command is visible in its cgroup before any userspace marker; first marker after thaw has identical membership |
| Normal descendants | Repeated trees and concurrent commands contained/removed |
| setsid/direct daemonization | Detached descendant remains in command subtree; natural completion kills it and reports leakage |
| Natural exit with descendants | Failure reported; population cleared before Run returns |
| Nested cgroups | Populated nested subtree killed; empty nested directories removed |
| Stubborn descendants | Ignored TERM/INT descendants forced out by cgroup.kill |
| stdout/stderr holders | Independent retained-output cases terminate, Wait completes and command directory disappears |
| Cancellation/deadline identity | errors.Is remains true through structured run.Error and exit/cleanup composition |
| Exit/cancel arbitration | Synchronized channel tests cover already-published completion, cancellation winning, deadlines and direct exit with pending pipes |
| Grace/empty races | Already-empty group does not need force kill; population disappearing during grace avoids force kill; already-empty kill-error race is accepted only after observation |
| Cleanup failure | Injected errors for activation, allocation/open, Start, kill/events/timeout/removal/close; repeated removal/cleanup; permanent poison |
| Exit plus cleanup failure | Native exit 7 survives composition with injected cleanup poison |
| Goroutine/process cleanup | Bounded repeated/concurrent native tests reap every recorded helper PID, require command-directory removal, and return to a stable goroutine bound |
| Bubblewrap | Production read-only launcher stays owned; nested namespace descendants cancel; no fallback without bwrap; existing isolation tests pass |
| TTY | Foreground/session behavior and input preserved; failed interactive commands restore saved termios; subsequent input succeeds |
| Manager-created services | A user-manager service survives initiating Run outside its cgroup; fixture stops it explicitly |
| Crash limitation | SIGKILL of fixture owner leaves population alive; test parent clears it and observes systemd collect the scope |
| Public GPG helpers | Repeated classic/keyboxd public import/export works after owned helper cleanup |
| Update | Check/decline do not activate; approval activates before download and before simulated verification/sudo/replacement; unsupported approval refuses |
| Updater descendants | Real Replace staged-executable fixture cancels a setsid descendant, preserves context identity, and runs cleanup with independent contexts and the same owner; temporary unprivileged file operations only |
| Reconcile | No-op/decline bypass ownership; unsupported approval is Fatal before sudo; approval activates before sudo; later poison blocks persistent writes and stops pacman/AUR/Flatpak application loops |
| Sudo keeper | Production acquisition/refresh each owned; concurrent lifetime cleanup and Close verified; joined cancellation/ownership error retained |

No PID enumeration, process-group fallback, privileged ownership helper, or
terminal-output redesign was added to production. Native test-only PID enumeration,
subreaping, freeze/thaw and crash cleanup make the assertions observable.

## Real sudo / PTY and minimal runtime

A **new disposable** Arch KVM guest was created for this session. It used the same
pinned official cloud image as CI, verified SHA-256
`d7cc7c86a21b32d6678c001464714f71f4ef7e0d7bbbfca65e99123ac5afc25b`, uid 1000,
kernel 7.2.6-arch2-1, systemd 261.3-1-arch and sudo 1.9.17p2. The preserved VM
was neither opened nor modified. No host sudo prompt or grant was used.

Before removing the ordinary cloud-init bootstrap grant, the guest ran:

```sh
OPS_OWNERSHIP_PRIVILEGED=1 go test -tags ownership_integration -count=1 -v \
  -run '^(TestPrivilegedOwnership|TestPrivilegedOwnershipKeeper|TestOwnershipPTY)$' \
  ./internal/run ./internal/sudo
# Then the identical targeted invocation with -race.
```

Both passed. Normal root trees, root setsid, natural root descendant leakage,
retained stdout/stderr, three bounded root-fork trials, and the sudo keeper were
contained and cleaned by UID 1000. In each PTY invocation the test enumerated
**two sudo processes** plus the root helper and three root descendants, checked
membership, canceled the owner, required all PIDs gone and cgroup removal,
then verified another input line and exact terminal-mode restoration.
See [privileged log](privileged-vm.log).

The cloud-init sudo grant was removed; `sudo -n true` failed. The complete
ordinary suite subsequently passed without sudo, as did native ownership,
GPG/crash and updater tests and their combined targeted race invocation.
See [ordinary guest log](unprivileged-vm.log). The permanent CI script maintains
this order and retains the existing complete race suite and skip audit.
This local VM run is not represented as a new protected GitHub Actions run.

A standard disposable Docker container inside that fresh guest ran the pinned
minimal Arch image with a read-only candidate mount, without privileged flags
or host cgroup mounts. Doctor, plan display and decline passed. Affirmative
approval returned status 2 with the safe-ownership-unavailable diagnostic,
without invoking the disposable sudo sentinel. Package inventory, managed
configuration hash and absence of SSH state were unchanged. The container
exited successfully and was removed. See [minimal log](minimal-runtime.log).
Its baseline package upgrade emitted a pre-existing pacman-key post-install
warning; the runtime assertions and command exit status passed.

The guest initially lacked Git metadata for one installer-render test. A local
fixture snapshot fixed that test environment; the subsequent complete ordinary
suite passed. No source relaxation or sudo re-grant was used.
The fresh VM exited through QMP; its image/overlay/seed/private SSH credentials
were deleted. See [cleanup record](disposable-vm-cleanup.json).

## Evidence, dependency and scope audit

All 21 original evidence/report files were hashed before implementation and
preserved byte-for-byte in `03df753`. Only the architecture report later gained
an implementation append. [Preservation audit](preservation.json) records the
original and current hashes. Original evidence contains reproducible source,
structured results and reports (about 424 KiB); secret-marker review found no
credentials. VM disks, seed credentials, build caches and bulk bootstrap output
are excluded. Retained output here is small and sanitized for control characters
and trailing whitespace without changing its substantive results.

Added dependency: `github.com/godbus/dbus/v5 v5.2.2`; sole transitive module
`golang.org/x/sys v0.27.0`. No existing module was upgraded. See
[module graph](module-graph.txt) and the architecture decision. No CGO is needed.

The transient scope remains process-scoped, with no persistent unit or lingering
change. Normal cleanup requires every owned command group empty and removed.
Hard-crash/SIGKILL, uninterruptible kernel tasks and hostile root cgroup changes
remain the explicitly documented limitations. These do not enable a weak
mutation fallback. Manager-owned services have separate operation semantics.

No push, PR, merge, tag, release, publication, R2 operation, preserved-VM mutation,
release signing, Go upgrade, unrelated dependency upgrade, or Wave F/later work
occurred. No existing main history was rewritten.
