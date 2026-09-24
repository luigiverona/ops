# Wave E propagation and retained-fixture corrections

Date: 2026-09-24. Branch: `fix/process-tree-cancellation`.
Starting HEAD: `7203bba125a5ad45b05730b3e3b91c560446aace`.
`origin/main`: `50adbe8518aee4108ee7bd7b6532e6a9ad9daf7f`, unchanged after fetch.
Preflight matched: clean worktree/index, no stash. The supplied independent review
reported Critical 0, Important 4, Optional 1. Earlier evidence and result data
remain historical; this file records the implementation correction and validation.

## Expected-state audit and corrections

Searched production code for `run.Exited`, `ExitError`, `ExitCode`, `errors.As`,
`errors.Is`, expected exits/statuses 1–4, and success/false-nil branches after
command failure. Inspected the command-result and error-classification helpers.

| Inventory | Disposition |
| --- | --- |
| resolve | `pacman -T` actually uses **127**, with exact missing requirement and silent stderr; `-Qq` uses silent exit 1 for empty inventory. Both formerly accepted an exit anywhere in a compound error. Both now require `run.OnlyExit`. `Pacman`'s supplemental API fallback also requires an exit-only failure. |
| inspect | Existing service is-enabled 1/4, is-active 3/4, and foreign inventory `-Qqm` 1 use the shared classifier. Output restrictions remain. Removed the duplicate local classifier. |
| pgp | `missingKey` formerly accepted any error containing a run.Error with the missing-public-key diagnostic, including joins and unrelated causes. It now requires only exit 2 plus the diagnostic. |
| ssh | Agent-unreachable exit 2 and empty-agent exit 1 use the shared classifier. Discovery/revalidation's prior typed lifetime propagation is unchanged. |
| git | Silent unset exit 1 uses the shared classifier plus all existing diagnostic checks. Concurrent context cancellation now joins, rather than replaces, the command failure. |
| github | Configured's missing-user exit 1, scope-insufficiency classification, and SSH success-text/exit-1 acceptance cannot hide compound failures. Authenticated now returns `(bool, error)` and preserves execution failures through Login, RefreshSSHKeyScope, and application orchestration. Ordinary exit-1 unauthenticated state remains supported. |
| archrepo / archtrust | Query fallback requires exit-only status 1. Historical-version failure previously formatted its underlying error with `%v`, then errors.Is on the unavailable sentinel could erase it. Preserve both causes with `%w`; only the exact unavailable sentinel establishes missing evidence without another failure. |
| arch / aur / flatpak / release | No additional exit-status acceptance found. Commands propagate failure before successful-state parsing. Remaining filesystem/JSON EOF checks are not external-command exit classifiers. |

No production call site still uses `run.Exited` to discard an error. That API is
retained for finding an exit diagnostically; its comment directs state acceptance
to `OnlyExit`. `OnlyExit` requires one exit leaf through ordinary wrappers, rejects
all multi-cause unwraps (even duplicate expected exits or a singleton Join), and
rejects typed ownership and context failures even when they wrap the exit.
Callers retain the original error with `%w`; they never strip a matching leaf.

Direct tests include an actual `exec.ExitError`, wrapped exit, ownership, reverse
join, nested joins/wrapping, cancellation, deadline, arbitrary failure, duplicate
expected exits, nil, and a nonmatching status. Resolver tests invoke public
OfficialDependency separately for -T and -Qq, preserving missing-dependency
semantics and requiring no later command on every injected failure. Pacman's
fallback is tested against a successful fake HTTP response so the API cannot
conceal a joined cause.

Public Manager.Has tests cover classic listing, effective configuration,
keyboxd export, import, and final listing; Manager.Import tests cover existing
key lookup and downloaded-key inspection before mutation. Expected absent keys
remain false/nil only in the valid classic missing-key path. Every injected
ownership, cancellation, deadline, or arbitrary cause survives both join orders
and nesting. Keyboxd paths that previously rejected a nonzero exit still reject
it. There is no pgp.Configured method; github.Configured is covered separately.

GitHub public-method matrices cover Configured, Authenticated, VerifySSH,
Login, Refresh, and the scope classifier. Git, authenticated repository queries,
and unavailable evidence have additional propagation regressions. The new
application GitHub test returns Fatal and runs no subsequent command. Existing
service and SSH before/after-mutation fatal-stop tests, owner poisoning/admission,
approval ordering, and cgroup.events fail-closed tests remain passing.

## Systematic fixture-safety audit

