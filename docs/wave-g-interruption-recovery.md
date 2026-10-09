# Wave G — cooperative interruption and recovery evidence

Implementation evidence for I-04. Formal closure remains pending independent
re-review, final verification, protected CI, squash merge, merged-tree equality,
and canonical closure. The first independent review found one **Important**
EXIT-cleanup/status defect. The local correction checkpoint below records its
implementation and validation; Wave G remains local/unmerged and I-04 OPEN.

Sections through the initial self-review retain the first implementation's
historical evidence (commit `e8bba7730fa655db1e2fd59fa80828d0ba747bea`). The
correction section supersedes its cleanup model and initial zero-finding claim.

## Preflight

On 2026-10-03, main and fetched origin/main were exactly
`acc3b28912a3d4a99340e91fe2981c70faaa8fbf`; clean index/worktree, no stashes,
remote main only, open PRs zero. Strict protected checks ci, build, and
minimal-runtime succeeded on that exact commit (runs 37157785747 and
37157785721). The canonical handoff's earlier SHA is its explicitly historical
pre-documentation snapshot. Wave F is complete; Wave G was NEXT.

## Invariants recorded before production implementation

- Pre-mutation: HUP/INT/TERM terminate; only disposable state may be removed;
  no later install, configuration, signing, rendering, or publication step.
- Backup copying: target remains untouched. Partial backup is disposable until
  copying succeeds. Before replacement starts, backup becomes recovery-required.
- Replacement attempted or completed, verification pending: preserve the prior
  binary backup. Do not attempt complex recovery in a signal handler. Report the
  recovery path without claiming restoration or success.
- Verification accepted: backup is obsolete and may be deleted. A signal before
  that explicit state transition conservatively retains the backup.
- Failed verification: normal recovery may restore the old binary. Failed or
  interrupted recovery retains the backup if it still exists. Successful atomic
  restoration makes it obsolete; the durable old target remains.
- Configuration: preserve the installed binary and any independently created
  apps.toml. Cleanup is restricted to this run's private stage. Never print a
  normal success conclusion after a terminating signal handler.
- Release preparation/rendering: disposable stages clean; no final publication
  begins after the handler. An already completed rename remains durable.
- Publication: terminating traps stop future operations; already published
  objects remain. Cooperative interruption does not make publication atomic.
- Exit policy: outer shell HUP=129, INT=130, TERM=143. Existing configuration
  subshell exit 2 remains its internal failure contract.
- Recovery paths left by earlier runs are not disposable. Refuse collisions
  before claiming ownership; cleanup must not remove them.

## Pre-fix real-signal reproduction

Disposable scripts outside the repository used a pipe-acknowledged blocking
foreground command, then a `continued` sentinel. The parent delivered the real
signal before releasing the command. `/bin/sh` resolves to `/usr/bin/bash`,
GNU bash 5.3.20(1)-release. Both `sh` and `bash`, for each of HUP, INT, TERM,
exited 0 with `cleanup`, `continued`, `cleanup`: signal handler returned, normal
work resumed, and EXIT cleanup ran again. No signal-specific difference.

The existing installer fixture (fake sudo/curl/GPG, disposable target/home)
was tested before changing any production script, at three exact boundaries.
All nine real-signal cases failed the intended regression assertions:

| Boundary | Signals | Exit | Later sentinel | Target | Backup/staged | Config/success |
| --- | --- | --- | --- | --- | --- | --- |
| Before sudo authorization | HUP/INT/TERM | 2 | ran | old | absent | absent; later staging attempted with deleted download |
| Backup complete, before replacement | HUP/INT/TERM | 2 | ran | old | both deleted | absent; replacement attempted with deleted stage |
| Replacement complete, before target verification | HUP/INT/TERM | 0 | ran | new | both absent | apps.toml created; Installed success printed |

The last row proves both recovery-evidence loss and continuation through later
configuration work. Temporary download directories were removed in all cases.
No real sudo, release download, signing, workstation target, or remote mutation
was used.

A second configuration reproduction, after fixing only the outer terminating
traps, sent each signal to the parent during its foreground configuration
subshell. All three exited with 129/130/143, but the child still ran the later
sentinel and attempted `ln`. A concurrent config made that link fail in the
fixture; without that race the child could publish. This is why the change also
moves exclusive publication into the waiting parent. It is not a consistency
rewrite of the already-correct child signal traps.

