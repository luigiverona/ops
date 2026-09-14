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
