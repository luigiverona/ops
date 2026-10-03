# OPS canonical finalization context

THIS FILE IS THE CANONICAL FINALIZATION ROADMAP AND HANDOFF.
VERIFY LIVE GIT/GITHUB STATE BEFORE ACTING.
CORRECTNESS OVERRIDES ROADMAP CLOSURE.
LATER FINDINGS MAY REOPEN EARLIER WAVES.

This document owns the remaining finalization roadmap, finding accounting, and
release acceptance bar. Do not reconstruct or simplify the roadmap from older
session reports. Technical architecture, evidence, and operating procedures
remain in their linked documents; they do not replace this handoff.

## Verified repository state after Wave F

Snapshot verified on 2026-10-03, before creating the documentation transition
branch. These are recorded observations, not permanently current state.

| Item | Verified state |
| --- | --- |
| Repository | `luigiverona/ops` |
| Starting branch | `main` |
| Current main / origin/main at transition | `9cd605296162a7ebb4f3a563b3516da6fa404c34` |
| Wave F reviewed feature head | `ed8704624dc7ebee06f2a793fad2b099b7e80823` |
| Wave F reviewed tree | `4743785a0f6b2170d85ec354968a0e4bb1285e68` |
| Wave F squash merge commit | `9cd605296162a7ebb4f3a563b3516da6fa404c34` |
| Squash merge sole parent | `dda0bdf6ea363a290ee8c18837f35b0e94e9a1d5` |
| Merged main tree | `4743785a0f6b2170d85ec354968a0e4bb1285e68`; **EXACT** reviewed-tree equivalence |
| Wave F PR | [#24 — fix: validate shell scripts individually](https://github.com/luigiverona/ops/pull/24), merged |
| Wave F result | **O-01 CLOSED; Critical 0; Important 0; Optional 0** within Wave F review scope |
| Transition status | **Wave F merged and verified** |
| Remote Wave F branch | `fix/shell-validation-correctness` deleted |
| Remote long-lived branches | `main` only |
| Open PRs at preflight | 0 |
| Worktree / index / stashes at preflight | Clean / clean / 0 |
| Required protected checks | `ci`, `build`, `minimal-runtime`; all successful on the reviewed head and merged main |

Reviewed-head evidence: [CI run 36928579597](https://github.com/luigiverona/ops/actions/runs/36928579597)
and [Arch integration run 36928579423](https://github.com/luigiverona/ops/actions/runs/36928579423).
Push-triggered merged-main evidence on the exact transition commit:
[CI run 36933489473](https://github.com/luigiverona/ops/actions/runs/36933489473)
(`ci`: SUCCESS) and
[Arch integration run 36933489465](https://github.com/luigiverona/ops/actions/runs/36933489465)
(`build`, `minimal-runtime`: SUCCESS). Successful CI was verified, not rerun.
Branch protection also verified these three required checks with strict mode.
These runs are historical Wave F evidence, not future RC acceptance evidence.
Creating this handoff branch/PR temporarily changes the branch and PR counts;
the table describes the preflight, not the eventual documentation PR state.

The next implementation wave is **Wave G — Signal/interruption & recovery safety**,
covering **I-04**, which has **NOT started** during this documentation-only
transition. Wave F is complete; Waves G–S below are future work, not completed
acceptance claims. Zero known Critical findings remain at this transition;
three known Important findings remain outside the completed Wave F scope.

## Completed waves and finding accounting

The completed finding ledger is:

| Completed wave | Closed findings |
| --- | --- |
| A | I-01, I-10 |
| B | I-03, I-05 |
| C | I-06, I-09 |
| D | I-07, I-08 |
| E | I-02 |
| F | O-01 (Optional) |

Remaining known Important findings:

| Finding | Unresolved subject | Assigned wave |
| --- | --- | --- |
| I-04 | Shell signal/interruption recovery safety | G |
| I-11 | Diagnostic privacy scanner CPU/runtime cost | I |
| I-12 | Subprocesses bypass ops presentation boundary | H |

Known remaining Optionals are unresolved. Assignment to a wave is not a fix,
rejection, or deferral; no disposition or residual risk is invented here.

| Finding | Unresolved subject | Assigned wave |
| --- | --- | --- |
| O-02 | Stress-test split | J |
| O-03 | Updater verified bytes mutable path | M |
| O-04 | AUR untracked helper | P |
| O-05 | Release integer overflow | M |
| O-06 | Installer downgrade behavior | M |
| O-07 | Test harness architecture | J |
| O-08 | Dead APIs | N |
| O-09 | Resource determinism | I |
| O-10 | CI action refs | J |
| O-11 | Maintainability/docs | N |
| O-12 | Git retry UX | N |

### Evidence and historical status boundaries

- [Current architecture](architecture.md), [configuration](configuration.md),
  [workstation security](workstation-security.md), and
  [package-source provenance](package-source-provenance.md) describe current
  implementation contracts; future requirements below are not claims that those
  contracts have already been implemented.
- [Wave D final independent review](wave-d-final-independent-review.md) retains
  earlier findings and subsequent closure evidence.
- [Wave E blocker](wave-e-process-ownership-blocker.md) is preserved historical
  evidence from before implementation, not an outstanding I-02 disposition.
- [Wave E architecture](wave-e-process-ownership-architecture.md) contains
  preserved pre-implementation decisions followed by the production
  implementation section. Its earlier authorization/status text is historical.
- [Wave E validation](wave-e-evidence/implementation/validation.md),
  [security review](wave-e-evidence/implementation/security-review.md), and
  [correction evidence](wave-e-evidence/implementation/) describe their respective
  checkpoints. Statements such as “not pushed,” “independent review required,”
  or “complete race suite not run locally” must be read in that historical scope.
  Wave E merged in [PR #22](https://github.com/luigiverona/ops/pull/22);
  its historical merged-main evidence is
  [CI](https://github.com/luigiverona/ops/actions/runs/36873175413) and
  [Arch integration](https://github.com/luigiverona/ops/actions/runs/36873175491).
- [Wave F shell validation](wave-f-shell-validation.md) is completed historical
  implementation/review evidence. Its pre-merge statements that O-01 is “not
  closed” and review/CI/merge remain required describe that earlier checkpoint.
  The verified transition above owns current status: **O-01 CLOSED; Wave F
  merged and verified**. Neither that document nor Wave F protected CI replaces
  future RC acceptance evidence.

Wave E owns inherited-process lifetime, not the final terminal presentation
contract. Existing hard-crash/SIGKILL, uninterruptible-task, deliberate cgroup
manipulation, and manager-created-service boundaries remain documented in the
architecture evidence. Do not treat I-02 closure as proof that Waves G, H, K,
or L are complete or that final release gates have passed.

## Canonical remaining roadmap: Waves F–S

This ordering is authoritative. Do not skip, silently reorder, or replace it
with a simplified “productization / release” roadmap. “Closes” below identifies
the target finding; closure requires evidence and an explicit ledger update.
Correctness/security findings may reopen earlier work at any time.
Wave F is retained below as completed history; Wave G is next.

### Wave F — Shell validation correctness

**COMPLETE — O-01 CLOSED.** Shell `sh -n` multi-file validation bug;
[PR #24](https://github.com/luigiverona/ops/pull/24) merged and verified.

### Wave G — Signal/interruption & recovery safety

**CURRENT — local implementation prepared; I-04 remains OPEN.**

Wave G began on 2026-10-03 from verified main/origin/main
`acc3b28912a3d4a99340e91fe2981c70faaa8fbf` (the Wave F closure-documentation
commit). At preflight: main only remotely, zero open PRs/stashes, clean
worktree/index, and strict required `ci`, `build`, `minimal-runtime` checks
successful on that exact SHA (runs 37157785747 and 37157785721).

The local branch is `fix/signal-interruption-recovery`. Its
[implementation evidence](wave-g-interruption-recovery.md) records the full trap
inventory, pre-fix real-signal reproductions, backup state machine, configuration
publication boundary, 102 real-signal cases, validation, and self-review.
This is an unpushed implementation handoff, not formal finding closure or a
merged transition. Independent review, protected branch CI, squash merge,
reviewed/merged-tree equality, and canonical closure remain required.
Wave H has not started and must wait for Wave G closure.

Closes **I-04**: shell signal/interruption recovery safety.

### Wave H — Terminal ownership / subprocess presentation

Closes **I-12**. Core invariant:

> Only ops owns terminal presentation.
> Child processes produce data, not UI.

Required architecture:

```text
child process
    |
    +-- raw stdout/stderr -> bounded diagnostic transcript
    |
    +-- exit/state -> ops execution events -> terminal renderer
```

No default raw passthrough. No uncontrolled terminal escape/control leakage.
No unbounded transcript memory. Non-TTY output must remain stable/plain.

### Wave I — Diagnostic/runtime performance & resource bounds

Closes **I-11 and O-09**. Address diagnostic privacy scanner CPU/runtime cost
and resource determinism while preserving diagnostic privacy.

### Wave J — Test/CI architecture

Closes **O-02, O-07, O-10**: stress-test split, test harness architecture, and
CI action refs.

### Wave K — Platform & configuration compatibility contract

Explicitly define systemd expectations, cgroup v2 requirements, the supported
Arch environment, runtime/platform assumptions, and configuration
compatibility/support policy.

### Wave L — Crash, partial failure & convergence

Test and document SIGKILL, hard crash, partial package/update state, rerun
convergence, and recovery behavior. Preserve the distinction between observed
cleanup guarantees and documented crash limitations.

### Wave M — Install/update/upgrade compatibility

Closes **O-03, O-05, O-06**. Must cover fresh installation, the current
installer, self-update, **v2.1.0 -> candidate upgrade**, downgrade policy,
release-version parsing/bounds, and verified updater bytes.

### Wave N — Maintainability & product documentation

Closes **O-08, O-11, O-12**: dead APIs, maintainability/docs, and Git retry UX.

### Wave O — Stable Go & dependency refresh

Verify the current latest stable Go patch **at execution time**. Use stable
only: no beta, RC, or tip. Review dependencies and security, and pin the exact
stable patch for release.

The historical development baseline before Wave O is **Go 1.26.7**. This is
not a claim that 1.26.7 will still be latest when Wave O executes. Existing
toolchain pins remain unchanged by this documentation transition.

### Wave P — Final whole-repository security/adversarial audit

Explicitly resolve **O-04** (AUR untracked helper). Search for new Critical,
Important, and security findings across the whole repository. Reopen previous
waves if evidence requires it; prior closure is not an audit exemption.

### Wave Q — Release-candidate acceptance matrix

Validate the complete functionality/failure/idempotence matrix, including all
applicable final gates below. Record the exact candidate and evidence.

### Wave R — Supply chain & production release

Build, sign, checksum, stage, independently verify, and publish. Follow the
[release security procedure](release-security.md), including its precise
manifest/signature ordering and independent verification requirements.

### Wave S — Post-release closure

Verify public clean installation and public **v2.1.0 upgrade**, perform final
verification and repository cleanup, and record release closure. Public-path
checks must pass before declaring the release complete.

## Final definition of done and release gates

Before the next stable release, the required acceptance bar is all of the
following. Nothing in this handoff marks any future candidate gate as passed;
Wave S retains the explicit post-publication verification and closure work.

- Zero known Critical findings.
- Zero known Important findings.
- Every Optional fixed/rejected/deferred with explicit rationale and residual risk.
- Required protected CI green.
- Full native Arch ordinary suite green.
- Full native Arch race suite green.
- Fresh Arch install green.
- v2.1.0 -> candidate upgrade green.
- Repeated idempotent runs green.
- Ctrl-C/SIGTERM green.
- Partial-failure recovery green.
- Crash/SIGKILL policy tested/documented.
- TTY/non-TTY contracts green.
- Child processes no longer own presentation.
- Runtime/resource limits acceptable.
- Diagnostic privacy preserved.
- Final security audit green.
- Platform/support contract explicit.
- Exact stable Go/dependencies verified.
- Signed staged artifacts independently verified.
- Public clean install green.
- Public update/upgrade green.
- Tag/commit/public artifacts coherent.
- Only explicitly documented residual limitations remain.

Record passed, failed, or not run for each gate with the exact candidate,
environment, evidence, and limitations. A procedure, old successful run, or
documentation-only change is not new candidate acceptance evidence.

## Desired final repository shape

- `main` only; open PRs 0; feature branches 0.
- Clean working tree; stashes 0; untracked project files 0.
- Release candidate fully merged.
- One canonical stable tag/version for the release.
- One canonical signed artifact set for the release.
- No temporary proof/dev debris.

Historical merged PRs remain as audit/history. Preserve historical release
records and retained review evidence; they are not temporary debris.
The commit policy is **one squash commit per coherent Wave/PR**. “One commit”
does not mean rewriting the repository into one root commit. Any history rewrite
requires a separate explicit decision later.

## Historical v2.1.0 release baseline

This is the upgrade/release comparison baseline, not the next candidate.
The local tag object and peeled commit were checked during this transition;
the supplied production hash and endpoints are recorded historical provenance,
not a claim of a fresh public download/signature verification in this session.

| Item | Historical value |
| --- | --- |
| Squash commit | `79bebce75eed4d57d62f0dc1392e5c25c4ace417` |
| Tag object | `ab365ac89913d899c49558b9a4ebc2d84cc086c8` |
| Production binary SHA256 | `c894dec6e3a05f92affa5e514fd16d8c35f1881c6e753e736b37b2ccb05dcfd4` |
| Release | [v2.1.0 production artifacts](https://ops.luigiverona.dev/releases/2.1.0/) |
| Installer | [Public installer](https://ops.luigiverona.dev/install) |

No GitHub Release object by design unless that design changes deliberately.

## CI architecture baseline

The authoritative native Arch CI architecture at the Wave E transition is:

- `ubuntu-24.04` host for the native VM job.
- `/dev/kvm` mandatory; no TCG fallback.
- Checksum-pinned official Arch cloud image and disposable overlay.
- Normal UID/GID 1000 test user.
- Temporary bootstrap privilege, including privileged ownership regression
  coverage; privilege revoked before normal validation.
- Native package/Flatpak behavior, ordinary suite, and full race suite.
- Cleanup proof and bounded failure evidence.

Required protected checks: **`ci`, `build`, `minimal-runtime`**.
Sources: [native VM workflow](../.github/workflows/ci.yml),
[guest validation](../script/test-ci-arch.sh), and
[build/minimal-runtime workflow](../.github/workflows/arch.yml).
The latter uses a separate minimal Arch container and does not replace the
native VM acceptance architecture.

Current cloud-image baseline: official Arch arch-boxes release
`v20260915.594445`, image `Arch-Linux-x86_64-cloudimg-20260915.594445.qcow2`,
SHA256 `d7cc7c86a21b32d6678c001464714f71f4ef7e0d7bbbfca65e99123ac5afc25b`.
This is a current reproducible baseline, **not a permanent requirement to freeze
that Arch image release forever**. Future updates require reviewed provenance,
an exact checksum pin, and fresh validation.

## Local acceptance VM baseline

The established local acceptance baseline is recorded below for future use.
These machine details were supplied for this handoff; this documentation task
does not claim a fresh live VM inspection or acceptance run. Verify them before
use with the [VM acceptance procedure](vm-acceptance.md).

| Item | Baseline |
| --- | --- |
| URI | `qemu:///system` |
| VM | `opsinitialstate` |
| Snapshot | `pre-ops` |
| Expected VM/snapshot state | Shut off; current snapshot; no parent; no descendants |
| MAC | `52:54:00:6b:1c:13` |
| Disk | `/var/lib/libvirt/images/opsinitialstate.qcow2` |
| Guest user | `ops01` |
| SSH key | `~/.ssh/ops-vm-test` |
| Guest key fingerprint | `SHA256:a9Ca67c6/jRfVM3l8mi3ypbn8bQOI9xj+ml1rATrlmo` |
| Typical guest IP | `192.168.122.84`; rediscover and verify, do not assume |

**Never start the baseline concurrently with a candidate clone because the MAC
is shared.** Verify actual clone networking before boot; clone tooling can
generate a different MAC, which does not relax the baseline preservation rule.
Keep the preserved baseline shut off during candidate acceptance. Do not
delete/redefine its snapshot, flatten its disk, or install candidate prerequisites
into the baseline. Run acceptance on a disposable candidate clone.

## Session handoff protocol

At the start of every major ChatGPT/Codex session:

1. Read this file completely.
2. Verify live branch, HEAD, origin/main, worktree/index, stashes, open PRs, and
   required CI state. Fetch/prune before comparing remote state; verify the
   protected check requirements and their results for the exact relevant commit.
3. Do not trust SHA/status text in this file without live verification. Explain
   discrepancies and reconcile the evidence before acting.
4. Identify the exact current Wave. At this transition the next implementation
   wave is G (I-04); this documentation PR does not start it.
5. Do not skip Waves.
6. Do not silently simplify or reorder the roadmap.
7. Update this file after each major merged transition: verified main/reviewed
   head/tree, PR and CI evidence, finding accounting, next wave, and remaining
   release gates. Preserve the distinction between historical baselines,
   current observations, completed work, and future obligations.
8. Correctness/security findings may reopen earlier work. Record new findings
   and revised evidence explicitly; correctness overrides roadmap closure.

Keep this as the single canonical roadmap. `AGENTS.md` and README link here;
they must not acquire competing copies of the roadmap. This transition is
documentation only: no Wave G implementation, automatic merge, production
change, release, or publication is part of it.
