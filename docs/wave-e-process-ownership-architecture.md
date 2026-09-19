# Wave E / I-02: delegated cgroup ownership architecture proof

Date: 2026-09-18. **Decision: APPROVE for a separate Wave E implementation review.**
Production implementation remains unauthorized and has not been started.

The missing privileged-descendant gate is now proven in the existing protected
Arch KVM CI environment. [Actions run 35393152228](https://github.com/luigiverona/ops/actions/runs/35393152228)
passed all 13 cases: normal sudo/PAM transition, UID-0 descendants, root setsid,
ten bounded root-fork trials, UID-1000 cgroup.kill, population-zero/reaping,
retained stdout/stderr EOF, and ordinary-user cgroup removal. Bootstrap sudo was
then revoked, the complete normal and race suites passed, and the VM was deleted.

The prior workstation evidence below remains intact: ordinary descendants,
atomic placement, setsid, fork races, terminal interaction, and retained pipes
passed locally, while local sudo required a password. That local limitation was
not bypassed. The temporary CI proof closed it without adding a privileged
ownership supervisor or changing the stated lifecycle threat model. See the
[privileged CI report](wave-e-evidence/architecture/privileged-ci-proof.md),
[structured evidence](wave-e-evidence/architecture/privileged-ci-proof.json), and
[preservation audit](wave-e-evidence/architecture/privileged-ci-preservation.json).

## Baseline and preservation

- Repository: `luigiverona/ops`, `/home/ah/ops`.
- Branch: `fix/process-tree-cancellation`.
- HEAD: `50adbe8518aee4108ee7bd7b6532e6a9ad9daf7f`.
- `origin/main` was that same SHA before and after `git fetch origin`.
- Preflight: only the expected untracked blocker and evidence directory; no stashes.
- No tracked file or index entry changed in the original Wave E worktree.
  Temporary CI/proof files were committed only in the separate disposable branch;
  no production file, dependency, or toolchain definition changed.
- The four original evidence files were neither edited, deleted, nor committed.
  Their preflight and final SHA-256 values match; see
  [platform.json](wave-e-evidence/architecture/platform.json).
- The existing [blocker](wave-e-process-ownership-blocker.md) remains authoritative
  evidence of the unchanged production implementation's failures.

## Candidate and observed platform

Candidate: transient systemd user scope with `Delegate=yes` -> ops-owned subtree
-> fresh domain cgroup for each command -> Go `UseCgroupFD`/`CgroupFD` -> atomic
`clone3(CLONE_INTO_CGROUP)` -> bounded grace -> ordinary-user `cgroup.kill` ->
observe `cgroup.events` empty -> reap/drain -> remove child cgroups -> return.

| Observation | Local result |
| --- | --- |
| OS/identity | Official Arch, x86_64; ordinary UID/GID 1000, supplementary wheel |
| `uname -a` | `Linux arch 7.2.6-arch2-1 #1 SMP PREEMPT_DYNAMIC Mon, 14 Sep 2026 22:41:30 +0000 x86_64 GNU/Linux` |
| systemctl, systemd-run, loginctl | systemd 261, package `261.3-1-arch` |
| cgroup filesystem | `/sys/fs/cgroup`: `cgroup2fs`, rw, `nsdelegate` |
| Additional hierarchy | Separate legacy `net_cls` mount also present; host is not exclusively v2 |
| Systemd ownership hierarchy | Unified v2 `0::` hierarchy, used by every ownership probe |
| Initial invocation | `0::/user.slice/user-1000.slice/session-3.scope` |
| User manager | Reachable and running, zero failed units; Version `261.3-1-arch` |
| Manager cgroup | `/user.slice/user-1000.slice/user@1000.service` |
| Exact probe Go | `/home/ah/.local/opt/go1.26.7/bin/go`, `go version go1.26.7 linux/amd64` |
| Go environment | `GOENV=off`, `GOTOOLCHAIN=local`; Go reports empty GOENV file and `local` |
| Default PATH Go | 1.27.1-X:nodwarf5; deliberately not used to build probes |
| Installed sudo / bwrap | `1.9.17.p2-6` / `0.12.0-1` |
| Noninteractive sudo | Unavailable; exit 1, password required |

The extra net_cls mount does not invalidate the verified v2 execution boundary;
code must select the `0::` record, not assume `/proc/self/cgroup` has one line.
No services were enabled or made persistent. The normal user manager was already
available; these results establish availability in this session, not every SSH,
TTY, container, or graphical login configuration.

## Delegation and permissions

Inspected local `systemd-run --help`, `systemd-run(1)`,
`systemd.resource-control(5)`, `systemd.scope(5)`, `pam_systemd(8)`, and
`org.freedesktop.systemd1(5)` before interpreting the interfaces. Commands used
unique `ops-wave-e-architecture-*` unit names and:

```text
systemd-run --user --scope --quiet --collect --unit=UNIQUE \
  --property=Delegate=yes --expand-environment=no -- DISPOSABLE_COMMAND
```

A delegated scope appeared under the user manager's `app.slice`. The ordinary
user enumerated it, created a child, opened the directory with
`O_PATH|O_DIRECTORY|O_CLOEXEC`, opened its interface files, read the initially
empty events file, and removed the child. No manual chmod/chown of cgroupfs.

Both the scope root and created child had UID/GID 1000:1000:

| Object | Mode | Tested access |
| --- | --- | --- |
| directory | 0755 | enumerate, mkdir, O_PATH, rmdir |
| cgroup.procs | 0644 | O_RDWR |
| cgroup.events | 0444 | read; later poll notification |
| cgroup.kill | 0200 | open/write as UID 1000 |
| cgroup.subtree_control | 0644 | observed, never enabled controllers |

`getfacl` showed only the corresponding owner/group/other mode entries, no named
ACL grants. Full relevant stat/ACL output is in
[delegation.txt](wave-e-evidence/architecture/delegation.txt).

These are domain cgroups. Ownership needs no resource controllers. The probes
left subtree_control empty and the supervisor at the scope root. A future design
should put the supervisor in a separate `supervisor` leaf before enabling domain
controllers; do not enable controllers on a populated inner node. Always kill
only the command cgroup, never the scope containing ops or other commands.

## Atomic placement and inherited descendants

The standalone [Go launcher](wave-e-evidence/architecture/launch.go.txt) imports
only the standard library. It opens the harness-created cgroup and calls Start
with `syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}`. It does not migrate
children after creation and does not set Setsid, Setpgid, Foreground, or Ctty.

Local Go 1.26.7 `src/syscall/exec_linux.go` was read: fields at lines 107-108;
clone3 selection and CLONE_INTO_CGROUP at 315-322; raw clone3 call at 333-334.
There is no legacy-clone fallback in that branch. Its source checksum is retained
in platform.json. Successful Start and the child's membership therefore establish
a successful clone3 operation through that code path; **no strace transcript was
captured** (strace was not installed). A closed CgroupFD caused Start to fail with
`bad file descriptor`, rather than starting outside the cgroup.

A stronger controlled proof froze an empty nested cgroup before Start. PID 166048
appeared in its cgroup.procs while the helper had not executed its marker-writing
code. After thaw, its first marker reported exactly the same cgroup. No
cgroup.procs write occurred in this placement test. Together with the inspected
Go syscall path this distinguishes atomic placement from migration after fork.
See [supplement-results.json](wave-e-evidence/architecture/supplement-results.json).

The final normal tree was PID 166374 -> 166375 -> 166376, 166377, 166378.
Each recorded UID, GID, PPID, SID, PGID, and cgroup. All were UID 1000 in the
same command cgroup. The regular processes had SID 166344 / PGID 166348;
setsid grandchild 166377 had SID=PGID=166377, while retaining the command cgroup.
All helpers installed SIGTERM ignore. No helper ran a package operation.

## Force kill, fork races, empty detection, and pipes

The ordinary Python harness wrote `1` to each command cgroup's cgroup.kill.
It used no sudo. Results in [results.json](wave-e-evidence/architecture/results.json):

- Normal tree and setsid descendants died; heartbeat sizes stopped changing.
- Twenty final stress trials used one serial forker with at most one additional
  short-lived child at a time, a maximum of 200 forks, a 12-second helper alarm,
  and a 50-second outer harness timeout. Varying millisecond delays let each
  trial advance through multiple forks before kill (12-21 recorded processes
  per trial, including six fixed tree/forker processes). No fork bomb.
- Every recorded process disappeared from `/proc` after the subreaper reclaimed
  orphans. The delegated parent's process set returned to its pre-command set;
  no tested child escaped to it. Every command cgroup became empty and removable.
- Polling at 5 ms observed `populated 1` -> `populated 0` in 5.183-5.322 ms across
  the final tree/pipe/stress run. These are sample observation times, not SLAs.
- A separate nested-child test killed the ancestor command cgroup. A poll on its
  events FD returned POLLPRI|POLLERR (mask 10), reread showed `populated 0`, and
  kill plus Wait completed in 0.81 ms. Both empty directories were removed.
- Stdout-only and stderr-only holders each called setsid and ignored SIGTERM.
  The direct child exited and was reaped; the Go launcher remained blocked in
  Wait for an additional observed 80 ms while the holder lived. After cgroup.kill,
  Wait completed within the four-second fixture bound with both output buffers
  drained. WaitDelay was zero and no pipe read end was manually closed, so this
  establishes EOF from disappearance of the inherited writer, independently for
  stdout and stderr. The direct child's successful exit still yielded nil from
  raw Cmd.Wait: the future owner must preserve cancellation separately.

The harness issues the cancellation cleanup action directly; this proves the
ownership/pipe mechanism, **not** a finished context cancellation state machine.
Stress is finite experimental support, not an exhaustive proof over all schedules.
The kernel documentation specifies concurrent-fork/migration handling for
cgroup.kill; empty detection includes live descendants. Zombies still require
reaping and are not live population. These semantics match the local probes.
[Kernel cgroup v2 documentation](https://docs.kernel.org/admin-guide/cgroup-v2.html).

## Sudo, UID-0, and deliberate escape: local gap closed by CI

The workstation sudo gate failed before any root workload was started. Its original
results remain unchanged. The separate temporary CI proof now establishes all
six previously missing properties with normal installed sudo/PAM:

1. sudo remains in the dedicated command cgroup;
2. the root helper retains that membership after credential transition;
3. root children/grandchildren and the root setsid descendant retain membership;
4. no PAM/systemd migration was observed;
5. UID-1000 filesystem writes to cgroup.kill terminate the privileged subtree;
6. root heartbeats stop, population reaches zero, zombies are reaped, and the
   ordinary user removes every command cgroup.

CI used kernel 7.2.6-arch2-1, systemd 261.3-1-arch, sudo 1.9.17p2, and exact Go
1.26.7 with GOENV=off/GOTOOLCHAIN=local. The existing SSH/PAM user manager was
running with zero failed units. A transient Delegate=yes scope was owned by UID
1000; all 13 command cgroups had cgroup.kill mode 0200 and events mode 0444,
both owned by UID 1000. Frozen placement passed in every case. Complete PID,
UID/GID, SID/PGID, membership, fork, heartbeat, EOF, and removal evidence is in
[privileged-ci-proof.json](wave-e-evidence/architecture/privileged-ci-proof.json).

The controller was PID 5282. In the normal case, command 5289 -> sudo 5295 -> root
helper 5297 -> root child 5303 -> root grandchild 5309 all remained under the
command cgroup. Root setsid PID 5331 changed SID/PGID from 900/900 to 5331/5331
without moving cgroups. Root stdout/stderr holders 5360/5361 outlived the original
command and sudo, then died with both readers reaching EOF after UID-1000 kill.
All termination checks completed in 106–107 ms including a 100 ms heartbeat sample.
The ten root-fork trials were fixed-size and finite; no fork bomb was used.

Recursive enumeration found one effective-UID-0 sudo in each live normal/setsid/
fork tree, with no additional monitor/PTY process in these non-TTY cases. Every
observed PID was verified absent after kill. Interactive sudo-PTY behavior is
still a later implementation test; it is not silently inferred from this run.

A discovery correction matters for future bootstrap validation: the first CI
attempt stopped before root workloads because the harness incorrectly required
user.delegate on a user-manager scope. In systemd v261.3, cgroup_xattr_apply
returns for user managers before applying delegation xattrs. The final probe
requires the manager's Delegate=yes, Transient=yes, and exact matching ControlGroup
and tests actual filesystem access; it records the absent marker without creating
one or altering permissions. [Exact systemd source](https://raw.githubusercontent.com/systemd/systemd/v261.3/src/core/cgroup.c).

Local sudo PAM includes `session optional pam_systemd.so class=none`; the earlier
configuration observation was not treated as execution evidence. CI exercised the
actual installed sudo/PAM path while its existing cloud-init bootstrap grant was
available, then revoked that grant and retained the normal no-sudo assertion.
No local password prompt, sudoers change, or privileged workaround was used.

Kernel source cross-check: cgroup_kill traverses descendants under its cgroup
lock and delivers SIGKILL internally rather than invoking the ordinary caller's
kill(2) permission check. This supports the hypothesis that write authority on
cgroup.kill is sufficient across UIDs. The successful CI run additionally exercises the tested guest's actual
sudo/PAM path; it does not establish every possible local policy or LSM configuration. [Kernel source](https://raw.githubusercontent.com/torvalds/linux/master/kernel/cgroup/cgroup.c).

**This is lifecycle ownership, not hostile-code confinement.** A controlled
UID-1000 helper deliberately wrote its own PID to a disposable sibling's
cgroup.procs and successfully left the command cgroup. It exited there; both
siblings were removed. Migration stayed wholly within our own scope. No process
was moved into any real service/session. The helper did not need UID 0.
An unrestricted host-root helper can likewise deliberately defeat this boundary;
intentional root escape was deliberately excluded from the acceptance proof. Kernel migration permission semantics allow
movement where destination/common-ancestor permissions permit it.
[Delegation semantics](https://docs.kernel.org/admin-guide/cgroup-v2.html#delegation-containment).

Accepted boundary for this architecture-proof task: trusted/non-adversarial fork/exec, session/group
changes, normal sudo, and ordinary package/build helpers remain owned. Intentional
cgroupfs migration, delegation to external managers, or passing FDs to unrelated
processes are outside it. This exclusion must cover deliberate same-UID escape,
not merely malicious root. cgroup.kill's migration synchronization cannot recover
a process that moved out before kill or prevent a later outside task entering.

For ops, root package hooks already possess authority to change the host; this
model is acceptable for cancellation of trusted package work, but is **not** a
security guarantee for malicious AUR scripts/hooks. The task explicitly accepts
this boundary, consistent with docs/workstation-security.md describing source
approval as a trust decision rather than sandboxing. External-manager delegation
and agent lifetimes remain explicit implementation-review work.

## Service-manager boundary

A client placed atomically in a command cgroup requested a harmless transient
**user** service via systemd-run. The service only printed /proc/self/cgroup,
had RuntimeMaxSec=5, and was collected. Its output identified its independent
app.slice service cgroup, outside the requesting command cgroup. This locally
proves manager IPC is different from fork inheritance.

No PID-1 service was started. The equivalent system-manager behavior is an
architectural inference from systemd's service/scope model: services are spawned
by the manager, while scopes contain externally created processes. The local
systemd-run(1) and systemd.scope(5) document that distinction.

For ops' explicit `sudo -n systemctl enable --now` work, a persistent service is
an intended result. Killing its systemctl client must not imply stopping or
rolling back that service. Package hooks requesting services have the same
boundary. A hook offloading a build/transaction to an external unit, however,
invalidates the assumption that transaction work is wholly inherited: that needs
an explicit ownership contract, named-unit tracking, or rejection. Do not claim
that all causal work is contained or that cancellation undoes installed state.

## Interactive terminal proof

Fresh PTY fixtures compared baseline exec and CgroupFD exec. In each:

- the Go parent remained the session/foreground process-group leader;
- child SID and PGID equalled the parent's, and tcgetpgrp(0) matched;
- stdin/stdout/stderr were inherited TTY FDs;
- the child read the explicit `wave-e-input` line and wrote both output streams;
- no stop from SIGTTIN/SIGTTOU occurred; no tcsetpgrp transfer was used.

A separate PTY exercised **systemd-run scope bootstrap itself**: it preserved
SID/PGID/foreground identity, cwd, a sentinel environment value, a literal
`$WAVE_E_SENTINEL` argv string, terminal input, and command exit code 37.
No real terminal settings were touched. This is a launch/TTY proof; Ctrl-C,
Ctrl-Z/job-control continuation, externally delivered SIGTERM, and sudo's own PTY
monitoring still need end-to-end tests of the eventual bootstrap implementation.

## Proposed cancellation design (not implemented)

Use an explicit per-command owner whose lifetime includes Start, process exit,
pipe draining, descendant cleanup, and directory removal. Keep it separate from
whether the direct process has exited. Go 1.26.7's exec.go shows watchCtx can hand
its result to Wait before pipe copying completes; a Cancel callback alone is not
sufficient to track cancellation during that later pipe wait.

1. Check ctx before allocating/starting. Allocate a unique domain child, validate
   interfaces, retain directory/events/kill handles, and install ownership before
   Start. Serialize cancellation with Start and admit no further starts into that
   command cgroup once cleanup begins. A cancel-before-Start returns the context
   error without a child; a race immediately after Start still owns the child.
2. On cancellation latch ctx.Err independently, stop work admission, and request
   graceful termination. Default SIGTERM to the direct process via a stable
   process handle. For a known terminal SIGINT, preserve the original interrupt
   intent; foreground descendants may already have received it from the terminal.
   Do not signal a shared negative PGID: it includes ops/the shell. Group signaling
   is only appropriate when the command already has a safely isolated group.
   EPERM behind sudo does not defeat cgroup escalation (as the CI root proof now establishes).
3. Recommend **2 seconds grace**, with immediate escalation on a repeated interrupt.
   End grace early when the whole command subtree is empty. A natural direct-child
   exit alone is not empty. SIGTERM is cooperative, not guaranteed graceful
   transaction completion; interrupted package transactions still require recovery.
4. Ordinary-user write of `1` to cgroup.kill; then poll/reread cgroup.events for
   `populated 0`, with **2 seconds population observation bound**. Poll notifications
   are hints: parse the current value initially and after notifications. Bound
   retries and handle spurious notifications. Never kill the supervisor's cgroup.
5. Reap the direct child and drain pipes concurrently; recommend **WaitDelay=5s** as
   a secondary pipe defense. It starts at context cancellation or direct-child
   exit, not after cgroup cleanup. Keep Cancel fast and nonblocking; run the owner
   cleanup independently. Do not serialize a slow cleanup inside Cancel, since
   Go starts its WaitDelay timer after Cancel returns. No goroutine may still be
   writing a returned buffer when Run publishes results.
6. Only after empty and final process/I/O accounting, remove nested empty cgroups
   bottom-up, then the command cgroup; close retained FDs. Cmd.WaitDelay does not
   delete cgroups or establish descendant death. A nil direct-child Wait must not
   erase a context cancellation observed while descendants/pipes remained alive.
7. Preserve the existing structured run.Error and errors.Join the relevant
   context.Canceled/DeadlineExceeded, exit error, cleanup errors, and ErrWaitDelay.
   Avoid double-wrapping identical errors; retain errors.Is/errors.As behavior.
   Give ownership-cleanup failure priority in the user-facing diagnostic while
   preserving machine-readable cancellation. Do not include sensitive argv/output.

The numerical bounds are recommendations for review, not experimentally established
worst-case kernel latency. With ordinary responsive processes, the intended grace
and population phases fit within about four seconds, with the five-second I/O
bound concurrent. Arbitrary blocking output writers/readers are not made bounded
by closing an os/exec pipe. Uninterruptible kernel sleep can also postpone process
exit after SIGKILL; WaitDelay cannot universally bound Process.Wait. An absolute
promise of both bounded return and guaranteed disappearance under such failures
is unsatisfiable. Define and test a separate fatal cleanup-failure policy.

| Race/failure | Required behavior |
| --- | --- |
| Context done before Start | Return context error; remove empty allocation; never launch |
| Natural child exit during cancel | Still inspect descendants and pipes; latch cancellation if command lifetime is unfinished |
| Natural exit with lingering descendants, no cancel | Do not silently let daemons escape command lifetime; bounded lingering-descendant cleanup policy and agent compatibility need explicit review |
| cgroup.kill ENOENT | Benign only when the same owned object is already confirmed removed/empty by serialized cleanup; do not treat arbitrary path disappearance as proof of no survivors |
| Concurrent cleanup | Exactly one owner; idempotent state transitions; never recreate a removed path under an old cleanup action |
| Empty becomes populated again | Treat as ownership/admission violation; a kill is not a permanent admission barrier |
| Kill/poll/reap/removal failure | Abort further mutation, return a fatal joined error with command/unit identity and incomplete-cleanup status; preserve the remaining owned cgroup for diagnosis, never claim cleanup succeeded |
| Supervisor crash/SIGKILL | User scope does not inherently disappear while descendants remain; process-crash containment is a separate unresolved policy, not proven by normal cancellation |

Recovery commands using Background/WithoutCancel still need ownership and their own
finite cleanup lifetime; do not accidentally cancel intended rollback by reusing an
already cancelled command owner. New versus pre-existing GPG/SSH agents and external
FD handoff need a lifetime audit before killing leftovers on every successful exit.

## Bootstrap alternatives and recommendation

| Alternative | Advantages | Costs/risks |
| --- | --- | --- |
| A. Re-exec through systemd-run --user --scope -p Delegate=yes | Installed systemd CLI, no Go D-Bus dependency; local PTY/cwd/env/argv/status proof passed | Correct argv and --expand-environment=no essential; re-exec can repeat inspection/approval or lose in-memory plan; requires recursion guard validated against actual delegation, plus signal/job-control tests |
| B. Register current ops PID in a transient scope through user-manager D-Bus | Preserves live approved plan, process/TTY identity, cwd, environment, and exit path; can establish ownership immediately before first privileged/mutating work | D-Bus transport not in Go stdlib or current deps; requires reviewed transport/dependency choice, job-completion/error handling and direct registration proof |
| C. Accept an already delegated invocation, or use a transient service | Existing scope can avoid re-exec if delegation/ownership is verified; services offer DelegateSubgroup= on current systemd | Ordinary session cgroup is not automatically a delegated ops subtree; services change parent/env/stdio semantics; --pty introduces a different terminal; no generally superior transparent service mechanism found |

**Preferred production direction: B, lazily before sudo acquisition or mutation,
with C for an explicitly verified existing delegation.** This avoids approval
replay and keeps the existing terminal process intact. It is a design recommendation,
not approval of an untested implementation or authorization to add a dependency.
A is a demonstrated viable bootstrap option if a review chooses re-exec and can
prove single-approval state handling. Its lack of a new Go dependency alone is not
a sufficient reason to choose it.

Use a unique transient scope, `Delegate=true`, and the actual PID; wait for the
StartTransientUnit job to complete, then obtain ControlGroup from the manager and
cross-check /proc/self/cgroup and writable interfaces. Do not manufacture a path
from a unit name. An environment marker alone is not delegation authorization.
No persistent unit installation, root helper, linger enabling, per-command sudo,
or system-wide cgroup permission edits are needed by the demonstrated user path.
The already running systemd/logind/PID 1 remain part of the workstation platform;
“no privileged supervisor” means **no additional ops-owned privileged supervisor**.
[Systemd delegation contract](https://systemd.io/CGROUP_DELEGATION/).

Graphical terminal, TTY login, and SSH should work where a normal PAM/logind login
provides the user manager/runtime bus. The graphical/interactive session here
worked; fresh TTY and SSH were not separately tested. A missing bus, non-systemd
host, chroot, read-only cgroup mount, denied clone3, or undelegated container must
produce a clear capability failure before mutation. Do not start/enable user
services or linger to conceal the missing prerequisite. Re-exec markers must be
validated and bounded; direct D-Bus registration avoids recursive exec entirely.

Help/version should never need a scope. Doctor and pre-approval planning should
remain available for diagnosis and declining a plan; a blanket startup scope
requirement would break their existing container contract. A separately reviewed
restricted read-only path could allow only trusted nonmutating queries with
bounded I/O and no sudo, hooks, agent creation, or service starts. Such a path
has reduced ownership guarantees and must be explicit, not a silent reusable
process-group fallback. Alternatively report partial filesystem-only diagnostics
when strong ownership is unavailable. Exact degraded-plan fidelity is unresolved.
Approval must never cross into mutation without successful strong ownership.

## Platform contract and CI impact

Current repo contract: README supports official Arch x86_64 as a normal user;
internal/system/system.go checks ID=arch, non-root, and uname architecture, but
no kernel/systemd/bus/delegation requirement. docs/architecture.md lists baseline
sudo/pacman/CA/GnuPG utilities. systemctl is already used for declared services,
yet that does not imply an accessible user manager for every command. This candidate
would add a real, explicit mutation prerequisite; no contract was silently changed.

Feature floors and proposed supported contract:

- Linux 5.7 introduced CLONE_INTO_CGROUP; O_PATH is permitted.
  [clone(2)](https://man7.org/linux/man-pages/man2/clone.2.html).
- Linux **5.14** supplies cgroup.kill, absent from 5.13's cgroup implementation;
  therefore 5.14 is the combined kernel feature floor, with writable domain
  cgroups in the unified hierarchy and clone3 not blocked by seccomp/LSM.
  [5.14 source](https://raw.githubusercontent.com/torvalds/linux/v5.14/kernel/cgroup/cgroup.c),
  [5.13 source](https://raw.githubusercontent.com/torvalds/linux/v5.13/kernel/cgroup/cgroup.c).
- Local manuals date Delegate= to systemd 218. This is the property floor, **not**
  a claim that all historical user-manager configurations are supported.
  The exact CLI used here needs **systemd 254** for --expand-environment=no;
  DelegateSubgroup= also appeared in 254. Delegation xattrs appeared in 251, but
  are not a required user-manager scope marker in the observed systemd 261.3;
  validate manager properties plus actual filesystem authority instead.
  The observed sudo PAM class=none behavior dates to **258**.
- Recommend the maintained, fully updated Arch/systemd workstation configuration,
  not historic minima as an unconditional support promise. Validate capabilities
  at runtime even above these versions. Tested combination: kernel 7.2.6,
  systemd 261.3, Go 1.26.7, with sudo 1.9.17p2 now proved in the Arch CI guest. No kernel/systemd
  minimum can alone guarantee sudo policy, delegation permissions, or user-bus access.
- Go probe/build contract stays **1.26.7, GOENV=off, GOTOOLCHAIN=local**.
  No toolchain installation, version switch of system defaults, or dependency change.

Fail closed for mutation if any required capability or ownership operation fails.
An insecure process-group-only downgrade is unacceptable. Read-only degradation
must have an explicitly narrower contract as above.

The following describes the original CI surfaces and later production integration
work. Only the disposable proof branch changed ci.yml/test-ci-arch.sh and added
two temporary probe files; no workflow change was applied to this worktree:

| Surface | Existing behavior | Necessary later change |
| --- | --- | --- |
| .github/workflows/ci.yml / protected ci | Disposable real Arch KVM guest, ordinary ops-ci over SSH, exact checkout/Go transfer | Assert actual user-manager/bus/delegation/clone3/kill capabilities over SSH; run ownership integration plus PTY/race/failure-path tests with no silent skips |
| script/test-ci-arch.sh | Installs guest tools then deletes cloud-init sudo grant and explicitly asserts sudo -n fails; creates runtime dir but does not itself establish a user manager | Keep ordinary suite unprivileged. Add a separate reviewed disposable-VM root proof phase while existing bootstrap authorization is still available, or a separately authorized bounded fixture; run launcher/kill writer as ops-ci, helper alone via sudo; revoke grant and verify teardown before ordinary suites |
| .github/workflows/arch.yml / protected build | Ubuntu build produces unsigned ops and resolve.test artifacts using exact Go | Compilation itself needs no live cgroup; do not bootstrap at package init. Preserve build job and artifact contract |
| protected minimal-runtime / script/test-minimal-arch.sh | Official Arch Docker container, no systemd PID 1/user manager, root preparation then runuser; doctor, native queries, real-TTY first-run decline | Preserve container as negative/degraded capability test; doctor and decline must stay nonmutating without ownership setup. Add explicit mutation-refusal coverage; do not make it privileged or mount host cgroupfs to fake a workstation |
| Native Bubblewrap tests | --unshare-all, --new-session, read-only root, private proc; DBus disabled for read-only inventory | Launch bwrap itself in command cgroup before its namespaces. Assert ownership from outside since inner cgroup namespace paths can differ; test kill/EOF with PID namespaces. Keep current native isolation assertions and no-skip policy |
| Fake-runner/unit tests | Production boundary is injected | Add an explicit owner capability/test fixture; do not silently disable ownership under test or require a bus for compile/fake tests |

The protected names remain `ci`, `build`, `minimal-runtime`. The temporary CI run
now proves the privileged cases in the existing real VM before sudo revocation;
the entire ordinary suite then passed without sudo. Permanent production coverage
still requires separate review. No preserved VM was used or modified.

## Evidence, reproducibility, and cleanup

The original workstation evidence is confined to `docs/wave-e-evidence/architecture/`
and remains unchanged:

- launch.go.txt: standard-library Go launcher with inherited or captured FDs;
- workload.py: bounded ordinary tree, setsid, heartbeat, pipe, TTY, escape helpers;
- harness.py: ordinary-user subreaper, kill/empty/reap assertions and stress;
- supplement.py: frozen atomic placement, nested kill/events poll, manager IPC,
  and scope bootstrap PTY;
- delegation.py and delegation.txt: permission inspection and observed output;
- results.json and proof.txt: final ordinary-user suite observations;
- supplement-results.json and platform.json: supplemental results and provenance;
- cleanup.json: final teardown, original-evidence hashes, and repository checks.

The Python fixtures require UID 1000, matching this workstation; they are disposable
architecture evidence, not a generic installed test harness. All mutations were
inside a fresh mode-0700 /tmp directory or the newly delegated disposable scopes.
Root workloads are deliberately absent from those original workstation harnesses;
the separate privileged CI patch and structured evidence now cover them. An earlier
20-trial exploratory run was strengthened to exercise multiple serial forks before
kill; results.json preserves the final stronger run, not just the first attempt.

To reproduce in a fresh temporary directory (never execute from the evidence
directory itself): copy the four .py files, copy launch.go.txt as launch.go, build
with the exact Go 1.26.7 binary and GOENV=off/GOTOOLCHAIN=local with GOCACHE inside
the temporary directory. Run delegation.py, harness.py, and supplement.py each
under a uniquely named `systemd-run --user --scope --collect -p Delegate=yes
--expand-environment=no` scope. Use an outer timeout (50s for harness, 25s for
supplement, plus 3s kill-after). Check all generated unit names are unloaded,
all scoped cgroups gone, all recorded PIDs gone, then remove the temporary directory.
Helpers have finite lifetimes; on failed fixture assertions, inspect and stop only
the exact disposable scope before removing its files. Never use a wildcard kill
against unrelated processes. The Go launcher exposes a creation API; the harness
creates the cgroup, and the combined program is the atomic-placement experiment.

Final validation is recorded in cleanup.json: exact probe units not-found, no
matching cgroup directories, all retained recorded helper/launcher PIDs absent,
no root-owned probe files, /tmp probe directory removed, original hashes equal,
HEAD/branch unchanged, tracked/index diff empty, and git diff --check clean.
That earlier workstation phase made no commit or push. This follow-up made and
pushed only the two explicitly authorized experimental commits on the temporary
proof branch; neither was merged. The original Wave E branch was not pushed.
There was no persistent configuration change, production implementation, PR,
merge, tag, release, R2 action, signing action, preserved-VM operation, toolchain
change, dependency addition, or Wave-F-or-later work. New final textual evidence
is limited to privileged-ci-proof.md/json/patch, privileged-ci-platform.txt, and
privileged-ci-preservation.json. The preservation audit records all 16 original
file hashes, unchanged hashes for 15 files, and this report's old/new hashes.

## Findings and decision

**Unresolved Critical architecture findings: none for the specified ordinary
inherited-process ownership model on the tested Arch platform.** The real sudo/PAM
transition, UID-0 descendants and setsid, bounded root forking, and ordinary-user
kill/empty/reaping/pipe/removal gates all passed in protected CI.

**Unresolved Important implementation-review work:** select and prove the direct
D-Bus bootstrap; full signal/job-control and sudo-PTY behavior; unsupported-login
and read-only capability behavior; external manager/hook/agent lifetimes;
failure/race/context-error composition; supervisor-crash and uninterruptible-task
failure policy; and permanent protected-CI coverage. These remain outside this
experimental proof and are not authorization to begin implementation. The accepted
threat model excludes intentional root and same-UID cgroup manipulation.

Recommendation: **APPROVE for a separate Wave E implementation review.** The
previous privileged architecture blocker is closed. This does not merge or approve
any production implementation; no run.Exec, cgroup manager, D-Bus bootstrap,
cancellation timing, or WaitDelay changes were made.

| Requirement | Result |
| --- | --- |
| normal descendants contained | PASS — local and privileged CI |
| setsid descendants contained | PASS — local and UID-0 CI case |
| atomic child placement | PASS — local and 13 CI frozen-placement cases |
| sudo UID transition contained | PASS — normal installed sudo/PAM in Arch KVM guest |
| root descendants contained | PASS — exact command membership and measured UID 0 |
| ordinary-user cgroup.kill root subtree | PASS — UID-1000 filesystem write in all 13 cases |
| sudo monitor/helper accounting | PASS — recursive enumeration; all observed sudo PIDs removed; no separate monitor observed in non-TTY runs |
| fork race bounded | PASS — 20 local trials and 10 bounded root-fork trials; finite scheduling evidence |
| cgroup empty detection reliable | PASS — population zero and no recursive processes in all 13 privileged cases |
| inherited stdout cleanup | PASS — root holder EOF after original command exit |
| inherited stderr cleanup | PASS — independent root holder EOF after original command exit |
| command cgroup removal | PASS — ordinary UID 1000 in all 13 cases |
| interactive TTY unchanged | PASS — existing local launch/CLI fixtures; production and sudo-PTY integration still need review |
| no persistent privileged supervisor | PASS — no additional ops-owned privileged supervisor |
| no per-command privileged ownership helper | PASS — only workload credential transition uses sudo; ownership/kill entirely UID 1000 |
| current Arch platform supports model | PASS — workstation user proofs plus complete privileged Arch KVM proof |
| CI model can exercise architecture | PASS — temporary proof plus full normal CI after sudo revocation |
| deliberate malicious root confinement | OUT OF SCOPE — explicitly excluded; not a sandbox |
| systemd-spawned service ownership | SEPARATE BOUNDARY — manager-owned services may outlive the requesting command |

See [the privileged CI report](wave-e-evidence/architecture/privileged-ci-proof.md)
for the process table, precise test limits, source correction, complete run links,
normal CI result, and reproduction patch. The original blocker document remains
unchanged evidence of the still-unmodified production implementation's failures.

## Production implementation — 2026-09-19

The sections above are the preserved pre-implementation architecture evidence.
The user subsequently authorized production implementation on the same branch.
The original bytes are retained in commit `03df753`; the pre-implementation
SHA-256 manifest is `wave-e-evidence/implementation-baseline.sha256`.

### Responsibility and bootstrap

`internal/run.Owner` is the single process-lifetime controller. `DefaultRuntime`
creates it once; Exec copies, `WithIO`, trusted repository wrappers and the
cancellation wrapper retain the same pointer. No package initializer activates
ownership. Fake-runner application tests explicitly supply a fake capability.
Standalone Exec values without a controller retain the read-only/direct-child
contract; production mutation always uses DefaultRuntime's controller and gate.

Reconcile activates immediately after affirmative top-level confirmation, before
sudo acquisition. Update activates after its single confirmation, before even
download/verification, and uses the terminal-rebound runner for verification and
replacement. Both preserve the approved in-memory plan. Doctor, no-op setup,
decline and update checks never activate. Unsupported confirmed mutation returns
a fatal safe-subprocess-ownership diagnostic before sudo or managed-state writes.

Bootstrap uses the systemd **user** manager's `StartTransientUnit` D-Bus API,
registering the current PID in a random-128-bit-named `ops-*.scope`. Properties
are `Delegate=yes` and `CollectMode=inactive-or-failed`. It subscribes to job
completion before requesting activation, then verifies the matching job, unit
properties, current `0::` membership, and the actual cgroup2 filesystem. It
connects directly to `/run/user/<effective-uid>/bus`; no shell parsing, re-exec,
root helper, system-manager fallback, persistent units, or lingering changes.

The focused dependency is `github.com/godbus/dbus/v5 v5.2.2`, with its sole
transitive module `golang.org/x/sys v0.27.0`. Existing TOML v2.2.4 is unchanged.
The library supplies established authentication, framing, types, signals, and
context cancellation. systemd-run cannot move this existing process into its
own new scope without changing launch semantics; busctl would add an external
bootstrap command and CLI argument/result encoding. Neither is preferable to
the typed current-PID API. The build works with CGO disabled. Go remains 1.26.7.

Activation proves real mkdir/open/read/write/remove capability on an empty child,
including opening cgroup.kill as the ordinary user and writing it. A second
inert `/usr/bin/true` probe proves CLONE_INTO_CGROUP and pidfd availability before
sudo. Population accounting can briefly outlive reaping, so the probe uses a
bounded population observation rather than a single immediate snapshot. No
version string or `user.delegate` xattr substitutes for these checks.

### Commands, cleanup, errors, and terminal behavior

Every post-activation command gets a monotonically numbered child cgroup under
the retained scope directory. Names have no user-controlled component. Directory
and cgroup interface descriptors stay open through Start and cleanup. Linux
SysProcAttr sets only `UseCgroupFD`, `CgroupFD`, and `PidFD`; no process-group or
session attributes are introduced. Failure to start never retries unowned.
The frozen-cgroup integration proves the child is contained before userspace
executes, then checks its first recorded membership after thawing.

The executor owns cancellation while os/exec owns reaping and stream copying.
A pidfd separately observes direct-child exit when Wait is still blocked by
inherited pipes. Already-published Wait completion wins a simultaneous context
cancellation; otherwise the latched cancellation cause is joined with the exit
and cleanup errors. `run.Error` and errors.Is for cancellation/deadlines survive.
Deterministic channel tests cover both arbitration orders and observer failures.

Cancellation requests SIGTERM of the direct child where permitted, allows two
seconds for population to disappear, then writes the command's cgroup.kill as
UID 1000. It observes cgroup.events populated=0 for up to two more seconds before
reaping/draining, with a further five-second bound. Cmd.WaitDelay is five seconds
and is a secondary pipe/direct-child safeguard; it starts on context cancellation
or direct-child exit with unfinished I/O. It does not establish ownership.
Natural exit allows a 50ms accounting-settle interval, then kills remaining
background descendants and reports a lifecycle failure. Successful ordinary
output is drained without truncation beyond the existing capture policy.

A cgroup is removed only after authoritative populated=0 and successful reap/
drain. Empty nested cgroups are removed using a bounded walk of that command's
subtree only; ordinary commands take one rmdir. There is no production /proc
scan or descendant-PID enumeration. Nested live population, setsid, forks and
sudo credential transitions remain covered by the kernel boundary.

The real sudo PTY regression found that authoritative killing also kills sudo's
monitor before it can restore its caller's terminal modes. Interactive execution
therefore snapshots the supplied TTY and restores its modes on command failure;
successful commands retain their usual terminal semantics. There is no tcsetpgrp
choreography or output redesign. The test inventories both sudo processes and
root helpers, checks input, sends keyboard Ctrl-C and an explicit SIGTERM to ops
(sudo may route keyboard interrupts into its own PTY), checks complete cleanup,
then reads another line and verifies original termios. This does not claim that
a keyboard interrupt ignored by an interactive program always cancels ops.

### Mutation admission and lifecycle boundaries

Uncertain cleanup, changed scope membership, lost capability, removal failure or
reap/drain timeout permanently poisons the controller. No later command starts.
`beginMutation` requires active healthy ownership; short persistent SSH/GnuPG
file changes also share the controller's admission mutex with poisoning. Core,
AUR, Flatpak, Git, SSH, GitHub and update paths classify ownership errors as fatal,
including cleanup errors joined to cancellation. Application failure loops and
final reinspection stop. Sudo acquisition/refresh commands use separate command
cgroups; Keeper.Close waits for refresh cleanup and retains ownership errors.
Updater recovery commands keep their independent contexts but the same owner.

The command inventory found the following intentional lifetime boundaries:

- Direct daemonization, setsid, build/helper background processes and newly
  inherited sudo/root processes remain owned and cannot survive Run.
- Public GnuPG import/fetch/export can auto-start dirmngr, keyboxd or gpg-agent.
  These synchronous public-key calls explicitly declare ephemeral helpers. They
  are still killed and verified empty before return; only the unexpected-leak
  diagnostic is suppressed for that declaration. Repeated classic/keyboxd
  import/export regression checks committed public data survives cleanup.
  Release verification uses isolated GnuPG with `--no-autostart`.
- Existing SSH/GPG agents reached through sockets were not forked by the command
  and are not killed. ops does not start a persistent ssh-agent.
- systemctl can request a service from PID 1 or the user manager. That service
  is manager-created state outside the initiating command's cgroup. A native
  regression confirms it survives the initiating Run and is stopped explicitly
  for fixture cleanup. Operation/recovery policy remains responsible for such
  services. No manager-owned service is killed as a descendant.

This is lifecycle ownership, **not hostile-root sandboxing**. Deliberately
malicious root or same-UID code manipulating cgroupfs is outside the model.
Existing Bubblewrap read-only isolation, --die-with-parent and no-fallback
behavior remain independent and intact; bwrap itself is atomically owned.

### Crash and uninterruptible-task limitations

SIGINT/SIGTERM delivered to ops cause the bounded Go cleanup above. SIGKILL or a
hard crash prevents Go cleanup and TTY restoration. A scope is not a supervisor
bound to its registering PID: it remains populated while inherited descendants
live. The native crash regression kills its fixture owner, observes surviving
population, then uses the surviving test parent to clear only that fixture.
Systemd collects the transient scope after all population disappears. Production
does not add a permanent crash supervisor or promise crash rollback.

A kernel task in uninterruptible sleep can remain after cgroup.kill. The bounded
cleanup then reports incomplete ownership, poisons admission and preserves the
command cgroup. os/exec cannot forcibly finish reaping such a task, or unblock
an arbitrary caller-supplied reader/writer. The exceptional reap/drain bound
returns without reading buffers still used by Wait; any pending goroutine ends
only when the kernel/IO permits. Normal inherited-pipe holders are killed and
reach EOF, as verified by both root and ordinary-user tests. Neither this nor
crash behavior is represented as successful cleanup.

### Permanent validation

Dedicated `ownership_integration` tests exercise production code; ordinary
`go test ./...` gains no environmental skips. CI runs real sudo/root/fork/PTY
and sudo-keeper tests, then their targeted race versions, before removing the
existing cloud-init sudo grant. It verifies sudo -n true fails, then continues
the existing ordinary and complete race suites and skip audit. Unprivileged
native ownership/GPG/crash/updater tests run separately after revocation.

The minimal Arch container retains Doctor and decline checks and now confirms a
plan with a disposable sudo sentinel. It verifies fatal ownership refusal,
no sudo invocation, unchanged package/config state, and no managed SSH files.
No privileged-container workaround or host cgroup mount is introduced.

Local invocation (normal user, no sudo):

```sh
PATH="$HOME/.local/opt/go1.26.7/bin:$PATH" GOENV=off GOTOOLCHAIN=local \
  go test -count=1 -tags ownership_integration ./internal/run ./internal/pgp ./internal/release \
  -run '^(TestNativeOwnership|TestNativeCrashScopeLifetime|TestNativeGPGHelpers|TestNativeUpdaterOwnership|TestOwnershipPTY)$'
```

Detailed implementation validation and review results are retained separately in
`wave-e-evidence/implementation/validation.md`.
