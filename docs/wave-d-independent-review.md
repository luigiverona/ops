# Wave D independent review, resumed 2026-09-14

## Second resume checkpoint (2026-09-14)

The second resume found state C, a clean `fix/package-source-provenance` at
`ae53c6c738508e2dd8fcde64b735c2e6dfa0ed0e`, with no stashes. All prior work was
committed and preserved. The three surviving review commits are `b8a7476`,
`01ae9bd`, and `ae53c6c`; their production changes and tests were inspected.
Fetch again left origin/main at `2fc8614171d8c335ade870d045da4e73f44b79bd`.
The original implementation tree is still `dd5b5db76bac4d2600900c16571ec461958e4604`.
Both original failing native suites were rerun with Go 1.26.7 and reproduced
D-R1, D-R2, D-R4, D-R5 and D-R6. The earlier report below is preserved as a
historical checkpoint; second-resume dispositions supersede its status totals.

### D-R6 second-resume correction

`Resolver.OfficialDependency` now walks each selected package's official
`Depends On` metadata, resolves each requirement through pacman, and includes
satisfied transitive providers in the retained qualified closure. Each satisfied
edge uses the same installed predicate as direct dependencies. Missing dependency
metadata, repository changes, conflicting package identities and incomplete
installed closures fail closed. A work queue handles cycles without recursion.
Exact unversioned targets take precedence over virtual provides in their pulled
transaction, matching pacman's named-target rule (needed for ca-certificates).

The existing `TestReviewSatisfiedTransitiveCustomDependency` failed before this
correction and passes afterward. Added real-libalpm coverage exercises official,
custom, foreign, versioned virtual, cyclic, missing and moved-repository children.
Parser regressions reject missing, duplicate, malformed and changed closure
metadata. The real resolver suite passes against the existing Arch sync databases,
including cargo, base-devel and java-runtime>=26 (78 seconds locally).
The forged-content predicate remains D-R1; the closure correction does not
claim to authenticate its members. D-R6 is corrected, conditional on that shared
predicate being repaired before release.

## First resume checkpoint

State A: clean `fix/package-source-provenance`, no stashes or corrective
commits. HEAD was `64bb2328202864d6859e5a7745161ae2b7de064f`, tree
`dd5b5db76bac4d2600900c16571ec461958e4604`. Fetch left `origin/main` at
`2fc8614171d8c335ade870d045da4e73f44b79bd`. There was no uncommitted work to
recover. Go is `/home/ah/.local/opt/go1.26.7/bin/go`, with `GOENV=off` and
`GOTOOLCHAIN=local`. Review evidence is being recorded before corrections.

## Findings recorded before corrective work

### D-R1 — Important: copied package metadata is accepted as source identity

Location: `internal/archrepo/query.go`, `InstalledMatch`, and every consumer of
`OfficialMatches` or `InstalledMatch`.

Reproducer: synthesize custom and extra package archives with the same name,
version, architecture, build date and packager, different payloads and SHA256
digests; place the custom package's metadata/files in an isolated libalpm local
database. `InstalledMatch` accepts it. This admits pacman declarations, core
prerequisites and satisfied AUR providers. The comparison does not authenticate
the installed source. The original documentation explicitly acknowledges that
the tuple is copyable; that limitation defeats this review's source-specific
reconciliation invariant.

Smallest reliable correction must add evidence outside the copied tuple:
authenticate the official archive/manifest and verify installed content against
it, or require a qualified reinstall without `--needed` for ambiguous state and
retain verifiable evidence of that transaction for subsequent inspection. A
version, packager, validation-method flag, or unauthenticated local mtree is not
such evidence. A bare checksum from a spoofed sync repository is insufficient.
Disposition pending adversarial evidence and investigation of practical identity
primitives.

### D-R2 — Important: Flathub subset restriction is invisible to inspection

Location: `internal/flatpak/flatpak.go`, `Manager.Remotes`, `ParseRemotes`,
`Remote.Canonical`.

Reproducer: an isolated OSTree user repository config with canonical URL,
`gpg-verify=true`, and `xa.subset=verified`. Flatpak 1.18.2 reports `options: ""`
for Wave D's requested columns. The remote is considered ready although its
available source is restricted. Requesting `subset` explicitly reveals
`subset: "verified"`. No remote creation, GPG import or app install is needed
for this reproducer.

Smallest correction: include the subset column in every remote inspection,
require an unrestricted remote, preserve strict JSON validation, and check it
before and after correction/install as well as in Doctor and final inspection.
Do not reject presentation metadata such as remote title or comment.