## Complete signal/trap inventory

The tracked-file inventory and searches included every tracked file, including
workflows, testdata, and historical evidence. Search terms: trap, HUP, INT,
TERM, SIGINT, SIGTERM, os.Interrupt, signal.Notify, signal.NotifyContext,
cleanup, backup, staged, recovery. The baseline broad search returned 691 lines;
most are package backup metadata, identifiers, prose, or ordinary cleanup.

| Active site (baseline location) | Classification before change | Final behavior |
| --- | --- | --- |
| `script/install.sh:27` top level | **Vulnerable** | EXIT cleanup; separate HUP/INT/TERM exit 129/130/143; state-aware backup preservation |
| `script/install.sh:210–211` configuration subshell | **Correct terminating handler**; parent/child publication boundary independently vulnerable | Keep EXIT private-file removal and signal exit 2; successful writer transfers file cleanup to parent, which owns exclusive publication |
| `script/prepare-release.sh:69` and preflight | **Vulnerable** cleanup-only handler and untrapped preflight INT | EXIT cleanup before stage allocation; terminating signal traps at startup |
| `script/prepare-release.sh:136` | Correct post-publication trap reset | Remains after successful final rename |
| `script/render-install.sh:33` GPG command substitution | **Vulnerable** | EXIT GPG-home cleanup; separate terminating HUP/INT/TERM traps before allocation |
| `script/render-install.sh:50` outer output phase | **Vulnerable** | EXIT output-temp cleanup; outer terminating handlers installed before GPG phase |
| `script/render-install.sh:89` | Correct post-publication trap reset | Remains after successful final rename |
| `script/publish-release.sh:116–119` | **Correct once installed**; preflight before registration independently **vulnerable** to parent-only INT | Existing signal exits 129/130/143 moved to startup; EXIT cleanup registered before allocation |
| `script/publish-release.sh:453` | Correct post-publication trap reset | Unchanged; no further mutation after reset |
| `script/test-ci-arch.sh:5` | **Unrelated** ERR diagnostic; missing INT handler independently **vulnerable** | ERR unchanged; startup adds terminating HUP/INT/TERM handlers |
| `.github/workflows/ci.yml:194` | **Unrelated** ERR diagnostic | ERR handler unchanged |
| All inline `run` blocks: `.github/workflows/ci.yml` (7), `arch.yml` (2), `release.yml` (3) | No baseline signal handlers; same **vulnerable** Bash startup semantics | Each entry point now terminates HUP/INT/TERM with 129/130/143 |
| `script/test-minimal-arch.sh` outer and runuser heredoc | No baseline trap/signal site; missing INT handler **vulnerable** | Both shell entry points add terminating HUP/INT/TERM handlers |
| `cmd/ops/main.go:36` | **Correct** Go SIGINT/SIGTERM context cancellation | Unchanged |
| `internal/run/execute_linux.go:122` | **Correct**, sends SIGTERM; not a handler | Unchanged Wave E ownership teardown |

All remaining registration sites are **historical/test-only**:

- `internal/app/terminal_test.go:138` and
  `internal/run/ownership_native_test.go:414`: NotifyContext test helpers.
- `internal/run/testdata/ownership.py:10,14–15` and
  `internal/release/testdata/ownership.py:15,21–22`: reset SIGALRM and deliberately
  ignore SIGINT/SIGTERM to test owned-process cleanup.
- `docs/wave-e-evidence/architecture/workload.py:7,11` and
  `docs/wave-e-evidence/reproduce.py:32,36`: historical SIGALRM reset/SIGTERM
  ignore fixtures.
- `docs/wave-e-evidence/architecture/privileged-ci-proof.patch:586`: historical
  EXIT-only proof-directory cleanup.
- New `internal/installer/interruption_test.go`: the checkpoint helper ignores
  HUP/INT/TERM so group delivery cannot release its gate prematurely; the test
  sends actual signals to the parent, phase shell, or fixture process group.
  SIGKILL is only emergency fixture teardown, not a production recovery claim.

