# Wave E / I-02 implementation corrections

Correction baseline: `bed02e0733962c865cc927b2140390207f10edc6`.
Branch: `fix/process-tree-cancellation`.
`origin/main`: `50adbe8518aee4108ee7bd7b6532e6a9ad9daf7f` before and after fetch.
Initial worktree/index clean; no stashes. The prior three Wave E commits remain
unchanged. This report supplements the earlier implementation reports; it does
not rewrite their historical validation claims.

The independent review supplied in the correction request reported **Critical 0,
Important 4**, with conclusion **CORRECTIONS REQUIRED**. No separate Wave E
independent-review report was present in the repository or earlier available
conversation. The architecture, implementation validation, security review, code,
fixtures and CI were reread before applying corrections.

## I1: makepkg temporary helpers

Confirmed: both production makepkg calls lacked the `EphemeralHelpers` policy
already used by public GnuPG commands. The build can start GPG helpers for source
verification or reviewed build code; `--packagelist` also sources PKGBUILD and can
start helpers there. Both specific call sites now declare the existing policy.
No other command family gained an exemption. AUR unit coverage requires the flag
on both calls actually emitted by `Manager.Build`.

`TestNativeMakepkgHelpers` uses real installed makepkg, a disposable HOME and
keyboxd-enabled GNUPGHOME, an inert locally authored PKGBUILD, public key listing,
and a disposable unsigned package. There is no download, package installation,
private-key import or release signing. It records the actual command-owned PIDs.

- **Before:** `before-build` deliberately retains the original false policy.
  makepkg exits successfully and creates the package; Run reports `command exited
  with live background descendants`. No independent exit or ownership error is
  accepted as this reproducer's result.
- **After:** build and packagelist return success with the policy enabled.
  Tests require a recorded keyboxd, a real `cgroup.kill`, a subsequent real
  `populated 0` observation, disappearance of every recorded PID, and removal of
  the command cgroup.
- **Unexpected helper:** an unrelated forked Python helper is synchronized into
  existence, recorded, and proven live immediately before `cgroup.kill`. The
  command succeeds and the helper is killed and reaped. This policy deliberately
  does not classify helper executables. It accepts command-family-owned temporary
  descendants only in the sense that it resolves them before success; it never
  permits them to outlive Run. PID/name allowlists would not strengthen this
  reviewed lifecycle boundary. Undeclared command families still reject natural
  descendant leakage, and cleanup errors still poison the shared owner.

## I2: direct completion versus owned-lifetime completion

Confirmed: `pending := !waitReceived` incorrectly made a successful published
Wait imply that cancellation no longer belonged to the command lifetime.
The cleanup body now has an explicit testable boundary. Its final context
arbitration occurs after population resolution, direct reaping/pipe drain,
removal and handle closure, including exceptional cleanup returns.

An independent nonnil Wait result published before cancellation retains its
precedence. A small atomic completion marker also covers Wait publishing while
cleanup is already in progress, before the final receive. Existing cancellation
arbitration remains intact. Synchronized tests cover both event orders. An earlier lifecycle failure is different: ownership is still
unresolved, so a later context error is joined with the lifecycle error, preserving
both poisoning and interruption identity.

Synchronized ordinary tests failed before the correction for cancel, deadline,
prior lifecycle failure and cancellation after kill; they now pass. The existing
already-published independent-failure test also passes. Native tests synchronously
reap the direct process before their first real population observation, with a
live descendant still present:

| Ordering | Result |
| --- | --- |
| Wait success, first real population read, explicit cancel | `errors.Is(context.Canceled)` true |
| Wait success, population read waits for a real context deadline | `errors.Is(context.DeadlineExceeded)` true |
| Wait publishes exit 7, later cancellation in cleanup | Exit 7 preserved; cancellation does not replace it |
| Wait success, lifecycle observation failure, later cancellation | Both ownership failure and cancellation preserved |

No arbitrary sleep establishes these orderings. The ordinary deadline context is
synchronously controlled; native coverage also uses a real deadline, waiting on
its Done channel inside the population observation.

## I3: SSH fingerprint discovery

Confirmed: discovery continued after every fingerprint error, reducing ownership
failure to missing/changed identity state. Revalidation could discard the same
causes. Both now propagate typed ownership failures and context cancellation or
deadline errors with `%w`. Ordinary malformed/unrecognized keys still follow the
existing discovery classification.