Further real-CLI evidence: `gpg-verify-summary=false` also produces an empty
options field and passes readiness. Flatpak's summary-index fetcher consults
this flag before verifying summary signatures. Adding only `subset` is not a
complete fix: absent subsets and a literal `xa.subset=-` are both printed as
`"-"`. A reliable correction needs the underlying trust/source configuration,
read through a genuinely read-only mechanism, rather than treating formatted
columns as a complete trust inventory. Disposition: unresolved.

### D-R3 — Important: official-only upgrade omits configured custom updates

Location: `internal/arch/manager.go`, `FullUpgrade`, and
`internal/arch/provenance.go`, `runOfficial`.

Reproducer: installed custom client 1 depends on an unversioned official library;
the refreshed official repository has library 2 with a changed SONAME, while
the configured custom repository has rebuilt client 2. Filtering that repository
out of `-Syu` upgrades the library and omits the available rebuilt client. Pacman
cannot catch an ABI dependency absent from declared dependency metadata.

Smallest correction: use the user's complete configured repositories for the
general interactive system upgrade; continue qualifying managed official targets
and restricting their subsequent installations. Disclose the scope accurately
before approval. Alternatively refuse automated system upgrades on this mixed
repository configuration. An ordinary upgrade cannot guarantee that every AUR
or custom binary has been rebuilt, but it must not deliberately omit an available
configured rebuild.

Disposition: corrected in `FullUpgrade`; it validates the expanded configuration
but invokes the full interactive upgrade with the original configured repositories.
The pre-approval disclosure and source-contract documentation were updated.
`TestReviewFullUpgradeIncludesConfiguredCustomRepositories` failed before the
change and passes afterward. The real libalpm fixture independently demonstrates
the omitted rebuild.

A separate isolated ELF probe compiled a client against `libops.so.1`, then
left only `libops.so.2` available. The old client exited 127 with a missing
`libops.so.1` loader error; the rebuilt client linked to `.so.2` exited 0.
Together with the real libalpm upgrade-selection probe this confirms the
concrete ABI failure, rather than inferring a defect solely from repository
configuration differences.

### D-R4 — Important: official repository section names are trust anchors

Location: `internal/archrepo/identity.go`, `Official`, and
`internal/archrepo/config.go`, `OfficialConfig`.

Reproducer: `[extra]` with `Server = file:///custom` and `SigLevel = Never` is
retained as official. The unchanged official-only fast path then runs privileged
pacman with that configuration. Source qualification proves a configured label,
not that its contents are official Arch packages.

Smallest reliable correction: authenticate package identities against an
independent official Arch trust anchor, including mutation-time verification;
alternatively validate an explicit supported official mirror/signature policy
and fail closed on configurations whose source cannot be established. Root-owned
configuration is an administrative trust assumption, not evidence that a custom
section has official content. This is the same missing authentication boundary
that stronger installed-package verification must address.

### D-R5 — Important: Doctor's Flatpak inspection can write configuration

Location: `internal/flatpak/flatpak.go`, `Manager.Remotes` / `Applications`,
reached from `internal/inspect/inspect.go`, `Workstation.Local`, and Doctor.

Reproducer: create an isolated valid OSTree user repository without the newer
`core.min-free-space-size` setting, then call `Manager.Remotes`. Flatpak 1.18.2
rewrites `repo/config`, adding `min-free-space-size=500MB`. No modifying Flatpak
subcommand is issued. `_flatpak_dir_ensure_repo` in upstream Flatpak explains
this behavior; it can also initialize missing repositories. The mock Doctor
tests check command names, so they cannot detect these native side effects.

Violated invariant: Doctor must not modify Flatpak state. Smallest reliable
correction: inspect a private metadata snapshot or use a genuinely read-only
native/filesystem interface; do not run these CLI queries on the actual user
installation under a claim of strict read-only behavior. Merely restoring the
configuration afterward is not read-only and loses concurrent changes.
Disposition: unresolved; regression is deliberately failing.

### D-R6 — Important: satisfied transitive build dependencies escape inspection

Location: `internal/resolve/resolve.go`, `OfficialDependency`.

Reproducer: official installed `ops-builder` depends on `ops-compiler`; a
custom `ops-compiler` of the required version is already installed. Its packager
and payload differ; no forged metadata is needed. `pacman -T ops-builder`
succeeds. `-Sp ops-builder` prints only `extra/ops-builder`, omitting the
satisfied custom compiler. The binding is accepted with `Satisfied=true` and
only the builder in `Packages`. The same omission applies to an installed
`base-devel` metapackage and its compiler/build-tool dependencies.

