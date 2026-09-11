# Architecture and dependency policy

## Lifecycle

```text
detect -> load -> local inspection -> external facts -> pure plan
                                                       |
                         final re-inspection <- apply <- confirm
```

`cmd/ops` dispatches commands and cancellation. `internal/app` owns the lifecycle:
`prepare.go` reads top-to-bottom through approved execution; `lifecycle.go`
checks convergence; `applications.go` owns package/service application;
`identity.go` coordinates Git/SSH/GitHub decisions; `doctor.go`, `update.go`, and
`output.go` keep diagnostics, signed updates, and presentation separate.
These are cohesive files in one orchestration package, not wrapper packages.

`config` owns strict source-qualified intent. `inspect.Workstation.Local` reads
the package database, Git identity, SSH files/agent, Flatpak inventory, required
services, and pacman configuration without network access. `External` performs
read-only authenticated account checks and authoritative host-key freshness
checks. Missing optional executables are not invoked just to discover absence.
Malformed pacman configuration and unsafe managed SSH paths fail closed.

`resolve.Applications` materializes facts only for missing exact declarations.
`plan.Build(Config, State, Facts)` performs no I/O. The planner owns action
classification, conditional capabilities, service policy, and deterministic
ordering; presentation does not decide whether work exists. Configuration
normalizes order to pacman, AUR, Flatpak, then exact identifier. Dependencies
are deduplicated and sorted. Application states distinguish unresolved intent,
temporarily unavailable facts, invalid facts, and executable actions.

Domain data flows into the planner; only orchestration and operation packages
depend on `run.Runner`. `arch`, `aur`, `flatpak`, `git`, `ssh`, `github`, `pgp`,
`sudo`, and `release` localize their security-sensitive operations. `aurmeta`
parses declarative build metadata without executing PKGBUILD. No new Go runtime
dependencies were added; TOML decoding remains the single external module.

## Canonical application order

1. Validate official Arch x86_64 and normal-user execution; validate configuration.
2. Inspect local state, obtain required external facts, and construct/present a plan.
3. Confirm the plan. Acquire and refresh sudo only if privileged work is required.
4. Prepare required repositories; perform one full interactive `pacman -Syu`
   before package installations. A no-op does not upgrade the system.
5. Install missing foundational official packages and verify their presence.
6. Configure user Flathub only for declared Flatpak applications.
7. Apply applications in source/identifier order. Before each AUR build, fetch
   the pinned commit, review tracked files, and revalidate source metadata and
   dependency providers/transaction. Only after review, import exact required
   PGP keys and install/verify its official build dependencies. Build as the
   normal user and install only validated, selected artifacts.
8. Verify each application and configure/verify its required service. Services
   are deliberately adjacent to their owner; later unrelated apps may continue
   if one application fails.
9. Configure Git, then SSH, then authenticate/reconcile GitHub keys when needed.
10. Re-inspect actual local and remote state and rebuild the plan. Remaining
    work is reported as incomplete, never assumed successful from child exits.

Flathub preparation precedes applications because it has only the verified
Flatpak prerequisite; AUR dependencies are delayed until their own source review
so declining a build does not install its compiler toolchain. No uninstall or
automatic orphan cleanup is performed. Core failures stop dependent execution;
after a partial fatal failure use doctor before retrying.

## Dependency audit

| Dependency | Previously | Now | Capability / reason |
| --- | --- | --- | --- |
| git | Always, but required during its own planning | Always, used only after installation for mutations | Intentionally managed Git identity and Git transport for approved AUR builds |
| openssh | Always | Always | Managed identity, effective configuration, GitHub SSH access |
| github-cli | Always | Always | Intentional GitHub device authentication and account key reconciliation |
| base-devel | Always through mandatory AUR bootstrap | Only when building a declared AUR package, or explicitly declared | makepkg's implicit build-tool baseline |
| paru | Mandatory pinned bootstrap | Only if explicitly declared as an AUR app | ops already resolves, reviews, builds, and installs AUR content itself |
| flatpak | Always | Only declared Flatpak capability or explicit package declaration | User-scoped Flatpak application installation |
| flathub | Always | Only declared Flatpak applications | Exact configured Flatpak source |
| compilers/build dependencies | Mandatory paru toolchain plus apps | Pinned AUR build's concrete official dependency transaction | Source-declared build/check/runtime requirements |
| optional dependencies | Selected automatically one level deep | Only explicit declarations | No objective universally required feature |