Before correction, `TestDiscoveryLifecycleFailures` returned nil and inspected
both candidates for injected ownership, cancel and deadline errors. Afterward it
returns the original cause after one candidate. Ordinary malformed candidates
still yield no recognized identities and no fatal discovery error. Revalidation
also preserves each interruption identity.

Application lifecycle regressions inject discovery ownership failure before any
mutation and after successful Git identity changes. Both are Fatal, preserve the
original OwnershipError, stop inspection and execute no later command or SSH file
mutation. The first reports no applied changes; the second retains the earlier
Git changes and reports that earlier changes may remain. Ownership failure alone
is not mislabeled as context interruption.

## I4: fixture deadlines and failure cleanup

Confirmed with a bounded standalone fork probe: an alarm armed before fork
reported **0 inherited seconds** in the child. Running the old indefinite
heartbeat fixtures without an independent cleanup mechanism was avoided.
`ownership.py` now registers an at-fork hook which resets SIGALRM to its default
hard termination action and arms ITIMER_REAL in every child and grandchild.
The default remains 20 seconds; a validated test-only environment override permits
shorter bounds. TERM/INT remain ignored where required by cgroup.kill tests.
Production timing constants are unchanged.

`TestFixtureIndependentExpiration` uses one-second fixture bounds, a surviving
subreaper and recorded PIDs. Tree and fork cases prove active independent child
and grandchild timers, kill the direct parent unexpectedly, then require the
heartbeat children to terminate specifically from SIGALRM without receiving a
cleanup signal. Every recorded PID is reaped/absent. Failure fallback targets
only this fixture's waitable recorded children.

The PTY supervisor is now a surviving subreaper. The Go helper writes its verified
unique scope path before launching workloads. In a bounded finally path, the
supervisor kills/reaps the owner, kills only that exact validated scope, observes
population zero (or concurrent systemd collection of the empty scope), removes
empty disposable child cgroups where possible, reaps recorded children and
requires scope and PID disappearance. Cleanup is attempted even if waiting for
the owner raises an error. No process-group or user-wide cleanup is used.

The injected PTY failure regression first proves **owner-only cleanup leaves
populated=1**, safely reproducing the prior failure, then requires independent
scope cleanup to succeed. Ordinary native failure cleanup now kills, population-
checks, removes and reaps only fixture groups below the exact activated scope.
Crash-test cleanup is registered immediately after Start, using the separately
recorded scope even if an assertion fails before normal cleanup. Owner reaping
and parent-death fallback reaping each have explicit independent time bounds. The inline
Bubblewrap fork fixture also rearms child alarms.

The full ordinary suite exposed a pre-existing PGP fixture omission: testGPG
started temporary agents/keyboxd without stopping them before deleting their
homes. Its cleanup now uses a five-second independent context and bounded pipe
wait to run `gpgconf --homedir <exact disposable home> --kill all` before TempDir
removes sockets. PGP ordinary/race tests pass with no new surviving helpers.
The two helpers from the initial full-suite reproduction were stopped using
pidfds after verifying their exact recorded temporary-home argv. Six helpers
already present before this session were recorded and left untouched.

## Optional cgroup.events hardening

Implemented and tested: incomplete final records and snapshots filling the fixed
4096-byte buffer fail closed. ReadAt already fills its buffer or returns an error;
a full buffer does not prove EOF. Rejecting it avoids parsing a potentially valid
prefix without introducing an unbounded read loop. Focused tests cover missing
newlines, truncated trailing records, exact-full and oversized snapshots, and a
complete valid snapshot. Existing duplicate/malformed population tests still pass.

## Validation

All Go commands use `~/.local/opt/go1.26.7/bin`, `GOENV=off`,
`GOTOOLCHAIN=local`. Version: `go version go1.26.7 linux/amd64`; Go reports empty
GOENV filename and `local`. No Go/module version changed.

