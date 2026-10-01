# Wave E SSH-agent fatal-stop correction

Date: 2026-09-29. Branch: `fix/process-tree-cancellation`.
Starting HEAD: `a354c7c6d918b26e715968df9d3e892b90c5688b`.
`origin/main`: `50adbe8518aee4108ee7bd7b6532e6a9ad9daf7f`, unchanged after fetch.
Preflight matched: clean worktree/index, no stashes, exact branch and commits.

## Root cause and correction

`ssh-add -L` -> `AgentIdentities` returns the original execution error ->
`configureSSH` stores it in `issue.Err` and returns immediately -> `executePlan`
previously escalated only OwnershipError. With a live parent, cancellation and
deadline therefore reached the ordinary result with `stopInspection=false` and
`applied=true`; `preparePlan` reinspected the workstation, including GitHub.

The independent review reported **24 later calls, including two GitHub calls**.
An external Go source overlay against the unchanged starting tree reproduced
Issues for both live-parent cancellation and deadline, with **25 later runner
calls, including two GitHub calls** in this session's ready-workstation fixture.
The same overlay fixture after correction returns Fatal with **zero later calls
and zero later GitHub calls**. The differing fixture count is recorded explicitly;
the independent 24-call count is not claimed as a fresh measurement. No history
was rewritten or repository source temporarily replaced for reproduction.

Only `internal/app/prepare.go` changes production behavior. Its identity
aggregation now reuses `resolve.StopsPlanning`: OwnershipFailed or errors.Is
cancellation/deadline, including wrappers. The existing `stop` result retains
the original error, returns Fatal immediately, and sets `stopInspection=true`.
`identity.go` remains unchanged: its Issue already preserves the full error.
`lifecycle.go` remains unchanged: its guard now correctly suppresses reinspection;
a failure first encountered during final inspection already reports Fatal.
No new classifier or generic error framework was introduced.

## Deterministic application coverage and boundary audit

All fatal injections use a live parent and a healthy fake Owner. No test relies
on poison blocking a later command. Runner admissions and later GitHub calls are
counted separately; each fatal case requires both counts to be zero.

| Case | Result |
| --- | --- |
| Direct/wrapped cancellation and deadline | Original errors.Is identity, Fatal, stopInspection, zero later calls |
| Direct/wrapped OwnershipError | Original object and fatal classification retained; zero later calls |
| Initial public Prepare agent inspection | Fatal before approval, ownership activation, sudo or mutation; SSH files unchanged |
| Approved reconciliation agent inspection | Immediate Fatal; no later inspection, prompt, sudo, SSH-file change or mutation |
| Earlier Git name/email mutation, then agent failure | Earlier values retained, Fatal, original cause, no reinspection; “Earlier changes may remain” |
| Agent failure first reached in final reinspection | Fatal, original cause reported, earlier Git values retained, no later calls or mutation |
| Empty agent / exit 1 with exact empty-state text | Nonfatal; existing managed-load behavior passes |
| Uncontactable agent / exit 2 | Existing unavailable state remains nonfatal |
| Ordinary unexpected agent failure | Original Issue retained; reconciliation continues as before |
| Malformed/unrecognized agent keys | Existing ignored-line semantics retained, nonfatal |
| Valid agent / unrelated identities / no managed key loaded | Normal reconciliation and managed-load planning retained; two final GitHub queries |

Doctor explicitly sets `SkipAgent=true`; even incomplete configuration must not
depend on a session agent (existing Doctor contract and tests). Consequently an
agent-returned error is unreachable through Doctor. New tests verify that skip,
then inject all six fatal cases at its reachable local SSH fingerprint inspection:
Fatal, original diagnostic, zero later calls, no ownership activation or mutation.
Existing effective-SSH-configuration interruption regressions also pass. No agent
query was added to Doctor to manufacture coverage of an unreachable path.

