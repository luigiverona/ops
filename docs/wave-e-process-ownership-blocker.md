# Wave E: process ownership blocker

Status: blocked, with no production implementation. On 2026-09-18 the user
explicitly selected **“Preserve constraints; document blocker”** after the
ownership limitations below were identified. This record and its disposable
reproducer preserve the evidence; they do not claim I-02 is fixed.

## Baseline and scope

- Repository: `luigiverona/ops`, `/home/ah/ops`.
- Preflight branch: `main`; index/worktree clean; no stashes.
- `HEAD` and `origin/main`, both before and after `git fetch origin`:
  `50adbe8518aee4108ee7bd7b6532e6a9ad9daf7f`.
- Created new local branch: `fix/process-tree-cancellation`.
- No implementation commit. HEAD remains the baseline.
- Required future protected checks: `ci`, `build`, `minimal-runtime`.
- Go: `go version go1.26.7 linux/amd64`, with `GOENV=off` and
  `GOTOOLCHAIN=local`. `go env GOENV GOTOOLCHAIN` prints an empty GOENV path
  followed by `local` when the environment file is disabled.

Production Go process-launch searches found only `internal/run/run.go`.
Other `exec.Command*` uses are test code. Runner wrappers in `app` and
`archrepo` delegate to this boundary; they do not establish process ownership.
The five requested `internal/run` files were read completely. Production
command specifications and input/output modes were inspected, including
package transactions, AUR builds, Flatpak, Git/GitHub, SSH, GPG, sudo, and
release replacement/cleanup.

`cmd/ops/main.go` creates a context for SIGINT and SIGTERM. The sudo keeper
uses a child context. Some established recovery/removal commands deliberately
use `Background` or `WithoutCancel`; this wave did not change those semantics.
`app.cancellationRunner` substitutes `ctx.Err()` after a delegated call, but
cannot unblock a stuck call or terminate descendants.

Actual privileged paths include:

- `sudo -v` for authentication and `sudo -n -v` for refresh;
- interactive `sudo -n pacman -Syu`;
- streamed package installation, including
  `sudo -n unshare --mount --propagation private -- /bin/sh -c ... pacman ...`;
- protected file staging/replacement and `sudo -n systemctl enable --now`.

The official transaction shell uses `exec` after setting up the private mount.
Its mount namespace is not a PID namespace. No shell-trap changes were made.
Service work requested through an existing system manager and external agents
also needs an explicit ownership definition; it is not automatically a forked
descendant of the client process. This record does not expand Wave E to service
rollback or externally managed agents.

## Concrete pre-fix failure model

`Exec.Run` creates `exec.CommandContext`, leaves its default cancellation
function and zero `WaitDelay`, then calls `cmd.Run()`. The default cancel
function kills only the direct process. Output goes to tail buffers and often
`MultiWriter`/diagnostic writers, causing `os/exec` to create copy goroutines
and pipes. Descendants inherit their write ends. Direct-child exit does not
establish EOF, descendant termination, or command-lifetime ownership.

The [Go probe](wave-e-evidence/probe.go.txt) invokes the unchanged production
boundary. The [Linux harness](wave-e-evidence/reproduce.py) synchronizes on
readiness files and `/proc` process state, then cancels via the probe's stdin.
Every leaf installs SIGTERM ignore before announcing readiness. The harness
asks surviving leaves to acknowledge a new request by writing a temporary
file, proving they can perform work after cancellation. For cases where Run
returns before cleanup, that request is made **after Run has returned**.

Observed results, repeated with the retained harness:

| Case | Observed failure |
| --- | --- |
| Captured child/grandchild | Direct child died; grandchild stayed live after Run returned and wrote an acknowledgment |
| Three grandchildren | All three stayed live and wrote acknowledgments after Run returned |
| Stdout holder | Direct child died, but Run remained blocked until the harness killed the grandchild |
| Stderr holder | Same retained-pipe behavior for stderr |
| Natural direct-child exit | Child exited before cancellation; inherited stdout kept Run blocked; after external cleanup Run returned **nil** despite cancellation while waiting |
| Stubborn grandchildren | Every leaf ignored SIGTERM, remained live, and wrote an acknowledgment; harness SIGKILL was required |
| Streamed output boundary | Same descendant survival after Run returned |
| Candidate dedicated group | A leaf called `setsid`; negative-PGID SIGKILL plus 250 ms WaitDelay still let it survive and acknowledge work after return |

