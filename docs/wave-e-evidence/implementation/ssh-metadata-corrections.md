# Wave E SSH metadata propagation correction

Starting HEAD: `81784bd39d42eabf04e1ad739c722b73246ecf37`.
Branch: `fix/process-tree-cancellation`.
`origin/main` and remote main: `50adbe8518aee4108ee7bd7b6532e6a9ad9daf7f`.
The user authorized continuing with the five existing SSH correction files after
the initial clean-worktree preflight stopped. Those edits were preserved. This
pass strengthened the explicit GitHub call counter and corrected a brittle test
assertion about timeout text. The implementation below was already in that diff.

## Reproduction and correction

A Go source overlay of HEAD's two production SSH files reproduced the original
failure without reverting any working-tree edit. Direct/wrapped OwnershipError,
cancellation, deadline, and a real local HTTP client timeout all produced Issues:
**26 runner calls, metadata failure after call 24, two later GitHub calls**.
The corrected implementation produces Fatal: **24 runner calls, metadata failure
after call 24, zero later calls, zero later GitHub calls**, one metadata request.

`fetchGitHubHostKeys` previously recognized only cancellation of the caller's
context. Other request/read errors became `metadataUnavailableError`.
`InspectGitHubConfiguration` accepted that wrapper as unavailable freshness and
returned nil, discarding its cause before workstation inspection could stop.

Both request and body-read failures now pass through `metadataFetchError`, which
joins any caller cancellation/deadline with the original error and checks the
existing SSH interruption classifier. OwnershipFailed and errors.Is cancellation
or deadline take precedence over ordinary unavailability. The inspection discard
site independently applies that classifier before accepting unavailable metadata.
`metadataUnavailableError` already implements Unwrap; that behavior is retained.
The adjacent `GitHubConfigured` wrapper now returns `(bool, error)` instead of
discarding effective-local-configuration inspection failures. Its existing test
callers were updated; no other production caller required a change.

HTTP semantics follow error identity, not message strings or every net.Error
timeout flag. Go 1.26.7's http.Client timeout retains context.DeadlineExceeded with
a live parent context, for both awaiting headers and reading the response body.
An ordinary DNS timeout without that identity remains a source outage. DNS lookup,
network/TLS failures, truncated bodies, and the existing HTTP 408/403/429/5xx
availability states remain nonfatal. Invalid authoritative metadata still fails
as before; it is not silently accepted as a source outage.

## Regression matrix and audit

| Case | Result |
| --- | --- |
| Direct and wrapped OwnershipError | Original object retained through errors.As/Is and OwnershipFailed; Fatal; zero later calls |
| Cancellation and deadline returned with live parent | errors.Is retained; Fatal; zero later calls |
| Caller cancellation/deadline plus another transport cause | Both identities retained |
| Request and body-read failure, including unavailable wrapper around ownership | Original cause returned, no successful inspection status |
| Real bounded local HTTP timeout | Header/body deadline identity retained; Prepare Fatal and zero later calls; 10 repeated Prepare timeout tests pass |
| Ordinary metadata unavailability | Freshness unavailable; Prepare Issues; normal two GitHub queries still execute |
| Valid metadata | Current freshness; Prepare Success; normal inspection preserved |
| Pre-approval Prepare | No approval prompt, ownership activation, sudo, interactive command, or SSH-file mutation |
| Earlier Git identity mutation, then SSH configure/final-inspection failure | Applied values retained, Fatal and original cause, zero later work, “Earlier changes may remain” reported |

Doctor deliberately calls Workstation.Local, never remote SSH metadata inspection.
A remote metadata cause is therefore unreachable through real Doctor orchestration.
The Doctor regression verifies no metadata request even with an injected failing
transport, and separately injects all four fatal cases at its real local SSH
inspection boundary: original cause, Fatal, zero later calls/activation/mutation.
Healthy offline Doctor remains successful. No remote call was added to Doctor.