Core Git/SSH/GitHub capabilities remain intentionally always managed; users who
do not want that opinionated scope should not run ops. `sudo`, `pacman`,
`pacman-conf`, `vercmp`, CA trust, GnuPG, and standard base utilities belong to
the supported Arch baseline, not hidden installers. `makepkg` itself comes from
pacman; `base-devel` supplies the tools needed to build. AUR-only dependencies
that cannot be satisfied by installed packages or exact official providers are
reported rather than recursively built or guessed.

Official/AUR declarations are explicit packages. Automatically required build
packages retain dependency reasons; pre-existing explicit official packages and
declared official build dependencies stay explicit. Pacman manages ordinary
required dependencies. Removing an app declaration never removes packages.

## Root cause and regression boundaries

Previously `plan.Build` called `git ls-remote` to resolve mandatory paru while
Git was missing but merely queued for installation. Planning failed before the
queue could execute. Installing Git earlier behind the plan would hide the
cycle, not solve it.

There is no mandatory paru bootstrap now. For declared AUR work, bounded HTTPS
Git smart-protocol reference discovery obtains the exact HEAD object ID without
a local Git executable. The same authenticated AUR service supplies revision-
qualified `.SRCINFO`. Apply fetches exactly that object, verifies its ID, and
compares reviewed metadata/files before build; no ref moves are followed.
See the [Git smart HTTP protocol](https://git-scm.com/docs/http-protocol).

`plan/plan_test.go` covers capability matrices and pure no-ops;
`resolve/minimal_test.go` resolves AUR/Flatpak without missing executables;
`app/lifecycle_test.go` persists simulated mutations through genuine
re-inspection, doctor, and a no-op second run. Other operation/security tests
remain isolated at the executor boundary rather than pretending fake successful
commands are real final state.

## CI and errors

CI uses Go 1.26.7 for unit, race, and build checks. The Arch workflow transfers
unsigned binaries into a pinned official base image; checkout and Go stay on
the build runner. The runtime installs only baseline sudo, asserts managed
dependencies remain absent, runs native pacman/vercmp checks, runs doctor with
and without configuration, and declines a real interactive first-run plan.
It checks package/configuration state did not change. This is not full systemd,
device-authentication, or real AUR build acceptance; the VM gate remains required.

| Situation | Behavior |
| --- | --- |
| Missing plannable package/configuration | Missing/required status; doctor exits 1 without changing it |
| Exact declaration not found | Unresolved application; unrelated approved work may proceed |
| Remote transport/service unavailable | Unavailable diagnostic; retry without changing declarations |
| Malformed facts or source drift | No affected application build/install; report cause and rerun/review guidance |
| Authentication/scope missing | Explicit deferred login/refresh and key inspection, only after confirmation |
| Top-level decline | Exit 0; no changes |
| AUR review decline or failed application | Exit 1; retain successful unrelated work and report incomplete convergence |
| Unsafe state, invalid config/platform, failed core prerequisite | Exit 2; stop dependent work |

No-op with unavailable host-key freshness exits 1 and does not prompt or mutate.
Doctor never calls login, sudo, installation, or file replacement. Workstation
health means local managed configuration readiness: doctor checks the persisted
GitHub account name without requesting credentials, remote key registration,
host-key freshness, or live authentication. Setup still verifies remote state.
Missing application declarations may require read-only source queries; an
inconclusive query receives retry guidance, never a guessed absence.

Command inspection uses the C locale, EOF stdin, and bounded complete output.
Successful build logs stay captured; approved pacman installs stream native output
without acquiring stdin. Streaming does not relax capture limits for inspection;
only commands explicitly designated as logs may truncate captured output.
Opted-in command failures retain at most 16 KiB of recent evidence and display
at most 12 lines / 2 KiB, plus an explicit omission marker. Terminal controls
are escaped, potentially sensitive output is withheld, and live output is not
replayed. Credential and configuration dumps never opt in; the fixed BatchMode
SSH probe may report connection stderr. The privacy scan remembers markers
before tail truncation and withholds overlong ambiguous escape sequences.
Final inspection attaches observed state to earlier operation issues; inability
to inspect is reported separately and never guessed to mean missing state.

Presentation summarizes only planned work: a user-scoped Flatpak-only install
does not announce a system upgrade. The AUR source view is separate from normal
progress; its sanitized display is never used for metadata or equality checks.
`ops update` has one approval for download, verification, and installation;
signature/checksum verification still completes before sudo or replacement.