The remaining signal sends in Python/Go test supervisors, EINTR handling,
terminal TERM variables, signing fingerprint markers, package backup metadata,
and ordinary context cleanup are unrelated to returning shell signal handlers.
No additional active registration site was found.

## Production design and recovery lifecycle

`install.sh`, `prepare-release.sh`, and both render phases split EXIT cleanup
from terminating signal handlers. All top-level trapped HUP/INT/TERM statuses
are 129/130/143. The GPG phase exits with those statuses when signalled directly;
its caller treats phase failure as its existing invalid-key failure (exit 1).
The configuration writer retains exit 2 when signalled alone. Group or parent
signals select the outer conventional status. No signal re-raising is required.

Installer cleanup variables start empty/safe, preventing inherited environment
values from claiming files. Staging-path collisions fail before assigning owned
paths, preserving a prior run's evidence even on PID reuse. The old unconditional
pre-install removal of staged/backup names is gone.

`backup_required=no` allows cleanup of incomplete copies while the old target is
still untouched. After successful backup copy, set `backup_required=yes` before
invoking replacement. A signal before that assignment is still pre-replacement:
removing the copy is safe because the old target is intact. Once replacement can
have begun, neither a nonzero mv status nor a signal can retire the backup.
Cleanup reports its retained path. Only successful target-version verification
or successful atomic restoration sets `backup_required=no`. Failed restoration
keeps it true, including a signal before the failure diagnostic. If restoration
already moved the backup to the target, the old binary is durable even if a
signal arrives before the flag reset. No handler attempts rollback.

Configuration still uses the physically pinned destination and exclusive hard
link publication. Descriptor 8 pins the private stage; its physical location is
validated before cleanup claims its contents. The writer subshell retains its
terminating traps and cleans failed writes. On success it hands cleanup to the
parent. The parent links from that pinned stage into its pinned current directory
only after the writer returns. Parent-only interruption therefore stops the
link, even when the in-flight writer completes. EXIT removes only the private
staged file and attempts to remove the empty private directory. A concurrent
apps.toml, symlink, device, FIFO, or changed destination directory retains the
existing race defenses. The stage-descriptor replacement regression also proves
cleanup cannot unlink an unrelated configuration through a raced-in symlink.

A late self-review probe established a separate Bash startup rule: without an
INT trap, a parent-only SIGINT while a foreground command returns normally can
be ignored. The minimal probe produced HUP=-1, INT=0 with a continuation
sentinel, TERM=-15. Full disposable prepare/publish preflight fixtures then
reproduced INT=0 with Prepared/Published output; fake publisher uploaded all
five objects. The exact startup prologues of both CI scripts also resumed after
INT. HUP/TERM terminated those untrapped parent shells immediately and temporarily
left the acknowledged helper alive; fixture teardown killed those exact groups.
These reproductions preceded the startup changes. No real bootstrap was run.

Consequently prepare, publish, and both CI shell entry points now trap before
blocking commands. The minimal-runtime runuser heredoc gets the same handlers.
Its normal parent can wait for an in-flight guest command; this does not allow
later parent work. The same three terminating traps now start every inline Bash
`run` step in the three tracked GitHub workflows (12 entry points). Their startup
prologues are tested without executing workflow work. The existing hosted ERR
diagnostic remains unchanged. No workflow architecture, action pin, or runner
cancellation policy was redesigned.

The installer never reports successful completion after these interruptions.
An already installed binary remains installed during configuration interruption;
an unverified replacement remains alongside its retained previous binary. This
is evidence preservation, not an assertion that the new binary was accepted.

Prepare-release removes disposable stage state and stops before later build,
sign, or final rename. Render-install cleans its isolated GPG home and temporary
output without publishing a final installer after interruption. Cleanup traps
are registered before temporary allocations. Signing identity and verification
logic are unchanged.

Publish-release's established terminating handlers were correct once installed.
A separate preflight reproduction exposed its earlier untrapped INT interval:
after direct parent SIGINT the fake fixture uploaded all five objects and
printed Published with exit 0. Its only production changes move those handlers
to startup and register safe EXIT cleanup before allocation. Tests interrupt it
after the first fake immutable object is published and verified. The object
remains; no checksum/signature/installer/latest upload follows; local private
state disappears and the kernel lock is available. There is no remote rollback
claim and no real AWS/R2/public endpoint use.

