# Wave D independent review, resumed 2026-09-14

## Reconstructed checkpoint

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