Audited retained Python, embedded native workloads, cleanup owners, temporary
homes/workspaces, service units, and test helpers under internal/run, release,
pgp, sudo, docs/wave-e-evidence, script, and internal/*/testdata. Explicitly checked
pre-fork-only alarms, ignored signals, infinite loops/writers, killed-helper
TempDir/defer ownership, transient units, and owner-only cgroup cleanup.

- `architecture/workload.py` had the reported pre-fork `alarm(12)` defect. It now
  installs SIGALRM's default action and rearms ITIMER_REAL **after every fork**,
  including serial stress children and grandchildren. Default 12 seconds is
  unchanged; a validated test-only override permits shorter proofs. Record writes
  are atomic. TERM ignoring, setsid, retained pipes, and original modes remain.
  `TestArchitectureFixtureIndependentExpiration` proves tree, stress, stdout and
  stderr holders independently expire from SIGALRM after natural/direct-parent
  death, with stopped heartbeats, all recorded PIDs reaped, and exact workspace
  deletion. No descendant cleanup signal is used during the expiration proof.
- `reproduce.py` remains runnable. Each separately exec'd helper now installs a
  hard 15-second alarm as well as its existing cooperative bound. Its subreaper,
  pidfds/exact identity checks, and bounded final cleanup remain.
- Original architecture `delegation.py`, `harness.py`, and `supplement.py` are
  **execution-guarded archival source** (including when copied). Their historical
  harnesses lacked independent failure supervision; the permission probe also
  used a fixed cgroup name. Their former instructions are explicitly superseded.
  Guard tests prove execution stops before work. Original JSON/text results,
  launch.go.txt, probe.go.txt and the historical privileged proof patch are not
  changed or executed; the .txt/.patch source is archival, not a runnable suite.
- Maintained run/release `testdata/ownership.py` already rearms all descendants;
  their expiration and parent-death tests pass. Existing PTY/release supervisors
  own the killed helper's exact scope and workspace and remain passing.
- Native run, makepkg, pgp, and privileged-sudo fixture owners now run beneath
  `internal/testproc/native.py`, a surviving subreaper with a 110-second owner
  deadline. It owns TMPDIR, cleans only the recorded/validated scope, reaps only
  its own waitable children, and requires scope/workspace disappearance. It reaps
  adopted helpers while the owner runs, preventing zombie keyboxd lock holders.
  Output is a supervisor-owned file, so inherited pipe writers cannot prevent
  failure cleanup. Native test skips fail the supervisor.
- New `TestNativeGPGOwnerDeath` proves a real keyboxd is live in the recorded scope,
  kills only the owner, observes remaining population, then requires independent
  exact-scope/PID/home cleanup. Native makepkg's real helpers receive the same
  supervisor protection. Inline makepkg/Bubblewrap fork helpers already rearm
  timers and have finite work; sudo's explicit sleep is ten seconds.
- The native manager-owned service now also has RuntimeMaxSec=20, independent
  of Go defer cleanup, in addition to its finite sleep and explicit stop.
- Disposable release signers now register bounded gpgconf cleanup for their own
  newly created home before key generation. Existing pgp helper cleanup is exact
  and bounded. No pre-existing user helper is selected or signalled.
- Script shell files contain no additional indefinite Wave E workload. CI script
  only gains the new unprivileged GPG owner-death test in its native selection.

The runnable maintained fixtures are the two bounded ownership workloads, their
run/release safety/PTY/cleanup supervisors, architecture_safety.py, native.py,
retained architecture/workload.py, and retained reproduce.py. Archived architecture
harnesses must not be enabled to reproduce; use the maintained native suite.

## Prompt/Flatpak Optional correction

The terminal helper deliberately calls os.Exit, bypassing both t.TempDir cleanup
and TestMain's IsolateFlatpakTests defer. The surviving Go parent now creates one
exact workspace per prompt case and supplies it as TMPDIR to Python and the Go
helper. It verifies both leaked allocations were actually created, removes that
exact workspace, and asserts absence. Parent t.TempDir is the failure fallback.
No global sweep, wildcard removal, stale-PID deletion, or flatpak product change.

A dedicated three-run 11-case suite passed: **33 actual os.Exit cases, 66 observed
prompt/Flatpak allocations, all removed**. Full ordinary and application race
runs repeat the same checks. No session-created prompt/Flatpak directory remains.

## Validation

All Go commands used `/home/ah/.local/opt/go1.26.7/bin` first in PATH,
`GOENV=off`, `GOTOOLCHAIN=local`. Version: `go1.26.7 linux/amd64`; GOENV filename
empty and GOTOOLCHAIN local. Disposable test key generation/signing/verification
was explicitly authorized and **unfiltered**, including release and official
package trust fixtures. No release signing home, production key, actual release
artifact, publication, or real release-signing operation was used.

| Package | Ordinary | Focused race |
| --- | --- | --- |
| run | PASS 46.867s (final full suite) | PASS 580.901s, timeout=30m |
| release | PASS 1.591s | PASS 2.639s |
| resolve | PASS 3.153s | PASS 4.630s |
| inspect | PASS 0.033s | PASS 1.085s |
| pgp | PASS 0.397s | PASS 10.478s |
| app | PASS 3.184s | PASS 6.966s |
| ssh | PASS 1.141s | PASS 2.239s |
| aur | PASS 0.013s | PASS 1.039s |
| git / github / archrepo / archtrust / sudo | PASS | PASS |

Final full unfiltered `go test -count=1 ./...`: PASS, every package.
`go mod verify`, `go vet ./...`, `go build ./...`, tagged native-package vet,
Fish `set formatted (gofmt -l .); test (count $formatted) -eq 0`, and
`git diff --check`: PASS. All 13 retained Python files parsed with ast.parse,
without bytecode output; archival guard execution checks passed. Established
checks passed: `sh -n script/install.sh script/prepare-release.sh
script/render-install.sh script/publish-release.sh script/test-minimal-arch.sh`
and `bash -n script/test-ci-arch.sh`.

Focused ordinary packages used `-count=1 -timeout=5m`; focused race used
`-race -count=1 -timeout=10m`, covering release, resolve, inspect, pgp, app, ssh,
aur, git, github, archrepo, archtrust, sudo, testpkg, testproc. Run's full race
command is `go test -race -count=1 -timeout=30m ./internal/run`. Subsequent focused
ordinary/race checks cover the final Git, actual ExitError, application GitHub,
and architecture expiration assertions. Prompt leak proof used
`go test -v -count=3 -timeout=2m ./internal/app -run '^TestPromptTerminalCancellationAndEOF$'`.

Complete unprivileged native ordinary/race both passed: run **41.466s / 47.532s**,
pgp **0.915s / 2.865s**, release **2.363s / 4.327s**. Exact selection:

```sh
go test -count=1 -timeout=5m -tags ownership_integration \
  ./internal/run ./internal/pgp ./internal/release \
  -run '^(TestNativeOwnership|TestNativeCrashScopeLifetime|TestNativeGPGHelpers|TestNativeGPGOwnerDeath|TestNativeUpdaterOwnership|TestOwnershipPTY|TestOwnershipPTYFailureCleanup|TestNativeMakepkgHelpers|TestNativeCleanupCancellation)$'
```

The race invocation adds `-race`. This includes normal/setsid descendants,
stdout/stderr holders, natural exit, cancel/deadline, frozen atomic placement,
nested cgroups, Bubblewrap, manager service, PTY, crash/parent death, release owner
death, makepkg/keyboxd, and GPG helpers. Retained reproduction expiration runs in
ordinary and focused race tests. No sudo/native privileged workload was run.
Privileged tagged code was vetted and compiled with `-run '^$'` only.
The complete repository race suite was not run locally.

## CI, security review, and leak audit

Read ci.yml and test-ci-arch.sh: privileged ordinary and race ownership/keeper/PTY
coverage precedes revocation; sudo grant removal is followed by required failure
of sudo -n true; full unfiltered ordinary and repository race suites follow.
Protected names `ci`, `build`, `minimal-runtime` and main/PR triggers are unchanged;
no proof-branch trigger. Native supervisors reject silent skips. CI was reviewed,
not invoked, and no local sudo, installation, or preserved VM action occurred.

Correction-delta implementer review: **Critical 0, Important 0, Optional 0 remaining**. This is not
an independent re-review. No ownership architecture, admission/poisoning,
approval/sudo ordering, Bubblewrap, terminal/job control, service-manager lifetime
boundary, malicious-root/same-UID threat boundary, dependency, or Go version was
changed. Existing production crash/uninterruptible-task limits and race-test cost
remain as previously documented.

Final read-only baseline comparisons use exact resource records and /proc
observations, never scans to authorize deletion. Native supervisors and prompt
regressions additionally assert their own exact cleanup. No session-created GPG
helper, heartbeat writer, command cgroup, scope, GPG home, record/workspace, or
prompt/Flatpak directory remains after completed suites. No pre-existing helper
was modified. The validation directory `/tmp/ops-wave-e-propagation-audit` contains
small local logs/snapshots; an unrelated desktop-generated temporary PNG is left
untouched. After the 580.901-second run race completed, the final snapshot found
zero new helpers, fixture processes, heartbeat FDs, or cgroups, no loaded ops
scope, and no session-created temporary directory. The only new top-level /tmp
entry was that unrelated desktop PNG; all Go build directories also disappeared.

An additional supervised invocation of retained reproduce.py's stdout, stderr,
and setsid helper modes killed their parents and sent no descendant cleanup
signal during the measured interval. All three independently died from their
15-second SIGALRM and were reaped; the exact supervisor workspace was removed.
The small command script and its PASS log are in the local validation directory.
No archived Go probe or old architecture harness was executed.

No push, PR, merge, tag/release, publication, R2, preserved-VM mutation, production
signing, toolchain/dependency change, or Wave F/later work occurred. One focused
local commit is allowed only after all validation completes; prior commits remain
unchanged. Commit and final clean status are reported with delivery.