| Check | Result |
| --- | --- |
| internal/run ordinary, count=1 | PASS (42.407s focused; also full suite) |
| internal/run race, count=1, timeout=30m | PASS (final arbitration: 582.159s) |
| internal/aur ordinary / race | PASS (0.024s / 1.024s) |
| internal/ssh ordinary / race | PASS (1.371s / 2.230s) |
| internal/app ordinary / race, timeout=10m | PASS (3.236s / 7.059s) |
| Additionally affected internal/pgp ordinary / race | PASS (0.494s / 10.427s) |
| Complete safe native integration | PASS (run 40.843s, pgp 0.634s, release 2.092s) |
| Complete safe native integration under race | PASS (run 43.611s, pgp 1.570s, release 3.091s) |
| Strengthened final makepkg/PTY assertions, ordinary / race | PASS (24.659s / 25.501s; PTY race 31.056s combined) |
| Full ordinary suite, count=1, after PGP fixture cleanup | PASS (final source: run 41.072s; every package passed) |
| go mod verify | PASS: all modules verified |
| go vet ./... and tagged ownership-package vet | PASS |
| go build ./... | PASS |
| Privileged tagged packages compile without execution | PASS |
| Fish gofmt check, shell syntax, git diff --check | PASS |

Complete safe native command (no sudo):

```sh
go test -count=1 -timeout=3m -tags ownership_integration \
  ./internal/run ./internal/pgp ./internal/release \
  -run '^(TestNativeOwnership|TestNativeCrashScopeLifetime|TestNativeGPGHelpers|TestNativeUpdaterOwnership|TestOwnershipPTY|TestOwnershipPTYFailureCleanup|TestNativeMakepkgHelpers|TestNativeCleanupCancellation)$'
```

The same selection passed with `-race`. Final bounded parent-death/crash/PTY
failure cleanup additions also passed targeted race checks (3.115s / 1.187s). It covers trees, setsid, retained output,
natural exit, cancellation/deadline, atomic/frozen placement, start failure, nested
cgroups, PTYs, manager-owned services, crash behavior, Bubblewrap, GPG and updater
ownership. Final targeted runs additionally validate strengthened makepkg kill/
zero-population and PTY owner-only-failure assertions. No privileged tagged test
was executed locally; its code was reviewed and compiled. The full repository
race suite was not run locally; I-11 remains unchanged.

## Correction-delta security and CI review

Review boundary is exclusively `bed02e0` to this correction, including all new
regressions and fixture code. **Critical: 0. Important: 0. Optional: 0 new or
unresolved in the correction delta.** The reviewed cgroup.events Optional is
addressed above; existing I-11 and the previously documented architecture limits
remain separate follow-ups.

- The makepkg declaration changes successful-result classification only; it does
  not bypass force cleanup, population proof, draining, reaping, directory removal
  or poisoning. No general/background escape allowance was introduced.
- Context arbitration preserves earlier published independent failure. Lifecycle
  errors retain their identity when joined with cancellation.
- SSH uses OwnershipFailed/errors.Is, not strings. Ordinary candidate rejection
  remains nonfatal and separately tested.
- Fixture cleanup uses exact scope paths/handles from activation, validates the
  PTY owner's PID and generated scope name, and targets only that scope's children
  or exact recorded waitable PIDs. Independent expiration exists solely in test
  data. No sudo, shared session cleanup or unrelated cgroup cleanup was added.
- Direct user-manager D-Bus bootstrap, transient Delegate=yes scope, CgroupFD
  placement, per-command cgroups, cgroup.kill/populated=0 authority, poisoning,
  shared Owner, read-only/no-op/decline behavior, fail-closed mutation, sudo order,
  manager boundary, malicious-root/same-UID threat limits, Bubblewrap and terminal
  presentation are unchanged.
- `.github/workflows/ci.yml` and `arch.yml` retain protected job names `ci`, `build`,
  `minimal-runtime`. Privileged production ordinary/race ownership and PTY failure
  cleanup run before sudo revocation. Revocation and its no-sudo assertion remain;
  ordinary and complete race suites remain after revocation. Native correction
  tests are added to the unprivileged selection. `fakeroot` is added only to the
  future disposable guest's fixture prerequisites for real makepkg builds; no
  local package installation or unrelated dependency upgrade occurred.

No push, PR, merge, tag/release, publication, protected CI invocation, R2 use,
preserved-VM access/mutation, release signing, Go upgrade, unrelated dependency
upgrade or Wave F/later work occurred. Final local commit and clean-state/leak
audit are reported with the correction delivery.

Final fixture audit: no new helper PID, heartbeat writer, command cgroup or
transient fixture scope remains. The six pre-existing GPG helpers (PIDs 85311,
85316, 87542, 87556, 87780, 87796) predate the session and were not modified.
The exact helper PIDs created by the regressions are asserted absent within each
native test; post-suite inspection confirms no new GPG helper or fixture scope.