The immediate app identity audit found equivalent omissions in Git error
aggregation, GitHub Issue aggregation (authentication/keys/verification), and
the standalone VerifySSH call after SSH reconciliation. All now use the same
classifier before continuing. The latter also now stops an unpoisoned returned
OwnershipError. The table-driven regression exercises these paths with all six
causes. SSH discovery and managed-key lookup already return their original fatal
error; no change needed. SSH creation/load Issues receive the shared correction;
the existing cancelled-load expectation changes from Issues to Fatal, while
ordinary passphrase rejection stays Issues.

Initial Workstation Local/External identity aggregation already returns errors
immediately. GPG is not aggregated by `identity.go`: AUR signing-key inspection
already checks StopsPlanning in resolve, and app AUR key import/revalidation
returns wrapped errors to the existing application fatal-stop boundary. No GPG
or lower-level identity implementation changed.

## Regression and validation

Every Go invocation used **Go 1.26.7**, its bin directory first in PATH,
`GOENV=off`, `GOTOOLCHAIN=local`; go env reports empty GOENV filename and `local`.

| Package | Ordinary count=1 | Race count=1 |
| --- | --- | --- |
| internal/app | PASS 4.825s | PASS 7.208s, timeout=10m |
| internal/ssh | PASS 1.179s | PASS 2.245s |
| internal/resolve | PASS 2.809s | PASS 4.169s |
| internal/release | PASS 1.527s | PASS 2.520s |

SSH metadata regression passes unchanged: direct/wrapped/nested ownership,
cancellation, deadline, real HTTP timeout, ordinary outage, valid metadata,
earlier mutation, and **24 total / failure at 24 / zero later / zero later GitHub**
(the earlier metadata evidence records the pre-fix 26/24/2). Private fingerprint,
revalidation, private-key interruption, empty-agent, malformed/unrecognized key,
pairing and real OpenSSH tests pass. General planning regressions retain Doctor
**24/24/0**, Prepare **27/27/0**, AUR dependency fatal-stop and earlier-change
reporting. Release's ownership/cancellation/deadline/structured run.Error cause
retention and privacy matrix passes; no release or metadata code changed.

Full unfiltered `go test -count=1 ./...`: PASS, including disposable test signing.
`go mod verify`, `go vet ./...`, `go build ./...`, Fish gofmt check and
`git diff --check`: PASS. Established sh syntax checks cover install,
prepare-release, render-install, publish-release and test-minimal-arch; bash
checks test-ci-arch. All 13 Python fixtures parse without bytecode generation.
There are no additional production packages affected.

The established bounded unprivileged native ordinary subset passes with
`-count=1 -timeout=5m -tags ownership_integration` for run/pgp/release and selection
`^(TestNativeMakepkgHelpers|TestNativeCleanupCancellation|TestOwnershipPTYFailureCleanup|TestNativeGPGHelpers|TestNativeUpdaterOwnership)$`.
Times: 25.596s / 0.691s / 2.298s. Supervisors assert PID/scope/workspace cleanup.
No ownership/native code changed; long native architecture proofs and the full
repository race suite were not repeated.

Final comparison with the pre-test /proc, /tmp and cgroup snapshot found no new
helper, agent, heartbeat writer, cgroup, scope or fixture workspace. No new /tmp
entry remained; the external audit directory predated the snapshot. No ops scope
remained in the user manager. External overlay sources, small logs and snapshots
are retained only in `/tmp/ops-wave-e-agent-audit`.

Correction-scope implementer review: **Critical 0, Important 0, Optional 0**.
The returned cause controls stopping independently of the parent/Owner state;
ordinary SSH problems retain their prior semantics. Delegation, CgroupFD,
cgroup.kill/populated=0, Owner poisoning, approval/ownership/sudo ordering,
Bubblewrap, service-manager boundaries, terminal behavior and fixtures are intact.
One focused local commit follows validation. No push, PR, merge, tag/release,
publication, R2, preserved-VM mutation, production signing, sudo execution,
package installation, toolchain/dependency change or Wave F/later action occurred.