Reviewed all production SSH code: discovery, private fingerprinting, public-key
parsing/pairing, revalidation, agent state/load/unload, local/effective configuration,
remote metadata, and mutation gates. Also checked the adjacent GitHub authentication
and VerifySSH boundaries. Discovery/revalidation retain their earlier fatal checks;
agent expected exits still use compound-safe OnlyExit; pure parsing/filesystem
rejections do not hide external execution causes. No further equivalent discard
remains in this scope. Existing malformed-key, empty-agent, real OpenSSH, pairing,
idempotence, and configuration-preservation tests pass.

The existing timeout test was updated, not deleted: it now requires deadline
identity and an empty inspection status. The new orchestration test initially
required the literal sentinel text from a real timeout; Go may instead describe
transport cancellation while retaining deadline identity. That brittle assertion
was corrected to verify identity at the error-returning boundary.

Earlier planning regressions remain passing: application ownership/cancel/deadline,
AUR dependency failures, Doctor **24 total / failure at 24 / zero later**, Prepare
**27 / 27 / zero later**, and honest earlier-mutation reporting. Release's full
cause-retention matrix passes for ownership, cancellation, deadline, ordinary exit,
and structured run.Error, including bounded diagnostics without private details.
No release implementation changed.

## Validation and resource audit

All Go commands used **Go 1.26.7**, its bin directory first in PATH, `GOENV=off`,
`GOTOOLCHAIN=local`; go env reports an empty GOENV filename and `local`.

| Package | Ordinary, count=1 | Race, count=1 |
| --- | --- | --- |
| internal/ssh | PASS 1.230s | PASS 2.294s |
| internal/app | PASS 3.349s | PASS 6.995s, timeout=10m |
| internal/resolve | PASS 3.114s | PASS 4.489s |
| internal/release | PASS 1.660s | PASS 2.594s |

Full unfiltered `go test -count=1 ./...`: PASS (run 45.229s), including disposable
signing/key generation. The interrupted full invocation was incomplete and was
rerun successfully. `go vet ./...`, `go build ./...`, `go mod verify`, Fish gofmt
check, and `git diff --check`: PASS. Established sh syntax checks cover install,
prepare-release, render-install, publish-release and test-minimal-arch; bash checks
test-ci-arch. All 13 Python files parse without generating bytecode.

The established unprivileged native subset passed ordinary and race with
`-count=1 -timeout=5m -tags ownership_integration` for run/pgp/release, selecting
`^(TestNativeMakepkgHelpers|TestNativeCleanupCancellation|TestOwnershipPTYFailureCleanup|TestNativeGPGHelpers|TestNativeUpdaterOwnership)$`.
Ordinary: 28.301s / 1.546s / 3.092s. Race: 29.166s / 2.759s / 4.293s.
Supervisors proved exact PID, scope, cgroup and workspace cleanup, including owner
death and PTY failure. No sudo or package installation was used.

Final comparison against the pre-test /proc, /tmp and cgroup snapshot found no
session-created helper, heartbeat writer, command cgroup, scope, GPG home or fixture
workspace remaining. No new top-level /tmp entry remains relative to that snapshot;
the local audit directory was created before the snapshot. Unrelated desktop
processes were left untouched. Small logs, overlay reproduction, and snapshots are
retained only in `/tmp/ops-wave-e-ssh-audit-VJGArHx1`.

Correction-scope implementer review: **Critical 0, Important 0, Optional 0**.
This is ready for independent narrow re-review, not an independent review itself.
The cgroup architecture, poisoning/admission, shared Owner, approval/ownership/sudo
ordering, Bubblewrap, manager boundary, terminal behavior and fixtures are unchanged.
The full repository race suite was not run. Remote branch absence was verified.
No push, PR, merge, tag/release, publication, R2, preserved-VM action, production
signing, toolchain/dependency change, or Wave F/later work occurred. One focused
local commit follows; previous commits are not rewritten.