## Real-signal matrix

Each row runs HUP, INT, and TERM independently. The 102 cases cover shared signal
semantics plus every recovery state, nested-shell boundaries, and durable partial
publication. All use an acknowledged pipe checkpoint, signal before gate release,
10-second checkpoint/exit bounds, a 5-second emergency reap bound, isolated
process groups, file-backed output, and exact-group teardown on failure. Normal
completion asserts the entire fixture process group is absent. There is no sleep
used to choose a signal checkpoint and no signal handler invoked as a function.

| Fixture/checkpoint | Delivery | Expected result for HUP / INT / TERM |
| --- | --- | --- |
| Prepare-release and publish-release preflight | parent | 129 / 130 / 143; no later build, signing, or AWS calls |
| CI guest script and minimal-runtime outer/inner shell startup prologues | parent | 129 / 130 / 143; no continuation sentinel; no actual bootstrap run |
| All 12 hosted workflow shell startup prologues | parent | 129 / 130 / 143; no continuation sentinel; no workflow commands run |
| Installer before sudo authorization | parent | 129 / 130 / 143; old target; no staging, backup, or config |
| Installer backup created, before replacement | parent | 129 / 130 / 143; old target and retained old backup; staged file removed |
| Installer replacement complete, before verification | parent | 129 / 130 / 143; new target and retained old backup |
| Installer rename completed, command still in flight and returning failure | parent | 129 / 130 / 143; new target and retained old backup |
| Installer verification failed, before restore | parent | 129 / 130 / 143; failed new target and old backup |
| Installer restore failed, before diagnostic | parent | 129 / 130 / 143; old backup survives EXIT and its path is reported |
| Installer target verified, before backup removal | parent | 129 / 130 / 143; accepted new target remains, obsolete backup cleaned |
| Installer old target restored, before failure report | parent | 129 / 130 / 143; old target remains, no backup needed |
| Config writer finished, before ownership transfer | phase and group | phase: 2 / 2 / 2; group: 129 / 130 / 143; concurrent config preserved; private stage removed |
| Config writer in flight | parent | 129 / 130 / 143; parent does not publish; concurrent config preserved; accepted binary remains |
| Prepare-release stage exists, before build | parent | 129 / 130 / 143; stage removed, no signing/final directory/Prepared |
| Render GPG private-home phase | phase, group, parent | phase: caller exit 1; group/parent: 129 / 130 / 143; home removed, no output publication |
| Render temporary output complete, before rename | parent | 129 / 130 / 143; temp removed, prior output unchanged |
| Publisher first immutable object durable | parent | 129 / 130 / 143; one object remains, no later upload, stage and lock released |

Every case rejects a post-signal sentinel and false normal success output.
Target, backup bytes, config, output, publication log, and private temporary
paths supply independent state assertions. Existing ordinary installer tests
cover signature/status rejection, safe paths, permissions, concurrent config,
changed directories, normal success, and decline. Additional tests cover ordinary
failed-restore retention, both earlier recovery-path collisions, and stage-FD
symlink substitution. Fixture hooks modify checked temporary copies only;
production has no fault-injection bypass.

## Go and later-wave boundaries

Reviewed `cmd/ops/main.go`, `internal/app/interruption.go`, `lifecycle.go`,
`update.go`, and the adjacent updater replacement/ownership boundary. Main still
cancels context on SIGINT/SIGTERM. The cancellation runner checks before and
after work, mutation state governs honest interruption messages, lifecycle
suppresses final inspection after cancellation, and claimConclusion prevents a
healthy final report after interruption. Update retains independent recovery and
ownership teardown. No separate concrete Go I-04 defect was found; no production
Go code changed. Go command exit 2 remains distinct from conventional shell
signal statuses. Existing app cancellation/terminal tests remain the regression
evidence; no Wave E ownership redesign is involved.

