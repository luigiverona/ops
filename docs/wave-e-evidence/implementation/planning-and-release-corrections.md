# Wave E planning and release cause corrections

Date: 2026-09-25. Branch: `fix/process-tree-cancellation`.
Starting HEAD: `d1e55992eb530dd81310d7dc686c4f9cc9d58928`.
`origin/main`: `50adbe8518aee4108ee7bd7b6532e6a9ad9daf7f`, unchanged after fetch.
Preflight matched all requested values: clean worktree/index, no stashes.
The supplied independent review reported Critical 0, Important 1, Optional 1.
This is the implementation correction report, not an independent re-review.

## R1: application resolution must stop at the original fatal cause

Previously, `resolve.applications` handled a metadata query error around line 88
by assigning `plan.Unavailable`, `app.Err`, and string `app.Cause`, storing the
application in `facts`, and continuing the declaration loop. AUR source, build
resolution, and signing-key inspection had equivalent problem-storage branches.
The facts-only API could not return a fatal failure. Prepare and Doctor checked
only their parent context afterward, so a returned ownership or independent
context error could become a normal planning problem.

A controlled regression used the starting implementation's application resolver
(with only its return signature adapted for compilation) and a poisoned-runner
model. The first ownership failure occurred on Doctor call **24**. Doctor made
**26** calls total: the second application's query and the later local GitHub
query both ran after failure. Its final diagnostic named the later poisoned
admission, losing the original cause as the orchestration stop reason. No real
mutation occurred. The temporary reproduction source was restored immediately.

Both public resolution functions now return `(plan.Facts, error)` and return no
partial facts on fatal execution failure. `resolve.StopsPlanning` recognizes
`run.OwnershipFailed` and `errors.Is` cancellation/deadline identities through
wrappers and compound errors. It runs before each problem-storage boundary;
ordinary missing, invalid, unavailable, dependency and signing-key problems keep
their existing states. Declaration iteration also checks the active context.
Doctor/Prepare wrap the original error with `%w` and use their existing Fatal
reporting boundary before plan construction or further inspection.

The application reinspection audit also found that configure/install error
branches checked ownership or only the parent context. They now use the same
classifier, including when a returned interruption has a still-live parent
context. The existing execution stop result preserves the original error,
sets `stopInspection`, and retains the applied-change bookkeeping/reporting.
This also avoids an early fatal report hiding earlier application changes.

Deterministic results:

| Boundary | Result |
| --- | --- |
| Doctor ownership failure | Fatal, original cause; **24 calls, failure at 24, zero later calls** (previously 26/24/2) |
| Prepare planning ownership failure | Fatal, original cause; **27 calls, failure at 27, zero later calls**; no ownership activation, approval, sudo or mutation |
| Direct pacman/AUR/Flatpak resolution | Original cause, nil facts, **one call only**; Doctor availability mode also covered |
| AUR pinned source, dependency, comparator, signing-key loops | Stop at failing call; no later resolver admission; original cause preserved |
| Cancellation and deadline | `errors.Is` preserved in all seven resolver injection points and both public lifecycles; zero later calls |
| Configure-time source reinspection | Fatal, `stopInspection=true`, original ownership/cancel/deadline cause; no later application command or mutation |
| Final AUR dependency reinspection | Earlier Git name/email changes retained; original OwnershipError remains Fatal; zero later calls; “Earlier changes may remain” reported |
| Ordinary resolution problems | Two declarations still receive normal problems; existing source absence/outage, malformed identifier, AUR/dependency and planning tests pass |

Audited all production files in `internal/resolve`, `internal/inspect`, and the
application planning/reinspection callers. OfficialDependency's queue, installed
provider inspection, resolveAURBuild's requirement traversal, and aurmeta's
comparator traversals already return wrapped errors immediately. Workstation
Local/External return Git, SSH, GitHub, Flatpak and service inspection failures;
GitHub insufficient-scope recovery requires the existing compound-safe OnlyExit
classifier. Final AUR binding and core-repair loops already stop on errors.
Pure parsing errors are ordinary source/configuration problems. No expected-exit
acceptance or ownership architecture was redesigned.

## O1: release execution cause retention