Violated invariant: satisfied installed state must not silently admit custom
providers into the approved official AUR build environment. Smallest reliable
correction: materialize and verify the installed dependency/provider closure,
including satisfiers omitted by pacman's transaction printer, retaining exact
identities during planning, pre-build checks and final verification. Removing
`--needed` only forces explicit print targets; it does not include these omitted
dependencies. Disposition: unresolved; real-libalpm regression is failing.

### D-R7 — Important: final inspection drops approved AUR provider bindings

Location: `internal/app/lifecycle.go`, `preparePlan`.

Reproducer: an AUR application passes its pre-build provider check, then a later
approved application changes that provider/repository. Final inspection calls
`Workstation.State` and `plan.Build` with no facts; installed foreign/AUR apps
are ready without rechecking the original `AURDependencies`. It can report a
healthy final workstation despite the changed binding.

Smallest correction: retain the approved bindings through final inspection and
rerun the existing dependency/repository revalidation for planned AUR apps that
are now installed. Require satisfaction and reject provider, repository and
transaction additions; verify omitted members using the same existing predicate.
This fixes the weaker final path, while D-R1/D-R6 still block the shared predicate.

Disposition: corrected. `preparePlan` revalidates the retained bindings after
local reinspection and before reporting final readiness. The end-to-end fake
build regression reports false success on the original path for provider
repository drift, an added transaction member, and lost dependency satisfaction.
After correction all three fail closed, and an unchanged binding still succeeds.

## Installed identity investigation

The forged archives used by `TestReviewRejectsForgedCustomPackage` have SHA256:

- custom: `5378cedd1b58272e6a7f550fc891612330397726faf0beb4e74c8011378ce683`
- official fixture: `283af437b18fb352ae4e666657bc6069f81f6f355a0168760a1b48da20edc854`

The local state is synthesized from the custom archive's metadata/payload rather
than installed with privileged pacman. Real libalpm reads those isolated local
and sync databases. Thus this proves the classification and resolver defects;
it does not claim an actual root transaction occurred during the review.

Pacman's local database stores a validation *method*, not the original package
archive digest, signature identity or historical repository. `be_local.c` writes
`%VALIDATION%` flags (`none`, `md5`, `sha256`, `pgp`); it does not persist an
authenticated archive identity for this comparison. Adding that flag or more
builder-controlled fields cannot repair D-R1. `pacman -Qkk` compares files
against the local mtree, which can itself come from the custom package. It is
useful only after binding that manifest to independently authenticated official
content. A cached official archive alone does not prove what was installed.

Pacman's `%h` and `%g` expose the sync archive SHA256 and signature. They are
useful inputs, but must be authenticated as official independently of the
repository label. A complete offline verifier must bind an authenticated
archive/manifest to the installed file inventory and content, define treatment
of configurable backup files and generated files, and fail closed when proof is
absent. An alternative is a forced qualified reinstall followed by durable,
tamper-resistant evidence invalidated by later package replacement. Reinstalling
on every inspection breaks Doctor and convergence; recording only the copied
metadata tuple recreates the original defect.

No incomplete authentication substitute has been introduced. D-R1 and D-R4
remain release blockers: this session establishes the attacks and required
boundary but does not deliver the missing authenticated-content/transaction
evidence implementation. D-R2/D-R5 require native trust configuration inspection
without Flatpak's initialization/migration side effects. D-R6 needs the installed
provider closure, not just a printed mutation transaction. These corrections
remain unfinished Wave D work; they are not waived or classified Optional.

## Source evidence