Wave H terminal ownership/presentation was not started. Wave L SIGKILL, hard
crash, power-loss-like termination, and convergence were not implemented or
claimed. An already-running foreground utility may finish before the shell runs
its pending trap; the invariant is that no later normal mutation/publication
step starts after that boundary. These shell tests do not claim a new process
ownership architecture or bounded cancellation of arbitrary external utilities.

## Initial implementation validation and self-review

Local environment: Arch Linux x86_64, Bash 5.3.20 invoked as `sh`, exact
`go1.26.7 linux/amd64`; PATH prefixed with `~/.local/opt/go1.26.7/bin`, GOENV=off,
GOTOOLCHAIN=local. `go env GOENV` reports an empty filename when disabled;
`go env GOTOOLCHAIN` reports `local`. No toolchain or dependency change.

| Validation | Result |
| --- | --- |
| `go test -v -count=1 -timeout=3m ./internal/installer -run 'Signals$'` | PASS, 102 real-signal cases |
| `go test -count=1 ./internal/installer` | PASS, all installer/release fixtures |
| `go test -count=1 ./internal/app` | PASS, including existing terminal/cancellation contracts |
| `go test -race -count=1 -timeout=5m ./internal/installer` | PASS; affected concurrent fixture package |
| `sh -n script/install.sh` | PASS |
| `sh -n script/prepare-release.sh` | PASS |
| `sh -n script/publish-release.sh` | PASS |
| `sh -n script/render-install.sh` | PASS |
| `sh -n script/test-minimal-arch.sh` | PASS |
| `bash -n script/test-ci-arch.sh` | PASS |
| `bash -n` on each extracted hosted workflow shell block | PASS, 12 separate invocations |
| `go mod verify` | PASS, all modules verified |
| `go test -count=1 ./...` | PASS |
| `go vet ./...` | PASS |
| `go build ./...` | PASS |
| `git diff --check` | PASS |

Full ordinary validation includes the existing release replacement, run ownership,
and app interruption tests. No full local repository race rerun or protected
native-VM CI run is claimed for this unpushed branch. The affected fixture package
has focused race coverage; no production Go code changed. Historical protected
main checks are preflight evidence, not new feature-branch acceptance.

Self-review against `acc3b28912a3d4a99340e91fe2981c70faaa8fbf` checked every
production mutation/cleanup transition and the entire branch diff. Remaining
**Critical: 0; Important: 0; Optional: 0** within Wave G. The parent-only config
publication gap found during implementation was corrected and regressed, not
deferred. Existing signature/checksum/pinned-key checks, atomic replacement,
sudo boundaries, config exclusivity/safe paths, provenance, cgroup ownership,
R2 conditional publication, and immutable verification remain intact.

The implementation is ready for independent review, but **I-04 is not formally
CLOSED and Wave G is not formally complete**. The canonical handoff owns roadmap
status. This local session does not authorize or claim a push, PR, merge, release,
R2 operation, preserved-VM operation, production signing, dependency refresh,
Wave H work, or Wave L/hard-crash guarantees. Test subprocess groups were absent
at normal fixture completion; disposable paths are owned by test cleanup. The
final local commit and clean/unpushed Git state are reported in the session
handoff rather than embedded as a self-referential commit SHA here.

## Independent review correction — 2026-10-09 local checkpoint

### Finding and verified start

The first independent Wave G review found **one Important issue**: unguarded
EXIT cleanup under `set -e` could replace an already-selected HUP/INT/TERM
status (129/130/143) with cleanup failure status 1. In the installer, failure of
the initial disposable-directory removal could abort cleanup before retained
backup guidance. Review reproduced render and installer failures and probed
prepare/publish cleanup functions. The backup survived byte-for-byte; no data
loss was demonstrated. The earlier self-review's zero-finding claim did not
survive independent review and is retained above only as historical evidence.

After `git fetch origin --prune`, branch was exactly
`fix/signal-interruption-recovery`, HEAD was
`e8bba7730fa655db1e2fd59fa80828d0ba747bea`, and both HEAD^ and origin/main were
`acc3b28912a3d4a99340e91fe2981c70faaa8fbf`. Worktree/index were clean, stashes
zero, and open PRs zero. Live protection still required strict `ci`, `build`,
`minimal-runtime`; all three were successful on that exact base SHA (the same
runs 37157785747 and 37157785721). These remain base evidence, not correction CI.

