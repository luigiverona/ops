# Wave E second-review follow-up corrections

Starting branch: `fix/process-tree-cancellation`.
Starting HEAD: `854f1f45cef7d034ca75172db179a267189f51ca`.
`origin/main`: `50adbe8518aee4108ee7bd7b6532e6a9ad9daf7f` before/after fetch.
Preflight and the pre-edit recheck matched; worktree/index clean, no stashes.

The second independent review reported **Critical 0, Important 2, Optional 1**.
It confirmed earlier I1–I3 but found I4 incomplete. Its precise findings were
provided by the user, not a repository review file. This report supplements
`corrections.md`; that historical implementer review was not a clean independent
re-review.

## C1 — release fixture independent expiration and failure cleanup

The old embedded staged executable armed `alarm(20)` before `fork()`. The child
inherited the handler but not the active timer, then looped indefinitely. The
reviewer's supervised observation of survival for 21.52 seconds is supplied
review evidence, not a reproduction run by this correction session. The unsafe
native test was not run before correction.

The staged executable now comes from `internal/release/testdata/ownership.py`.
It resets SIGALRM to SIG_DFL and arms ITIMER_REAL initially and in an at-fork child
hook. Every looping process has a fresh 20-second hard deadline. Only this test
script reads `OPS_RELEASE_FIXTURE_SECONDS` (validated greater than zero and at
most 20); the safety test uses one second. TERM/INT are ignored to retain stubborn
descendant coverage. A child performs setsid and forks a grandchild, so the same
fixture explicitly exercises both fork generations. There is no exec after
fork; the staged executable's initial interpreter exec precedes timer setup.
No production timing or release implementation changed.

A surviving Python subreaper owns the workspace and supervises the actual test
program. The ordinary expiration test observes active independent timers in all
three records, kills/reaps the direct parent, then sends **no cleanup signal** to
the child or grandchild during the measured expiration interval. Both must be
reaped with SIGALRM status within two seconds of their one-second timer record.
All recorded PIDs must disappear. Assertion/timeout cleanup kills only this
single-threaded supervisor's live, unreaped children; waitability prevents PID
reuse between its nonblocking wait and signal. No global PID/name scan authorizes
signalling. This fallback also handles children that die before writing records.

The native test now runs its Go owner in a supervised subprocess. Before any
workload starts, the helper records its activated scope's actual membership.
Each process record must match one command cgroup immediately below that exact
scope. Normal cancellation still exercises Replace, its independent recovery
contexts, command removal and absence of staged files. Reaping is now bounded
and nonblocking.

The second native mode deliberately SIGKILLs the Go owner after all three
processes are recorded. It first requires the exact scope to remain populated,
then the surviving supervisor invokes the existing exact-scope cleanup utility:
cgroup.kill, population/collection observation, reaping and scope disappearance.
The scope name must match the launched owner's PID plus the generated 128-bit
suffix under the current user's hierarchy. The supervisor checks every recorded
PID absent and removes only its own TemporaryDirectory. The outer timeout allows
the full inner timeout plus cleanup budgets. No scope or directory is selected
for deletion by a broad name scan.

## C2 — expected exit codes cannot discard inspection failure

`run.Exited` uses errors.As, so an exit-3 result was discoverable even when joined
with OwnershipError. The service branch accepted `inactive` and silently dropped
the whole compound error. The same pattern existed for disabled/missing services
and empty foreign-package inventory in this inspection function.

A local `expectedInspectionExit` classifier now rejects the existing typed
ownership classification and errors.Is cancellation/deadline identities before
considering an expected exit. It follows ordinary single-error wrappers and
rejects compound error trees, including an expected exit joined with an unrelated
inspection failure. The caller wraps and returns the original error with `%w`.
The global run.Exited API and production process ownership architecture are
unchanged. Silent ordinary inactive/disabled/not-found results keep their prior
state semantics; unexpected exits retain their failure semantics.

Deterministic tests use actual Workstation.State, injected runners, run.Error,
fmt wrapping and errors.Join. They cover is-active exit 3/4, is-enabled exit 1/4,
and the nearby pacman exit-1 path. Each has ordinary expected, OwnershipError,
cancellation, deadline, joined unexpected error, and unexpected exit cases.
Failure cases preserve the original object/identity and issue no subsequent
command. Expected cases assert the target response was actually exercised.

Application tests exercise initial Prepare and final inspection after successful
Git identity mutation. The observation boundary retains the original
OwnershipError. Both lifecycles return Fatal and execute no command after the
failure; initial failure never reaches approval or mutation, while post-mutation
failure retains the applied Git values and reports that earlier changes may
remain. Ownership failure is not mislabeled as interruption.

## O1 — PTY workspace ownership

Fixed locally in the fixture: the surviving Python PTY supervisor creates the
heartbeat/record directory inside its own workspace and supplies that exact path
to the Go helper. The killed helper no longer creates a t.TempDir. The existing
injected failure test requires proof of live descendants after owner-only cleanup,
then exact scope/PID cleanup, then a workspace-removal marker. Python asserts the
workspace disappeared; the outer Go test independently stats that exact path.
No global temporary-directory sweep was introduced. Pre-existing leaked directories
from earlier sessions are not this session's resources and were left unchanged.

## Validation

All Go commands used `~/.local/opt/go1.26.7/bin`, `GOENV=off`,
`GOTOOLCHAIN=local`. `go version` reported `go1.26.7 linux/amd64`;
`go env GOENV GOTOOLCHAIN` reported an empty GOENV filename and `local`.

