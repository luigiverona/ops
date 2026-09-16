# Wave D D-R1/D-R4 recovery and correction

## Final resume, 2026-09-16

Recovered state B on `fix/package-source-provenance`, starting HEAD
`8dba1b634480a52faa140b6a26f28b55446e3bcd`: 36 modified tracked files and
20 untracked files (including nine files inside the archtrust directory).
Nothing staged, no stashes, and no commits after that checkpoint.
Fetch retained `origin/main` at
`2fc8614171d8c335ade870d045da4e73f44b79bd`. All interrupted implementation
and existing commits were preserved and reviewed; no reset, clean, restore,
amend, squash or rebase was used. The earlier completion statements below were
inherited uncommitted notes, not evidence that this resume had already passed.

The recovered implementation was retained. This resume corrected archive
stat/read errors being classified as a content mismatch: these errors now remain
inconclusive instead of planning repair. New native tests exercise distribution
key rotation and actual main-key certification revocation, including invalidating
an old cached archive after revocation. New archive tests cover failed stat and
read operations without treating unavailable evidence as a forged package.

Endpoint probes on 2026-09-16 again returned HTTPS 200 without redirects for all
three x86_64 databases, with sizes 130478 (core), 8883199 (extra), and 84681
(multilib), served by the Johannesburg backend. Ownership is corroborated by
[Arch's infrastructure inventory](https://github.com/archlinux/infrastructure/blob/main/docs/servers.md),
[official mirror status](https://archlinux.org/mirrors/geo.mirror.pkgbuild.com/),
and the endpoint's use in the
[official container mirror list](https://github.com/archlinux/archlinux-docker/blob/master/rootfs/etc/pacman.d/mirrorlist).
The public-network archive integration was rerun successfully for all five
packages listed below, with identical versions/digests and manifest counts.
Only disposable temporary archives were downloaded; none was installed.

The system default Go was observed to be 1.27.1 during recovery. All compilation,
formatting, module checks and tests in this resume use the existing
`/home/ah/.local/opt/go1.26.7/bin` tools with `GOENV=off` and `GOTOOLCHAIN=local`.
No toolchain or dependency was installed or upgraded.

### Fresh final validation and disposition

**Zero unresolved Critical findings; zero unresolved Important findings.**
D-R1 and D-R4 are corrected under the current-managed-content and trusted-platform
contract. D-R2 through D-R7 remain corrected. The complete baseline-to-current
change was reviewed before committing, including all official readiness/query/
mutation call sites, retained AUR bindings, Flatpak trust and isolation, Doctor,
install reasons and approval. No metadata-only success path or live user-sync
fallback remains in official authentication.

All commands below used Go 1.26.7, GOENV=off and GOTOOLCHAIN=local. Full/race
validation was repeated after the archive I/O classification correction.

| Check | Fresh result |
| --- | --- |
| `go version`; `go env GOTOOLCHAIN GOENV` | go1.26.7 linux/amd64; local; GOENV disabled (reported path empty) |
| `go mod verify` | All modules verified |
| `gofmt -l .` | Empty |
| Focused archrepo / arch / resolve / inspect / plan / app | All pass, each with `-count=1` |
| archtrust / testpkg / flatpak | Pass; testpkg builds with no test files |
| Original D-R1 forged-package regression | PASS; distinct original fixture digests preserved |
| Original D-R4 official-name-spoof regression | PASS |
| Original D-R2 summary verification / subset regressions | PASS, including verified, literal dash and unrestricted control |
| Native independent mount / equal-version install reasons | PASS, executed without skips; only disposable namespace/DB state changed |
| Targeted race, archtrust / archrepo / arch / resolve / inspect / plan / app / testpkg / flatpak | PASS; final resolve 86.801s, app 6.913s |
| Additional `-race -count=1 ./internal/run -run '^TestReadOnly'` | PASS, 1.030s |
| `go test -count=1 ./...` | PASS on final code; resolve 86.480s, run 40.679s |
| `go vet ./...`; `go build ./...` | PASS on final code |
| `git diff --check` and baseline diff check | PASS |
| Public official-archive integration | PASS, all five targets, 9.468s |
| Full repository race | Omitted because of known I-11 diagnostic stress cost; targeted race above passed |

The full suite exercised its existing disposable synthetic signature fixtures;
no actual release was signed, and no release keys/infrastructure were accessed.
No push, PR, merge, tag, release, publication, R2 access, preserved-VM mutation,
toolchain/dependency upgrade or Wave E action occurred. No privileged workstation
package transaction was executed. Optional observations remain typed-target
ergonomics, maintenance of core mappings/key policy/endpoint, and clearer recovery
for unsupported different-name custom providers. They are not waived Important
findings.

## Earlier recovery notes, 2026-09-15

State A: clean `fix/package-source-provenance`, HEAD
`8dba1b634480a52faa140b6a26f28b55446e3bcd`. No staged, unstaged, untracked,
stashed, or interrupted-session commits. All prior work remains preserved.
`git fetch origin` retained main at
`2fc8614171d8c335ade870d045da4e73f44b79bd`.
Go 1.26.7, `GOENV=off`, `GOTOOLCHAIN=local`; initial module verification and
formatting checks passed. No recovered implementation needed replacement.

## Root causes and selected model

D-R1 used a builder-controlled installed metadata tuple as readiness evidence.
The preserved real-libalpm fixture copied name, version, architecture, build date
and packager while changing archive digest and installed payload. The previous
predicate accepted it. D-R4 trusted `[core]`, `[extra]` and `[multilib]` section
names despite those sections being able to reference arbitrary custom databases.
Fixing either alone leaves the other attack intact.

The selected model is **stateless current managed-content equivalence** to an
independently selected, authenticated official archive. A signed archive cache
supplies evidence; it is not an installation receipt. Missing cache evidence or
content mismatch plans visible repair/reverification. Source/read/trust errors
are inconclusive. A matching pre-existing legitimate package is immediately
ready. An ambiguous installation is never silently labeled official.

Rejected alternatives:

- More installed metadata, local `.MTREE`, `-Qn`, validation flags and section
  names remain copyable or locally configurable.
- Signature acceptance in the user's pacman keyring includes custom keys.
- Plain gpgv treats all supplied keys as trusted and ignores expiry/revocation.
- Durable transaction receipts add state and invalidation problems without
  proving current payload or undoing prior install scripts. No receipt is used.
- A private writable DBPath with a symlink to the real local DB uses a different
  pacman lock. Mutations instead retain the normal DBPath and lock, using only a
  private read-only sync mount in a disposable namespace.

The exact contract, settings and exceptions are maintained in
[package source provenance](package-source-provenance.md).

## Independent official source investigation

Selected: `https://geo.mirror.pkgbuild.com/$repo/os/x86_64`.
[Arch's mirror documentation](https://wiki.archlinux.org/title/Mirrors) identifies
pkgbuild.com mirrors as operated by Arch DevOps. The
[official mirror status](https://archlinux.org/mirrors/geo.mirror.pkgbuild.com/)
lists the HTTPS geo endpoint and regional mirrors.

Independent HTTPS HEAD requests on 2026-09-15 returned 200 without redirects for
`core/os/x86_64/core.db`, `extra/os/x86_64/extra.db`, and
`multilib/os/x86_64/multilib.db`. Sizes were 130478, 8893339, and 84706 bytes.
Responses identified the Johannesburg backend and advertised HSTS. Production
uses normal TLS certificate/hostname verification and rejects every redirect,
including HTTPS redirects to another host. This deliberately trades alternative
mirror availability for an explicit independent source; outages are inconclusive.
The hostname has regional routing, while the URL layout supports all required
stable repositories and x86_64/any packages. No fallback to configured mirrors.

All three bounded databases are parsed before an immutable snapshot is published.
Its bytes determine existence, dependency/provider metadata, qualified transaction
members, filename, size, SHA256 and embedded PGP signature. Native queries use
this snapshot, not arbitrary live sync data. Malformed entries, duplicate fields,
duplicate package names, unsupported architectures, traversal, corruption and
nonpadding data after the tar end marker fail closed.

The supplemental Arch API validates exact name/repository/architecture and complete
unambiguous results, including duplicate-key rejection. It cannot authenticate
payloads or replace unavailable source evidence; native transaction planning
always uses the independent snapshot. No pacman-to-AUR fallback exists.

## Official signature investigation and implementation

The installed `archlinux-keyring` supplies `archlinux.gpg`,
`archlinux-trusted`, and `archlinux-revoked` under
`/usr/share/pacman/keyrings`; this matches the
[official package file list](https://archlinux.org/packages/core/any/archlinux-keyring/files/).
The installed public keyring is ASCII armored. The trusted file contains five
main-key fingerprints with ownertrust 4. The public export contains certificates
of different trust states, so membership alone cannot authenticate a packager.

[Arch's keyring trust calculation](https://github.com/archlinux/archlinux-keyring/blob/master/libkeyringctl/trust.py)
requires three unrevoked main-key certifications for fully trusted packagers.
[pacman-key](https://man.archlinux.org/man/pacman-key.8) imports distribution
keyrings, locally certifies main keys, imports ownertrust, and disables revoked
entries. Plain [gpgv](https://www.gnupg.org/documentation/manuals/gnupg/gpgv.html)
is insufficient: all supplied keys are trusted and expiration/revocation ignored.
An isolated GPG trustdb with ultimate roots and a completes-needed setting was
also investigated; disposable tests demonstrated that one ultimate certification
can bypass the intended three-root policy. That design was rejected.

Final verification:

1. Safely read all three distribution files; never consult custom/user keyrings.
2. Use a disposable GPG home, no options/auto retrieval/import/autostart/default
   keyring. Dearmor the distribution export there when necessary.
3. GnuPG verifies the detached signature over the full archive. `trust-model
   always` is used only for cryptographic verification, **not** signer authorization.
   Strict status parsing accepts one valid binary signature and rejects invalid,
   unknown, expired, revoked, ambiguous and unsupported signatures/hashes.
4. Read current main-root fingerprints/validity. `--check-sigs --no-sig-cache`
   supplies cryptographically verified full-fingerprint certifications. Require
   three distinct usable main keys on one usable UID; honor certification
   revocations and reject weak certification hashes. The
   [GnuPG colon format](https://github.com/gpg/gnupg/blob/master/doc/DETAILS)
   defines these records.
5. Explicit revoked-list checks cover signer and primary key. Reread distribution
   material at the end to reject concurrent keyring changes.

Key rotation follows authenticated installed archlinux-keyring updates, without
hardcoded developer keys or receipts. Stale/missing trust material fails closed;
ops does not bootstrap new keys from package metadata or modify the real GPG home.
Native pacman package signature policy remains an additional installation gate.
A custom key locally trusted by pacman cannot pass the isolated certification gate.

## Acquisition, installed comparison and races

Inspection reads existing standard-cache archives only. After approval,
`Source.Prepare` downloads selected packages, checks size/SHA256/signature and
extracts only the authenticated `.PKGINFO` and `.MTREE` for comparison. Native
libarchive reads the already-open archive descriptor. All privileged copies of
snapshot/archive bytes are rehashed in protected staging before use. Only exact
selected package cache filenames are populated; unrelated cache data is retained.

The authenticated manifest defines managed regular-file SHA256, size, mode,
UID/GID, symlink target and directories. Installed local ownership inventory must
match its paths, but cannot authenticate content. Backups are identified only by
the authenticated `.PKGINFO`; their bytes may differ, while regular type,
existence, mode and ownership still must match. Shared directory mode is ignored;
directory type and ownership are checked. All other mutable owned files are
checked strictly; persistent normal mutation may need manual reconciliation.
NoUpgrade, NoExtract and alternate root/database/cache layouts are unsupported.

O_PATH/O_NOFOLLOW descriptor traversal rejects symlink ancestors, nonregular
cache files and unsafe paths without opening FIFOs/devices. A symlink leaf is read
with readlinkat on its descriptor. Managed hard links must all be accounted for
within the authenticated inventory; external links fail. Hashing plus inode,
ctime, mtime, size, mode, ownership and link-count checks, followed by a second
full installed-path pass, detects observed replacement/in-place races. No atomic
snapshot or exclusion of concurrent root changes after verification is claimed.

## Repair, approval, core and AUR paths

The I/O-free plan consumes inspection facts. Existing packages needing repair
are labeled repair/reverification before `Continue?`; core prerequisites and AUR
build dependencies have explicit labels. Approved official `-S` has no `--needed`,
so forged equal-version packages are replaced. The full native transaction is
independently resolved, authenticated, staged and checked afterward with the same
predicate. The equal-version native fixture verifies reinstall selection and
preservation of both explicit and dependency reasons using only a disposable DB.
Declared applications intentionally become explicit; AUR dependency repairs
restore pre-existing explicit reasons, including custom namesakes.

Core git/openssh/github-cli/conditional flatpak, pacman declarations, direct and
exact AUR dependencies, virtual/versioned providers, satisfied transitive closure,
omitted transaction members and retained final bindings share the same content
gate. D-R6 closure materialization and D-R7 final revalidation remain active.
Missing selected installed providers/query failures remain inconclusive rather
than guessed absent; same-name forged state plans repair. AUR source pinning,
review, build-key handling and output/source separation remain intact.

Official config discards user Server/Include and repo signature overrides,
forces required trusted package signatures, and retains only supported global
semantics. Protected source staging is mandatory even with no custom sections.
The private sync mount retains native DB locking and never changes host sync DBs.
AUR artifact -U has no repository sections, excluding implicit repo downloads.
General interactive -Syu retains all configured repositories and their custom
rebuilds (D-R3). After it succeeds, a new independent snapshot is used. A guard
refuses corrective installation when unrelated official upgrades remain pending
against that snapshot, preventing partial upgrades due to lagging user mirrors.

Doctor performs metadata reads and unprivileged temporary queries/GPG work, not
archive downloads, package sync, key imports, repairs, installs or privileged
staging. Flatpak queries retain mandatory read-only isolation and the full
persistent Flathub source/trust predicate. Offline/source failure is inconclusive.

## Exact threat boundary and historical limitation

Custom repositories, official-looking sections, copied package metadata and
locally trusted custom signers are in scope. Authentic installed distribution
keyrings, ops, kernel/filesystem, pacman/libalpm, GnuPG/libarchive, protected
staging utilities and system TLS roots are trusted bootstrap components.
Deliberately replacing the complete system trust base is outside the guarantee.

Current equivalence covers the enumerated managed contents/permissions and
explicit backup/directory exceptions. It does not attest historical scripts,
hooks, unowned side effects, running processes, configuration semantics,
extended attributes, ACLs or capabilities. A prior custom script could leave
side effects even if all current managed bytes match. Neither a reinstall nor a
receipt proves historical official-only installation or a clean system.

## Validation and re-review

Go 1.26.7 exactly, GOENV=off and GOTOOLCHAIN=local throughout. The requested full repository
suite includes disposable synthetic signature fixtures in internal/release;
these exercise verification using temporary test keys and synthetic artifacts.
No actual release artifact, release key or release-signing infrastructure is used.

- Initial Go version/env, module verification and formatting passed.
- Full `go test -count=1 ./...` passed, including all requested focused packages,
  original D-R1/D-R4 regressions, D-R2/Flatpak and D-R3/D-R5/D-R6/D-R7 regressions.
- Targeted `-race -count=1` passed for archtrust, archrepo, arch, resolve, inspect,
  plan, app, testpkg (no tests) and flatpak. Additional native reason and malicious
  database namespace regressions also passed with race enabled.
- `go vet ./...`, `go build ./...` and `git diff --check` passed.
- Full repository race was omitted because of the known expensive diagnostics
  suite; affected-package race coverage passed. Protected CI remains responsible
  for the full required race check.
- Native metadata-spoof test first proves a local official-named database can
  inject a member/provider/dependency, then uses the production mount script to
  prove independent metadata controls all three and host databases remain intact.
- Real isolated GPG tests cover one/two/three certifications, custom locally
  trusted keys, unknown/revoked/invalid signatures and the complete signed archive
  to installed-payload path. Status tests reject expiry and malformed evidence.
- Content tests cover executable/library substitution, missing/type/symlink/mode/
  ownership changes, internal/external hard links, empty/large files, backup
  semantics, traversal, inventory ambiguity, cache nonregular input and
  deterministic replacement/in-place races. Original forge tests add repair,
  idempotence, later replacement and official upgrade invalidation/reestablishment.

Explicit public-network integration authenticated these actual packages on
2026-09-15, with no installation, sudo, key import or workstation cache mutation:

| Target | Version | SHA256 | Manifest entries |
| --- | --- | --- | --- |
| core/acl | 2.4.0-1 | dca9ec50cf51243b86a67367a55e78b1851d31240b1edaf9c011f8511765d999 | 103 |
| extra/git | 2.55.0-1 | fbb24f455cb1de1c004a76b5a4dab92e4a9137764fbc3bc46afe13b7a38814fe | 750 |
| core/openssh | 10.5p1-1 | 7095495592ec579541478fb82dc0c0038f3b9da8adc774ffe1a1589004432992 | 89 |
| extra/github-cli | 2.101.0-1 | a866bf839783d133f164686eff9b0b550ed83a9b7c83cc967fde893123431b26 | 252 |
| extra/flatpak | 1:1.18.2-1 | a8d3a2c80a292b02aff50d3345a532a96bd454d4c8f58f0ca35c87c3762b579c | 241 |

The complete Wave D baseline diff was reviewed across source identity, installation,
AUR boundaries, Flatpak trust, read-only inspection, pure planning and final gates.
D-R1 and D-R4 are corrected under the explicit contract; D-R2/D-R3/D-R5/D-R6/D-R7
remain corrected. No intentionally failing Wave D gate remains. Optional future
maintenance remains typed target ergonomics, core mapping/key-policy/endpoint
maintenance and clearer recovery for unsupported different-name custom providers.
No later audit-wave work was introduced.

## Exact D-R1/D-R4 changed files from the confirmed checkpoint

56 files relative to `8dba1b634480a52faa140b6a26f28b55446e3bcd`:

```text
README.md
docs/architecture.md
docs/configuration.md
docs/package-source-provenance.md
docs/wave-d-independent-review.md
docs/wave-d-r1-r4-correction.md
docs/workstation-security.md
internal/app/app.go
internal/app/applications.go
internal/app/aur_application_test.go
internal/app/aur_order_test.go
internal/app/interruption.go
internal/app/lifecycle.go
internal/app/lifecycle_test.go
internal/app/official_fixture_test.go
internal/app/output.go
internal/app/prepare.go
internal/app/prepare_test.go
internal/app/provenance_test.go
internal/app/review_test.go
internal/arch/manager.go
internal/arch/manager_test.go
internal/arch/official_fixture_test.go
internal/arch/official_native_test.go
internal/arch/provenance.go
internal/arch/provenance_test.go
internal/archrepo/cli_test.go
internal/archrepo/config.go
internal/archrepo/config_test.go
internal/archrepo/official_fixture_test.go
internal/archrepo/query.go
internal/archrepo/review_test.go
internal/archrepo/trusted.go
internal/archrepo/trusted_test.go
internal/archtrust/content.go
internal/archtrust/content_test.go
internal/archtrust/files.go
internal/archtrust/prepare.go
internal/archtrust/query.go
internal/archtrust/signature.go
internal/archtrust/signature_test.go
internal/archtrust/source.go
internal/archtrust/source_test.go
internal/inspect/inspect.go
internal/inspect/official_fixture_test.go
internal/plan/plan.go
internal/resolve/applications.go
internal/resolve/diagnostic_test.go
internal/resolve/official_fixture_test.go
internal/resolve/official_json.go
internal/resolve/planning_test.go
internal/resolve/provenance_test.go
internal/resolve/resolve.go
internal/testpkg/fixture.go
internal/testpkg/official.go
internal/testpkg/pacman.go
```