At this HEAD, downloaded/staged/installed `--version` failures already used
`errors.Join` and retained their execution cause. The actual identity loss was
`validateSignatureStatus`: a GnuPG invalid-status diagnosis returned a new error
before considering `processErr`. A pre-correction regression reproduced loss of
ordinary exit, OwnershipError, cancellation, deadline, and structured run.Error
identities with BADSIG, while verification still failed safely.

A small release-local `verificationError` keeps the release diagnosis in Error
and the original execution cause in Unwrap. Invalid signature status now retains
the cause. Downloaded/staged/installed executable verification uses the same
wrapper so retaining causes does not append private raw command text. Existing
replacement recovery/cleanup and verification success semantics remain intact.

The matrix covers all five cause kinds at signature, downloaded executable,
staged executable, and installed executable verification. All fail; errors.Is,
errors.As, OwnershipFailed and ordinary exit discovery remain available. Exact
bounded error messages exclude private paths, arguments, stderr and output.
Application update tests independently prove signature/version failures return
Fatal with zero later commands, sudo or replacement, and private text absent.
Only disposable test signing keys were generated; no production signing occurred.

## Validation and preservation

Every Go invocation used Go **1.26.7**, `GOENV=off`, `GOTOOLCHAIN=local` with
`~/.local/opt/go1.26.7/bin` first in PATH. `go env` reports an empty GOENV filename
and `local`. The final code passes:

| Check | Ordinary | Race |
| --- | --- | --- |
| internal/resolve | 4.265s | 5.026s |
| internal/app | 3.763s | 6.860s (timeout=10m) |
| internal/release | 1.973s | 2.662s |
| inspect, pgp, ssh, git, github, archrepo, archtrust, aur | Full ordinary suite | All focused package suites pass |
| run OnlyExit, cleanup/cancellation, population parsing, fixture expiration | Full ordinary suite | Selected regressions pass, 9.880s |
| Safe native run / pgp / release subset | 26.067s / 0.573s / 2.214s | 28.565s / 2.577s / 4.226s |

Full **unfiltered** `go test -count=1 ./...` passes, including disposable fixture
signing/key generation. `go mod verify`, `go vet ./...`, `go build ./...`, Fish
`set formatted (gofmt -l .); test (count $formatted) -eq 0`, and `git diff --check`
pass. Established sh checks cover install, prepare-release, render-install,
publish-release and test-minimal-arch; bash checks test-ci-arch. All 13 retained
Python fixtures parse with ast.parse, without generating bytecode.

The unprivileged native ordinary and race commands use `-count=1 -timeout=5m
-tags ownership_integration ./internal/run ./internal/pgp ./internal/release`
with this exact selection:

```text
^(TestNativeMakepkgHelpers|TestNativeCleanupCancellation|TestOwnershipPTYFailureCleanup|TestNativeGPGHelpers|TestNativeUpdaterOwnership)$
```

This covers real makepkg/GPG helper cleanup, descendant-cleanup cancellation,
PTY failure/workspace cleanup and updater tree/owner-death supervision. Ordinary
and focused race tests separately cover release child/grandchild expiration,
retained architecture fixture safety, and prompt/Flatpak workspace removal.
No native fixture changed, so the entire long native experiment suite was not
repeated. The full repository race suite and protected CI were not run locally.

Final resource comparison against the pre-test /proc, /tmp and cgroup baseline
found no new helper, heartbeat writer, command cgroup, scope, GPG home or fixture
workspace. Native supervisors also assert exact recorded PID/scope/workspace
cleanup. Pre-existing resources were left untouched. Small local reproduction,
validation and audit logs are in `/tmp/ops-wave-e-planning-audit`, not this commit.

Correction-delta implementer review: **Critical 0, Important 0, Optional 0**.
Ownership errors cannot become accepted facts; partial fatal plans are discarded;
original identities and immediate stop survive all tested boundaries; ordinary
problems remain nonfatal. Delegated scopes, CgroupFD, per-command cgroups,
cgroup.kill/populated=0, poisoning/shared Owner, approval/ownership/sudo/mutation
order, read-only Doctor/no-op/decline, Bubblewrap, manager-owned services, threat
boundaries and terminal/job control are unchanged.

One focused local correction commit follows successful validation. No prior
commit is rewritten. No push, PR, merge, tag/release, publication, R2, preserved
VM action, production signing, sudo execution, package installation, toolchain
or dependency change, or Wave F/later work occurred.