### Complete EXIT-path inspection and corrected model

All four affected scripts were read completely, including their nested traps
and normal-path removals. The correction stays within these shell scripts:

| Site | Cleanup order / ownership | Correction |
| --- | --- | --- |
| Installer outer EXIT | Owned download directory; privileged staged binary; either report required backup or remove obsolete backup; descriptor-pinned private config file; private config directory | Explicit `on_exit`; warn separately on each failed disposal; continue later independent actions and retained-backup reporting |
| Installer configuration writer EXIT | Only `./apps.toml` inside the validated private stage | Explicit `config_on_exit`; warn on failed disposal; preserve writer status (signal-only writer still exits 2); parent can finish disposal |
| Prepare EXIT | Owned release stage | Explicit `on_exit`; report failed stage removal |
| Publish EXIT | Owned publication stage; kernel releases descriptor-9 flock on exit | Explicit `on_exit`; report failed stage removal; lock lifetime unchanged |
| Render command-substitution EXIT | Isolated verification GPG home | Explicit `gpg_on_exit`; report failed home removal; preserve phase status before existing caller failure mapping |
| Render outer EXIT | Temporary output file | Explicit `on_exit`; report failed removal; previous output/publication boundary unchanged |

Each EXIT entry captures `$?` as its first command, unregisters its own EXIT
trap, disables errexit for cleanup (`set +e`), attempts cleanup with explicit
operation-specific failure diagnostics, and calls `exit "$primary_status"`.
Thus neither an unsuccessful removal nor an unsuccessful diagnostic replaces
the selected primary outcome. There is no implicit trap-return-status dependency,
recursive EXIT execution, automatic rollback, or new shared shell framework.
Warnings are fixed, bounded lines to stderr without added artifact contents or
paths. The existing retained-backup path remains deliberately explicit for recovery.

Only installer outer cleanup has multiple independent shell disposals. None of
its failure branches returns early; the backup-required state still prevents
removal of recovery evidence. Single-action prepare/publish/render cleanup
functions return failure after warning; their EXIT wrappers still preserve the
incoming status. All HUP/INT/TERM registrations and successful trap resets remain
at their existing workflow boundaries. Prepare and render retire their EXIT
traps after successful final renames. The installer writer still transfers cleanup
to the parent on success; it does not publish configuration itself.

Ordinary outcomes were considered explicitly. EXIT cleanup preserves primary
success 0 and ordinary failure unchanged, with diagnostics for cleanup failure.
Prepare/render successful rename behavior is unchanged. Publish uniquely calls
cleanup manually before its `Published` summary: that removal remains a required
normal workflow step, with failure exit 1 and no `Published`. On this manual
failure, the EXIT trap is disabled before exiting, preventing a duplicate
cleanup attempt. Immutable object verification and latest-last ordering are
unchanged. No production Go or other production files changed.

### New deterministic regression coverage

`internal/installer/cleanup_failure_test.go` extends the existing fixture helpers,
checked temporary-script hooks, and actual-kernel-signal supervisor. A narrowly
matched shell `rm` fixture returns 73 for the selected disposal and logs an
injection marker; all other removals use the real utility. This works independently
of UID and filesystem permissions. Each case asserts exactly one injected
failure and one production warning, as well as the expected status and absence
of later normal work/success output. Production has no injection mechanism.

Every row below runs HUP, INT, and TERM separately:

| New cleanup-failure boundary | Delivery / assertions |
| --- | --- |
| Installer replacement complete, before verification | Parent: 129/130/143; exact old backup bytes (including NUL/newline), retained path reported, new target remains, failed download disposal remains, later staged binary disposal succeeds, no config work |
| Installer configuration writer in flight | Parent: 129/130/143; download cleanup fails, later pinned config file/directory cleanup succeeds, unrelated config preserved, no publication |
| Installer writer before cleanup ownership transfer | Phase: original exit 2 survives failed private-file removal; parent removes remaining private file/directory; unrelated config preserved |
| Render output complete, before rename | Parent: 129/130/143; failed temporary-output removal leaves the private file, previous output stays exact, no publication |
| Render GPG home | Parent, group, and phase: failed home disposal is diagnosed; previous output and unrelated artifact stay exact; no output staging/publication; directly signalled phase retains 129/130/143 before caller maps failure to 1 |
| Prepare stage exists, before build | Parent: 129/130/143; failed stage disposal diagnosed; unrelated file preserved; no build/signing/final release directory/Prepared |
| Publish first immutable object verified | Parent: 129/130/143; failed local-stage disposal diagnosed; prior artifact and first uploaded object retained; no later upload/Published; kernel lock released |