The candidate group case uses an isolated `exec.Cmd` experiment, not a
production change. It disproves treating that proposed mechanism as full tree
ownership. It does not imply every process-group-based improvement is useless.

[Recorded JSON results](wave-e-evidence/results.json) preserve durations and
error identity. In killed baseline cases, a structured `run.Error` was retained,
but `errors.Is(err, context.Canceled)` was false. The natural-exit case returned
success after the harness removed the pipe holder. Its controlled ordering
establishes the pipe-wait cancellation failure; a randomized cancellation/exit
race regression was not implemented.

The 300 ms negative observation is made after readiness and direct-child death
have been established. It is not the only synchronization. Missing `WaitDelay`
and Go's wait-for-EOF implementation explain why the wait has no production
bound; the proof then removes the holder instead of hanging indefinitely.

The harness is a disposable subreaper, holds pidfds for its own helpers, kills
and reaps them, and asserts `/proc` disappearance. Every helper has a 15-second
maximum lifetime. Test mutations are confined to unique temporary directories,
which are removed. No sudo command, package operation, unrelated signal,
workstation configuration change, or preserved VM operation is performed.

## Why the proposed small design is insufficient

**Process groups are not descendant containers.** A child can create another
group/session. A descendant's fork inherits its current group, which may already
be outside the original group. The `setsid` proof demonstrates this without
privileges. Enumerating `/proc` does not atomically own future forks or avoid
reparenting races. Subreaping can adopt orphans but does not grant permission to
signal them. Individual pidfds prevent signaling a reused PID but do not create
a recursive ownership boundary. See [setsid(2)](https://man7.org/linux/man-pages/man2/setsid.2.html).

**Normal-user signaling is insufficient for privileged descendants.** Linux
checks signal permissions against target credentials. A group-kill success means
at least one process was signaled, not that every member was killed. A root
command can have real and saved UID 0 even though sudo itself remains signalable
by its invoking user. See [kill(2)](https://man7.org/linux/man-pages/man2/kill.2.html).

Sudo's local 1.9.17p2 manual documents its PTY monitor/session and signal relay:
SIGKILL cannot be caught or relayed. Sending SIGTERM to sudo is therefore not
proof that an uncooperative root descendant is gone; later killing sudo cannot
supply that proof. Upstream's [non-PTY signal handling](https://github.com/sudo-project/sudo/blob/main/src/exec_nopty.c)
also distinguishes normal signal forwarding from timeout-triggered termination.
No live privileged mutation or root-process cleanup test was attempted. The
privileged path is **unproven**, not reported as passing.

**There is no established recursive kernel ownership mechanism in this runtime.**
The repository does not establish cgroup delegation or a privileged supervisor.
The inspected local session cgroup and its `cgroup.kill` are root-owned; they
were not modified. The minimal-runtime gate uses a container without a user
systemd service requirement. The Arch CI script explicitly removes the test
user's sudo grant before tests. Requiring a new user service/cgroup arrangement
or elevated supervisor would need a supported runtime design and validation,
not an assumption made inside `Exec.Run`. Kernel cgroup documentation describes
recursive kill and delegation: [cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html).

An unprivileged PID namespace created through a new user namespace cannot
preserve host-root sudo semantics. A privileged PID-namespace supervisor would
be a different ownership architecture, which the user elected not to introduce
in this session. No claim is made that a robust design is impossible with a
revised, explicit runtime contract.

**WaitDelay solves a narrower pipe problem.** Go 1.26.7 source was inspected at
`src/os/exec/exec.go`, including `watchCtx`, `Wait`, and `awaitGoroutines`.
It can close Go-owned pipes whose descendants withhold EOF, but does not kill
those descendants. It also waits for copy goroutines after closing pipes; an
arbitrary caller-supplied reader/writer blocked in its own Read/Write is not
made cancellable by closing the opposite pipe. The existing production output
writers can be files/terminals, while input includes files and memory readers.
A complete bounded-I/O contract must address that distinction without silently
leaking copy goroutines or truncating ordinary successful output.

A grace period followed by group SIGKILL and WaitDelay would improve ordinary
same-UID, same-group cases. It cannot satisfy the requested no-surviving-owned-
descendant guarantee for all the inspected modes. No production grace/kill/wait
constants were selected, and no final process-ownership design was installed.
Numeric PGID reuse, fork-during-cancellation, terminal restoration, privileged
cleanup, and cancellation/exit arbitration consequently remain design work.

## Interactive and read-only boundaries

Real terminal input is required. `app.prepare` and `app.update` open `/dev/tty`
and pass it as `Exec.In`. Interactive callers include sudo password prompts,
pacman decisions, SSH passphrases, and GitHub device authentication. Merely
setting `Setpgid` can put a reader in a background terminal group. A valid design
would need deliberate foreground transfer, restoration, and signal handling,
with PTY tests. No new TTY behavior was implemented or tested here; existing
`Interaction` enforcement and terminal plumbing remain unchanged.

Bubblewrap retains the exact existing `--unshare-all --die-with-parent
--new-session --ro-bind / / --proc /proc` boundary and disabled D-Bus addresses.
It has its own session/PID-namespace behavior, so an outer process-group model
must analyze that explicitly. Existing read-only/no-fallback tests passed in the
baseline run suite. A new bwrap cancellation test was not implemented. No
fallback, namespace relaxation, output presentation change, or privacy change
was introduced.

## Reproduce locally

From the repository root, with Fish and Linux/Python 3 available:

```fish
set -gx PATH ~/.local/opt/go1.26.7/bin $PATH
set -gx GOENV off
set -gx GOTOOLCHAIN local
go version
go env GOENV GOTOOLCHAIN
set proof_dir (mktemp -d "$PWD/.wave-e-proof.XXXXXXXX")
set proof_bin (mktemp -d /tmp/ops-wave-e-bin.XXXXXXXX)
cp docs/wave-e-evidence/probe.go.txt "$proof_dir/main.go"
go build -o "$proof_bin/probe" "$proof_dir/main.go"
# Continue only if the build succeeded.
env OPS_WAVE_E_PROBE="$proof_bin/probe" python3 docs/wave-e-evidence/reproduce.py
rm "$proof_dir/main.go"
rmdir "$proof_dir"
rm "$proof_bin/probe"
rmdir "$proof_bin"
```

The probe is stored as `.go.txt` so ordinary `go test ./...` does not discover
an extra production package. The script prints observations to stdout; it writes
JSON only if `OPS_WAVE_E_RESULTS` is explicitly set to a destination. Its
assertions expect the known baseline defects, so a successful proof run is
**not** a cancellation acceptance-test pass.

## Validation and review disposition

- `go test -count=1 ./internal/run`: PASS on unchanged baseline, 33.647 s.
- Disposable proof: all seven cases observed; all helpers killed and reaped.
- `go mod verify`: PASS, all modules verified.
- Fish-safe `gofmt -l .` check: PASS. Probe source separately formatted.
- `git diff --check`: PASS; new evidence files also reviewed for whitespace.
- Focused race, affected-package suites, full ordinary suite, vet, and build:
  not run as implementation validation after the user selected documentation
  of the blocker. No production code changed. The small proof binary did build
  with the exact requested toolchain.
- Protected checks: not triggered; branch remains local.

**Critical, unresolved:** I-02 descendant lifetime/continued-mutation defect;
no demonstrated privileged ownership boundary satisfying the strict contract.

**Important, unresolved:** inherited-pipe cancellation bound and context-error
identity in the existing boundary; final ownership design and its TTY/sudo/
bwrap/race/leak regression matrix. These are existing or unimplemented
requirements, not regressions introduced by a production patch.

**Optional follow-up:** retain this evidence when a future session chooses a
supported ownership architecture. No specific I-04 shell-trap defect was
newly established. Wave F, Wave G, and terminal-output redesign remain untouched.

No push, PR, merge, tag, release, artifact publication, R2 operation, preserved
libvirt VM mutation, release signing, Go/dependency upgrade, or later-wave work
was performed. Documentation/evidence are left uncommitted on the local branch;
there is no implementation ready for independent review.