- [Pacman CLI](https://pacman.archlinux.page/pacman.8.html): repository-qualified
  targets, `-T`, `--needed`, `%h`, `%g`, print transactions and file checks.
- [Pacman local database implementation](https://gitlab.archlinux.org/pacman/pacman/-/blob/v7.1.0/lib/libalpm/be_local.c):
  persisted fields and validation-method flags.
- [Pacman file-check implementation](https://gitlab.archlinux.org/pacman/pacman/-/blob/v7.1.0/src/pacman/check.c):
  mtree comparison, file SHA256 checks and backup-file exceptions.
- [Pacman configuration](https://pacman.archlinux.page/pacman.conf.5.html):
  repository sections, Includes, signature policies and paths.
- [Arch system maintenance](https://wiki.archlinux.org/title/System_maintenance#Partial_upgrades_are_unsupported):
  upgrade consistency and separately maintained unofficial packages.
- [Flatpak remote-list implementation, 1.18.2](https://github.com/flatpak/flatpak/blob/1.18.2/app/flatpak-builtins-remote-list.c):
  reported options, subset/collection columns, and missing-value placeholders.
- [Flatpak directory implementation, 1.18.2](https://github.com/flatpak/flatpak/blob/1.18.2/common/flatpak-dir.c):
  `_flatpak_dir_ensure_repo`, `flatpak_dir_get_remote_subset`, and
  `flatpak_dir_remote_fetch_summary_index` establish inspection side effects and
  the independent summary-signature flag.

## Remaining scope assessment

- Official sync targets retain `repo/name` through plan packages, build packages,
  provider bindings, transaction parsing and installation. Unknown repositories,
  duplicate concrete names, malformed/missing identities and missing explicit
  install targets fail closed. Ordering is canonicalized. Bare names remain at
  local query and install-reason operations, where they identify local packages;
  they are not used as official sync installation targets.
- Core targets are `extra/git`, `core/openssh`, `extra/github-cli`, and conditional
  `extra/flatpak`. Mutation and verification use these identities; the shared
  metadata predicate still admits the forged package. Repository moves may need
  maintenance. No new exception was added to accept a custom repository.
- Pacman's equal-version `--needed` skip was independently reproduced. Official
  `-S` mutation and print paths already omit it, including core and AUR official
  dependency installations. They can select an equal-version qualified reinstall.
  The unresolved forged case prevents repair from being planned in the first
  place. No real privileged reinstall was executed to claim installed-byte proof.
- Direct official dependencies, virtual official providers, versioned official
  providers and multiple installed possible providers pass the real fixture
  matrix. Custom-only and foreign-only installed virtual satisfiers are rejected
  even when `-T` succeeds. Copied provider metadata bypasses these checks (D-R1),
  and satisfied transitive custom dependencies are omitted (D-R6).
- Pre-build revalidation rejects changed providers/repositories and transaction
  additions. Shrinkage verifies omitted planned members. Final reinspection now
  repeats these checks for retained approved AUR bindings (D-R7 corrected). It
  does not reconstruct historical build plans for previously ready AUR apps.
- Install-reason behavior remains explicit: ordinary implicit dependencies stay
  implicit; AUR concrete build packages use `--asdeps`; planned explicit intent
  is restored and checked; declared official apps are marked explicit only after
  the source predicate. Existing focused reason-preservation regressions pass.
- Protected configuration round-trips through real `pacman-conf` with `RootDir`,
  `DBPath`, `GPGDir`, `Architecture`, `SigLevel`, `LocalFileSigLevel`, and
  `RemoteFileSigLevel` unchanged. Include expansion preserves resolved Server
  values. Commented multilib stays absent. Duplicate sections remain visible in
  real expansion and are rejected by the filter. Protected staging checks root
  ownership, file type, permissions, links and validated directory names; cleanup
  is attempted on copy, verification and transaction failure. Those protections
  do not authenticate the original configuration, its mirrors or sync content
  (D-R4), and separate CLI processes do not lock out concurrent root changes.
- Flatpak JSON parsing correctly distinguishes empty application output from an
  empty remote array, and rejects null, non-string values, missing/duplicate/
  unknown keys, malformed/trailing JSON, duplicate remote names and duplicate
  application IDs. Actual selected-column JSON keys match Flatpak 1.18.2.
  Schema strictness does not establish completeness of trust options (D-R2).
- Reported `oci`, `no-enumerate`, `no-gpg-verify`, `filtered`, and disabled
  options prevent readiness. Title/comment presentation metadata is harmless
  and does not block an otherwise canonical fixture. Subsets and summary trust
  are unresolved. Stored origin is checked, not historical Flatpak byte origin.
- Wrong-origin Flatpaks become failures in inspection/planning/Doctor and are not
  silently treated as missing. Install refuses migration and verifies origin
  afterward. The original final-origin/URL/disabled-drift regressions pass.
- Missing remote creation and disabled canonical remote enablement are exposed
  before `Continue?`. Decline does not mutate. A changed existing namesake or
  wrong URL is rejected before correction, and reported postconditions are
  checked afterward. There is no `--if-not-exists` preservation or destructive
  app reinstall. The CLI check/mutate window is not atomic against another
  same-user process; hidden source options still bypass these checks (D-R2).
- Doctor calls no new pacman mutation, synchronization, sudo staging, app install,
  remote repair or GPG mutation explicitly. Its use of real Flatpak inspection
  nevertheless violates strict read-only behavior on legacy state (D-R5).
- `archrepo` has a coherent repository-policy/identity-query responsibility and
  one official repository allowlist. Qualified-string boundaries are validated.
  The architectural blockers are missing authenticated evidence and confusing
  transaction membership with an installed dependency closure, already counted
  in D-R1/D-R4/D-R6; file count itself is not a finding.

Optional improvements: centralize a typed qualified target to reduce repeated
`Repository + "/" + Name` construction; provide clearer recovery for a custom
repository taking priority in read-only transaction planning when the eventual
official-only installation would use different implicit providers; maintain
core target mappings when official repositories move packages. These are not
additional release blockers.

## Validation and disposition

Go 1.26.7 exactly, invoked by absolute path with `GOENV=off` and
`GOTOOLCHAIN=local`. `go env GOTOOLCHAIN GOENV` prints `local` and an empty GOENV
path (the environment-file mechanism is disabled).

| Check | Result |
| --- | --- |
| Initial seven focused suites, before review tests | All passed |
| `go mod verify` | Passed |
| `gofmt -l .` | Empty |
| `go test -count=1 ./internal/archrepo` | Failed: D-R1, D-R4, D-R6 regressions |
| `go test -count=1 ./internal/resolve` | Passed |
| `go test -count=1 ./internal/inspect` | Passed |
| `go test -count=1 ./internal/plan` | Passed |
| `go test -count=1 ./internal/arch` | Passed, including D-R3 correction |
| `go test -count=1 ./internal/flatpak` | Failed: D-R2 and D-R5 regressions |
| `go test -count=1 ./internal/app` | Passed, including D-R7 correction |
| All new adversarial tests | Executed; unresolved findings remain red |
| `go vet ./...` | Passed |
| `go build ./...` | Passed |
| `git diff --check` | Passed |
| Targeted race | Not run: zero-blocker prerequisite is not met |
| Full `go test -count=1 ./...` | Not run: ready gate is blocked |
| Full repository race | Deliberately not run, as requested |

The full non-race test suite also contains real detached-signature fixture
creation in `internal/release/release_test.go`; it was not invoked under the
no-signing instruction. No key generation, import or signing command was run by
this review. No test or native probe used the preserved VM or real package/remote
mutation. Full end-to-end installed-byte repair is unverified because no such
repair implementation exists yet; the native pacman probes use print/query modes.

The two corrective production changes have separate Conventional Commits.
Failing unresolved regressions are preserved explicitly, without skips or weakened
assertions. There are **zero proven Critical findings, seven Important findings,
two corrected and five unresolved**. This is a blocked independent review, not a
completed implementation or permission to open a PR.

No push, PR, merge, tag, release, publication, R2 access, VM mutation, signing,
Go/dependency upgrade or next-wave work occurred. The expected feature branch
and original implementation commit remain intact. Evidence logs and source probes
also remain under `/tmp/ops-wave-d-review`; durable findings and reproducers are
committed in this repository so another shutdown does not lose the review.

## Exact changed files

Baseline to final review tree: 50 files. This includes
all 44 original Wave D files and six files added by the independent review.

```text
README.md
docs/architecture.md
docs/configuration.md
docs/package-source-provenance.md
docs/wave-d-independent-review.md
docs/workstation-security.md
internal/app/applications.go
internal/app/aur_application_test.go
internal/app/aur_order_test.go
internal/app/diagnostic_test.go
internal/app/doctor.go
internal/app/doctor_test.go
internal/app/lifecycle.go
internal/app/lifecycle_test.go
internal/app/output.go
internal/app/output_test.go
internal/app/prepare.go
internal/app/prepare_test.go
internal/app/provenance_test.go
internal/app/review_test.go
internal/app/terminal_test.go
internal/arch/manager.go
internal/arch/manager_test.go
internal/arch/provenance.go
internal/arch/provenance_test.go
internal/arch/review_test.go
internal/archrepo/cli_test.go
internal/archrepo/config.go
internal/archrepo/config_test.go
internal/archrepo/identity.go
internal/archrepo/provenance_test.go
internal/archrepo/query.go
internal/archrepo/review_test.go
internal/flatpak/flatpak.go
internal/flatpak/provenance_test.go
internal/flatpak/review_test.go
internal/inspect/inspect.go
internal/inspect/inspect_test.go
internal/inspect/provenance_test.go
internal/plan/plan.go
internal/plan/plan_test.go
internal/plan/provenance_test.go
internal/resolve/applications.go
internal/resolve/minimal_test.go
internal/resolve/planning_test.go
internal/resolve/provenance_test.go
internal/resolve/resolve.go
internal/resolve/resolve_test.go
internal/testpkg/fixture.go
internal/testpkg/pacman.go
```