That adds **27 cleanup-failure signal cases: HUP 9, INT 9, TERM 9** to the
original 102. Three corrected focused-suite runs passed **129 cases each:
HUP 43, INT 43, TERM 43**. Existing workflow startup regressions (36 cases across
12 inline blocks) and CI-script startup regressions remain in that suite.
The same acknowledged pipe gate sends signals before releasing the foreground
helper; waits remain bounded at 10 seconds, with 5-second emergency reap and
exact fixture-group teardown. No root, real sudo, production signing, remote
publication, or VM is used. Failed disposals intentionally leave fixture-owned
paths for test teardown; they do not imply successful filesystem cleanup.

Eight additional ordinary EXIT probes execute the exact production cleanup and
wrapper functions with primary 0 and 7, forcing failure and checking unchanged
status. A full fake-publication regression proves manual cleanup failure exits 1,
warns once, does not retry EXIT cleanup or print Published, and leaves the five
completed fake uploads with latest last. New fixture setup/unique-hook mistakes
were corrected during development before the final validation below.

### Correction validation and limitations

Exact toolchain setup used in fish for every Go validation invocation:

```fish
set -gx PATH ~/.local/opt/go1.26.7/bin $PATH
set -gx GOENV off
set -gx GOTOOLCHAIN local
```

`go version`: **go version go1.26.7 linux/amd64**.
`go env GOENV GOTOOLCHAIN`: empty disabled GOENV filename, then `local`.
No compiler/dependency upgrade. All following final validation passed:

| Command | Result |
| --- | --- |
| `go test -v -count=1 -timeout=3m ./internal/installer -run 'Signals$'` | PASS three times; 129 cases per run, 43 per signal; includes 27 cleanup-failure cases, 9 per signal |
| `go mod verify` | PASS, all modules verified |
| `go test -count=1 -timeout=5m ./internal/installer ./internal/app` | PASS |
| `go test -race -count=1 -timeout=5m ./internal/installer` | PASS |
| `go test -count=1 -timeout=10m ./...` | PASS |
| `go vet ./...` | PASS |
| `go build ./...` | PASS |
| `sh -n script/install.sh` | PASS |
| `sh -n script/prepare-release.sh` | PASS |
| `sh -n script/publish-release.sh` | PASS |
| `sh -n script/render-install.sh` | PASS |
| `sh -n script/test-ci-arch.sh` | PASS |
| `sh -n script/test-minimal-arch.sh` | PASS |
| `bash -n script/test-ci-arch.sh` | PASS |
| `bash -n` separately on each extracted workflow `run` block | PASS, all 12 blocks across arch.yml, ci.yml, release.yml |
| `git diff --check` | PASS |

Self-review of the complete correction diff found **Critical 0 / Important 0 /
Optional 0** remaining within this correction scope, pending independent
re-review. Checked saved statuses, explicit exits, trap resets, manual invocation,
duplicate cleanup, warning bounds, continued installer reporting, ownership and
safe-path checks, nested phase contracts, and publication order. This is a local
self-review conclusion, not independent approval or I-04 closure.

Limits remain: failed disposals can leave private artifacts and need operator
attention; warnings do not guarantee successful cleanup. In-flight utilities may
finish before a pending shell trap runs. Tests do not establish bounded teardown
of arbitrary utilities, repeated-signal behavior during cleanup, SIGKILL/hard-crash
convergence, or new process-ownership guarantees. The preserved acceptance VM
was not booted; production artifacts were not signed/published. Protected CI and
full native acceptance were not run for this local correction. Wave G remains
local/unmerged, **I-04 OPEN**; independent re-review, final verification, protected
CI, merge/tree equality, and formal closure remain required. No later wave started.