| Package/check | Ordinary | Race |
| --- | --- | --- |
| internal/release | PASS 1.517s | PASS 2.417s |
| internal/inspect | PASS 0.016s | PASS 1.057s |
| internal/app | PASS 3.314s | PASS 6.657s |
| internal/run | PASS 39.468s | PASS 561.485s, timeout=30m |
| internal/aur | PASS 0.007s | PASS 1.020s |
| internal/ssh | PASS 1.107s | PASS 2.105s |
| Safe native internal/run | PASS 40.285s | PASS 42.417s |
| Safe native internal/pgp | PASS 0.515s | PASS 1.546s |
| Safe native internal/release | PASS 2.210s | PASS 4.237s |

The native ordinary/race selection was:

```sh
go test -count=1 -timeout=3m -tags ownership_integration \
  ./internal/run ./internal/pgp ./internal/release \
  -run '^(TestNativeOwnership|TestNativeCrashScopeLifetime|TestNativeGPGHelpers|TestNativeUpdaterOwnership|TestOwnershipPTY|TestOwnershipPTYFailureCleanup|TestNativeMakepkgHelpers|TestNativeCleanupCancellation)$'
```

The race invocation adds `-race`. Ordinary release tests also run
`TestReleaseFixtureIndependentExpiration`, including in the full suite and
focused race suite. The initial focused expiration proof passed in 1.083s.
The initial native parent-death proof and PTY injected-failure test passed,
including the live-population prerequisite and both cleanup assertions.
No privileged native selection was executed.

Earlier I1–I3 regressions remain passing: makepkg build/packagelist and unexpected
helpers require actual kill, population zero and removal; owned-cleanup tests
preserve context identity and prior independent failure precedence; SSH discovery
and application tests preserve fatal identity and ordinary malformed-key behavior.
Existing internal/run child/grandchild expiration tests pass unchanged.

Full `go test -count=1 ./...`: PASS (internal/run 39.229s; every package passed).
`go mod verify`, `go vet ./...`, tagged run/release vet, `go build ./...`, Fish
`set formatted (gofmt -l .); test (count $formatted) -eq 0`, and `git diff --check`:
PASS. AST parsing of all three affected Python files passed without producing
bytecode. Established sh/bash syntax checks passed. No shell file changed.
The complete repository race suite was not run; protected CI was not invoked.

## Correction-delta security review and preservation

Implementer review of the complete correction delta: **Critical 0, Important 0,
Optional 0 remaining in this correction scope**. This is ready for independent
re-review, not a claim that the second review was clean.

- Hard expiration is rearmed after both fork generations. The surviving
  supervisor, not the killed owner, owns final reaping and workspace deletion.
- Direct PID signals target only a retained Popen child or an unreaped waitable
  child of the single-threaded supervisor. Recorded PIDs otherwise authorize
  observation/reaping only, not signalling an arbitrary reused PID.
- Cgroup cleanup reuses the exact recorded scope validator and bounded cleanup;
  no shared session, broad cgroup, process-group or user-wide kill was added.
- Error classification uses typed ownership and context identities, rejects
  joined failures and preserves the original error chain. Ordinary service
  state remains nonfatal. Tests verify actual injection and zero later commands.
- Only `internal/inspect/inspect.go` changes production behavior. Direct user-bus
  bootstrap, delegated scope, CgroupFD placement, per-command groups, cgroup.kill,
  populated=0 authority, poisoning/shared Owner, approval/ownership/sudo/mutation
  ordering, Doctor/no-op/decline, fail-closed support checks, Bubblewrap,
  service-manager boundary, terminal behavior and threat model are unchanged.
- No existing Wave E commit is amended/rebased. Go/module/dependency files,
  CI workflows and scripts remain unchanged.

Existing architectural limitations remain: deliberate same-UID/root cgroup
escape is outside the lifecycle model; manager-created services have a separate
lifetime; production SIGKILL crash supervision and uninterruptible kernel tasks
are not solved by test-only supervision. I-11's existing race stress cost remains.

## Exact resource audit

Each native supervisor validates its recorded unique scope, each recorded PID,
command-group removal and exact TemporaryDirectory removal before success.
The release expiration supervisor additionally requires no remaining waitable
children. Example paths observed and confirmed absent in focused failure tests:
`/tmp/ops-release-ownership-97ym1ocq` (expiration),
`/tmp/ops-release-ownership-6q5p6swd` (normal),
`/tmp/ops-release-ownership-vksy15bf` (owner death), and
`/tmp/ops-pty-cleanup-cgo9dmtq` (PTY failure).

A read-only baseline/final comparison after all tests found zero new GPG helpers,
zero fixture Python/staged processes, zero fixture heartbeat FDs, zero ops fixture
scopes or service-fixture cgroups, and zero new fixture temporary paths. No
pre-existing helper changed argv. Preserved helper PIDs: 85311, 85316, 87542,
87556, 87780, 87796, and the existing dirmngr 304883. None was signalled/altered.

The ten pre-existing `/tmp/TestOwnershipPTYHelper` directories with suffixes
1157398734, 1242266740, 1840959764, 2436339227, 2635225181, 267791087,
301534226, 3122467909, 386378091, and 4017564773 remain untouched. This session
created no additional directory of that kind. Small local validation logs/audit
JSON live under `/tmp/ops-wave-e-followup-audit`; they are evidence, not running
fixtures, and are not committed.

No push, PR, merge, tag, release, artifact publication, R2, preserved-VM operation,
release signing, Go/dependency upgrade, sudo password, package installation,
protected CI invocation, or Wave F/later work occurred. The final focused local
commit and clean git status are reported with delivery.
