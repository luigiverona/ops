# Wave E / I-02 privileged-descendant CI architecture proof

Date: 2026-09-18. **Recommendation: APPROVE for a separate implementation review.**
Experimental evidence only. No Wave E production implementation was made.

The actual sudo/PAM transition, inherited UID-0 tree, root setsid process, bounded
root forking, ordinary UID-1000 cgroup.kill, empty detection, inherited stdout and
stderr EOF, reaping, and command-cgroup removal all passed. The complete existing
CI suite subsequently passed without sudo.

## Provenance and reproduction

- Repository: `luigiverona/ops`.
- Protected original worktree: `/home/ah/ops`, branch
  `fix/process-tree-cancellation`, unchanged HEAD
  `50adbe8518aee4108ee7bd7b6532e6a9ad9daf7f`.
- Disposable proof worktree: `/tmp/ops-wave-e-root-proof`.
- Temporary branch: `proof/wave-e-root-cgroup`, based on that exact main SHA.
- Initial experimental commit: `ef3852327cfa37badf790e1208879d7291628555`.
- Final experimental commit: `9a36866ffb15ea09736a66d2760456bac9302c34`.
- Successful [Actions run 35393152228](https://github.com/luigiverona/ops/actions/runs/35393152228),
  job `105755893743`, runner version `2.337.0`.
- The only changed tracked files on the temporary branch were `.github/workflows/ci.yml`,
  `script/test-ci-arch.sh`, `script/wave-e-root-cgroup-proof.go`, and
  `script/wave-e-root-cgroup-proof.sh`. No production Go package or module changed.
- The workflow added exactly `proof/wave-e-root-cgroup` to its push branch list.
  No PR, merge, force push, tag, or release was used.

[privileged-ci-proof.patch](privileged-ci-proof.patch) contains the complete final
temporary delta against the baseline. Reproduce only in a fresh disposable checkout
and the existing disposable KVM CI workflow, retaining its image checksum and exact
Go transfer. Do not execute the CI helper on a workstation. This patch is evidence,
not a proposed production change; do not merge the temporary branch or its trigger.

[privileged-ci-proof.json](privileged-ci-proof.json) retains all unique structured
proof events, process identities, full run metadata, and the decision details.
[privileged-ci-platform.txt](privileged-ci-platform.txt) is a small textual platform
and validation excerpt. No credentials, SSH keys, disks, package caches, or full
workflow logs were copied back. Cleanup and before/after hashes are recorded in
[privileged-ci-preservation.json](privileged-ci-preservation.json).

After preserving this evidence, the remote proof branch was deleted and its
absence verified with ls-remote. The clean temporary worktree was removed without
force. Its local branch was then deleted with `git branch -D` because the verified
experimental commits were deliberately unmerged; the exact final identity was
checked before deletion. Only the original Wave E worktree remains. Its tracked
and staged diffs are empty, and its branch/HEAD and empty stash list are unchanged.
All 15 original files other than the intentionally updated architecture report
retain their original SHA-256 hashes. The architecture report's old/new hashes
are recorded in the preservation audit.

## Platform and delegation

| Property | Observed result |
| --- | --- |
| Guest | Official checksum-pinned Arch image, actual KVM |
| Kernel | `7.2.6-arch2-1`, x86_64 |
| systemd | `261 (261.3-1-arch)` |
| sudo and policy/I/O/audit plugins | `1.9.17p2` |
| User | `ops-ci`, UID/GID 1000 |
| Go | `go1.26.7 linux/amd64`, `GOENV=off`, `GOTOOLCHAIN=local` |
| cgroup mount | `cgroup2fs`, rw, `nsdelegate` |
| Initial SSH/PAM membership | `/user.slice/user-1000.slice/session-9.scope` |
| User bus/manager | Existing `/run/user/1000/bus`; running, zero failed units |
| User-manager cgroup | `/user.slice/user-1000.slice/user@1000.service` |
| Scope | `ops-wave-e-root-proof-FAbEIPoO.scope` |
| Manager properties | `Delegate=yes`, `Transient=yes`; ControlGroup equals controller membership |

The exact delegated path, denoted **S** below, was:

```text
/user.slice/user-1000.slice/user@1000.service/app.slice/ops-wave-e-root-proof-FAbEIPoO.scope
```

The scope used the previously proven invocation:

```text
systemd-run --user --scope --quiet --collect --unit=UNIQUE \
  --property=Delegate=yes --expand-environment=no -- DISPOSABLE_PROBE
```

No lingering, persistent service, manager configuration, cgroup chmod/chown,
privileged ownership helper, or additional privileged supervisor was introduced.
Scope and all 13 command directories were UID/GID 1000:1000, mode 0755.
Each command's `cgroup.kill` was UID/GID 1000:1000, mode 0200, opened writable by
UID 1000 before launch. `cgroup.events` was 1000:1000, mode 0444, and readable.

The [initial run 35387117583](https://github.com/luigiverona/ops/actions/runs/35387117583)
stopped before any command cgroup or root workload because the probe incorrectly
required the `user.delegate` xattr. Getxattr returned ENODATA. Inspection of the
[exact systemd v261.3 source](https://raw.githubusercontent.com/systemd/systemd/v261.3/src/core/cgroup.c)
showed that `cgroup_xattr_apply` returns for user managers before calling
`cgroup_delegate_xattr_apply`; the latter's writes are also best-effort.
The corrected probe verifies the manager's Delegate/Transient/ControlGroup
properties and then actual filesystem authority. It still records the absent
xattr. No property was repaired and no ownership acceptance gate was relaxed.
This was a probe discovery error, not a containment/kill failure. Both runs revoked
bootstrap sudo and deleted their disposable VMs.

## Identity, containment, and setsid

The controller was PID 5282, all four `/proc/PID/status` UID fields 1000,
SID/PGID 900, cgroup S. Every command was created with the Go standard-library
`syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}`. Each empty command cgroup
was frozen before Start. A child PID appeared there while its userspace marker
was absent; only after thaw did its first record appear in that same cgroup.
All 13 frozen-placement cases passed. There were no post-fork cgroup.procs writes.

Representative process evidence follows; the JSON retains every case and PID.
UID columns show real/effective/saved/filesystem UIDs from `/proc/PID/status`.

| Process | PID / PPID | UIDs | SID / PGID | Cgroup |
| --- | --- | --- | --- | --- |
| Controller | 5282 / 4594 | 1000/1000/1000/1000 | 900/900 | S |
| Launched command | 5289 / 5282 | 1000/1000/1000/1000 | 900/900 | S/00-normal |
| sudo | 5295 / 5289 | 1000/0/0/0 | 900/900 | S/00-normal |
| Root helper | 5297 / 5295 | 0/0/0/0 | 900/900 | S/00-normal |
| Root child | 5303 / 5297 | 0/0/0/0 | 900/900 | S/00-normal |
| Root grandchild | 5309 / 5303 | 0/0/0/0 | 900/900 | S/00-normal |
| Root setsid, before | 5331 / 5324 | 0/0/0/0 | 900/900 | S/01-setsid |
| Root setsid, after | 5331 / 5324 | 0/0/0/0 | 5331/5331 | S/01-setsid |
| Root stdout holder | 5360 / 5282 | 0/0/0/0 | 900/900 | S/02-pipes |
| Root stderr holder | 5361 / 5282 | 0/0/0/0 | 900/900 | S/02-pipes |

The normal installed sudo/PAM path ran via `sudo -n -- PROBE helper ...`.
Every helper and inherited root descendant reported the exact command cgroup.
The explicit setsid call changed both SID and PGID to 5331 and left membership
unchanged. UID 0 was measured, not inferred from command spelling.

Every command cgroup was enumerated recursively. The normal/setsid/fork snapshots
each showed one sudo process, with effective UID 0; no separate monitor/PTY process
was observed in these non-TTY cases. All enumerated PIDs were required absent
after kill. The pipe case had only the two orphaned UID-0 holders left before kill;
sudo and the original command had already exited. No particular sudo process shape
was assumed. Interactive sudo-PTY behavior remains an implementation-review test,
not something this non-TTY run claims to have exercised.

## UID-1000 kill, root forking, and retained pipes

Immediately before every kill the controller executed `id -u`, checked real and
effective UID 1000, and emitted all four UID fields. The decisive operation was
`os.WriteFile(commandCgroup + "/cgroup.kill", []byte("1"), 0200)` in that controller.
It used no sudo, systemctl kill, privileged signal helper, or capability grant.

All 13 writes passed. Each case then required bounded populated-zero observation,
command Wait completion, both pipe EOFs, child reaping, absence of every recorded
and enumerated PID, stable heartbeat sizes, an unchanged scope-parent process set,
an empty recursive cgroup enumeration, and successful UID-1000 cgroup removal.
Every condition passed. The controller used unprivileged PR_SET_CHILD_SUBREAPER
solely to reclaim orphan zombies; it did not use that facility to signal processes.

The ten `03-fork` through `12-fork` trials each allowed at most eight nonrecursive
forker children, plus the fixed root helper/child/grandchild/forker fixture.
After two fork children were ready, the forker waited for the controller's gate.
The controller then killed during the unfinished six-fork interval, after a third
child was ready. Every observed trial recorded fork-0, fork-1, and fork-2; later
records were checked after kill as well. The delegated parent never acquired a
descendant, and no root process survived. This is bounded stress evidence over
ten schedules, not a mathematical claim that SIGKILL interrupted a particular
clone instruction or that all kernel interleavings were enumerated.

For `02-pipes`, the root helper launched independent stdout-only and stderr-only
holders and exited. The original UID-1000 command was reaped before cancellation;
both readers remained open during separate 80 ms pre-kill checks. Ordinary-user
cgroup.kill removed both root holders. Both readers then reached EOF without
manual read-end closure, and controller wait/cleanup completed. Each holder's
heartbeat remained at nine bytes after death. Thus the original inherited-pipe
failure is covered across the sudo transition even after the direct child exits.

Each wait/poll had an 8-second bound and each process a 45-second independent
expiry. Observed kill-to-completed-termination checks were 106–107 ms, including
the deliberate 100 ms heartbeat stability sample. These observations are not
production latency guarantees. No SIGTERM-ignoring variant was needed or run.

## Normal CI restoration and safety boundary

Proof PASS was logged at 20:47:22 UTC, followed by sudo-revocation PASS. The
unchanged `sudo -n true` failure assertion preceded all ordinary validation.
Non-root namespaces/bwrap, native Arch and Flatpak tests, formatting, vet,
the full ordinary suite, the full race suite, skip audits, build, and shell checks
all passed. VM/disk/seed/ephemeral-key cleanup passed at 21:00:04 UTC. No VM was
preserved. Guest package/bootstrap and exact Go transfer followed existing CI;
no toolchain, dependency, or workstation configuration change was introduced.

This is lifecycle ownership of trusted ordinary process descendants. A malicious
UID-0 process may deliberately manipulate cgroupfs and defeat that boundary;
intentional same-UID cgroup migration can also defeat it. Neither is a supported
sandbox claim, and neither escape was attempted by the privileged acceptance
probe. This is acceptable for normal sudo, pacman, approved package hooks, and
build helpers under ops' existing trust contract: source approval is explicitly
a trust decision, not sandboxing (`docs/workstation-security.md`, AUR section).
No claim is made that cgroups make malicious approved code safe.

The service-manager boundary is unchanged:

- A daemon directly forked/detached by the package command remains an inherited
  cgroup member and should be killed on cancellation, including after setsid.
- A service explicitly requested through PID 1/systemd, for example by a package
  hook invoking `systemctl start`, is independently manager-owned and may
  legitimately outlive its requesting command. It is not an inherited child.

No arbitrary system service was started for this proof. Work offloaded to an
external build/transaction service requires its own lifecycle contract. This
mechanism does not roll back already completed package state or service starts.

## Decision and remaining review work

**Unresolved Critical architecture findings: none for the stated inherited-process
ownership model on the tested Arch platform.** All privileged acceptance gates pass.

Important implementation-review work remains: selected D-Bus bootstrap and platform
capability failure behavior; full terminal/signal/job-control and sudo-PTY paths;
external service/agent lifetimes; context/error/race handling; supervisor-crash and
uninterruptible-task failure policies; and permanent regression coverage without
weakening the normal CI no-sudo policy. This experimental patch is not that work.

**APPROVE** clears the architecture for a separate Wave E implementation review.
It does not authorize production implementation or claim universal containment of
malicious root. No run.Exec, cgroup manager, D-Bus bootstrap, cancellation timing,
or WaitDelay production changes were made.
