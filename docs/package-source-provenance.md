# Package source provenance

## Official Arch contract

Official readiness means **current managed package contents match an independently
resolved, authenticated official Arch archive**, with the explicit backup and
shared-directory exceptions below. It does not assert historical installation
origin or that the system is free of side effects from earlier scripts.

`internal/archtrust` separates three responsibilities: an independent official
source snapshot, an authenticated archive, and installed-content evidence.
`archrepo.Repositories()` lists supported namespaces (`core`, `extra`, `multilib`),
not authentication authorities. User repository names, Server lines, mirror-list
Includes, local sync databases and custom locally trusted keys cannot establish
official identity.

### Independent source and signature authentication

Ops obtains all three x86_64 sync databases directly from
`https://geo.mirror.pkgbuild.com/$repo/os/x86_64/$repo.db`, with normal HTTPS
certificate/hostname verification and no redirects. Arch identifies pkgbuild.com
mirrors as operated by its DevOps team; see the
[source investigation and integration evidence](wave-d-r1-r4-correction.md).
The immutable per-run snapshot supplies existence, repository, dependencies,
providers, transaction members, archive filename, SHA256 and embedded signature.
It includes `any` packages and multilib even before user multilib is enabled.
Bounded parsing rejects malformed, duplicate, ambiguous and unsupported records.
Native libalpm queries use only these bytes in disposable unprivileged scratch
storage; they do not read or synchronize the live sync databases. The live local
database provides installed constraints, never official authentication.

An archive must match the snapshot's size and SHA256 **and** a supported official
package signature. Ops uses only the installed distribution files
`/usr/share/pacman/keyrings/archlinux.gpg`, `archlinux-trusted`, and
`archlinux-revoked`. GnuPG performs cryptographic verification in a disposable
home, without imports, keyservers, the user's keyring or pacman's live GPG home.
Separate certification checks require three distinct current Arch main-key
certifications on one usable packager UID, with full issuer fingerprints.
Unknown, invalid, expired, revoked and insufficiently certified signers fail
closed; the distribution revoked list also overrides inclusion in the public
keyring. `gpgv` and ordinary pacman trust alone are insufficient. Key material
is reread for every verification and checked for changes during it; normal
archlinux-keyring updates provide rotation/revocation updates. Ops does not
update that trust base during inspection or silently recover missing keys.

The Arch HTTPS API remains supplemental exact-name metadata after an exact
native miss in an available independent snapshot. It proves only metadata
presence/absence, never archive bytes, installed content or transaction identity.
Malformed or duplicate JSON keys, incomplete/ambiguous results, wrong name/repo/
architecture, network errors and unavailable source evidence are inconclusive.
An independent-source outage cannot be masked by a successful API response.
There is no pacman-to-AUR fallback. Actual transaction membership always comes
from the independent sync snapshot, including after API metadata resolution.

### Installed-state evidence and repair

`archrepo.InstalledMatch` is the single shared gate. Local name/version/arch/build
date/packager are preliminary consistency checks only. Success additionally
requires an authenticated archive from the standard pacman cache, the exact
owned-path inventory, and filesystem comparison against the archive's signed
`.MTREE` and `.PKGINFO`, never the installed local `.MTREE`.

Regular non-backup files require exact SHA256, size, type, mode (including special
permission bits), UID and GID. Missing files, substituted executables/libraries,
wrong types and changed symlink targets fail. Symlinks are read as symlinks,
without following the leaf or any symlink ancestor. Authenticated backup entries
may contain locally modified bytes, but must retain regular type, existence,
mode and ownership. Shared directories require directory type and ownership;
their mode is not attributed exclusively to one package. Hard links are accepted
only when every link is accounted for by the same authenticated managed inventory.
Extra owned paths fail. Non-backup runtime-mutated files have no implicit exemption:
they require repair or manual reconciliation if normal operation changes them
again. NoUpgrade/NoExtract configurations are unsupported for official operations.

Cache paths are not evidence: regular-file descriptors, size/digest/signature
verification and stable stat checks authenticate the bytes. Symlinks, nonregular
files and multiply linked cache archives are rejected. Descriptor-relative
O_NOFOLLOW traversal, descriptor-based archive reads, observed inode/ctime/mode/
size/ownership checks and a second complete installed-path pass detect observed
replacement races. This is not an atomic filesystem snapshot and cannot exclude
privileged mutation after inspection.

A pre-existing legitimate installation is ready when this evidence is available
and matches. A missing archive, differing installed content or differing metadata
produces a visible official repair/reverification plan before `Continue?`.
Lookup/read/key/source errors instead report unavailable inspection; they do not
justify destructive repair. Doctor downloads metadata, never package archives.
After normal approval, repair downloads and authenticates the selected archive,
forces replacement even at equal version, and verifies every transaction member
with the same predicate. Official `-S` omits `--needed`. Subsequent matching runs
are idempotent while the archive remains cached. There are no readiness receipts:
custom replacement, changed official archive identity, upgrades or payload drift
are discovered by fresh evidence checks. Cache eviction may require reverification
through another visible repair. Ops never deletes unrelated cache entries.

### Official transactions versus general upgrades

General interactive `pacman -Syu` deliberately uses the user's complete configured
repository set, including custom rebuilds. Qualified managed targets in that
operation do not authenticate the user's repository content; all readiness and
subsequent official gates remain independent. After a successful full upgrade,
ops obtains a fresh official snapshot. Before a corrective transaction, every
pending upgrade in that snapshot must already be included in the transaction;
otherwise ops stops and asks for reconciliation of the full system upgrade.
This prevents a lagging user mirror from causing an independent partial upgrade.

Official mutation always stages an independent-source configuration and snapshot
in a protected root-owned directory. All user repository Server/Include and
per-repository policy overrides are discarded. Strong package signature policy
is forced; necessary supported global settings (GPGDir, hooks, DownloadUser,
ignore policy and ordinary presentation/logging settings) are retained. Only
standard root, database and cache paths and x86_64 are supported. Custom transfer
commands, sandbox disabling and content-exclusion settings fail closed. Unsupported
policy is detected during official inspection before approval.

Authenticated databases and archives are streamed into protected staging and
rehashed there. Authenticated archives populate only their exact standard cache
filenames. A private mount namespace mounts the staged sync database read-only
at the ordinary sync path for the transaction, retaining the real local DB and
pacman lock. It never replaces host sync databases. Native signature verification
remains an additional gate. Missing namespace support aborts; no weaker fallback
exists. Protected-path checks and cleanup also apply on failure. AUR artifact
`-U` has no repository sections, so it cannot introduce implicit repo downloads.

Ordinary installations leave pulled dependencies implicit. AUR build installs
use every approved qualified target with `--asdeps`, then restore existing
explicit reasons (including a repaired custom namesake) and declared application
intent. Declared apps are deliberately explicit. General repairs otherwise retain
pacman's reinstall reason behavior. Core `git`, `openssh`, `github-cli`, and
conditional `flatpak` use the same strong readiness/postcondition predicate.

### Boundary and limitations

The trusted platform consists of ops, the kernel/filesystem, verification tools
(pacman/libalpm, GnuPG, libarchive and protected staging tools), TLS roots and
authentic distribution keyring material. Arbitrary custom repositories and
locally trusted custom package signers are in scope. An administrator or package
script replacing this entire trust base is outside the guarantee.

The predicate covers the enumerated managed content and permissions, not all
system behavior: it does not authenticate mutable config bytes, extended
attributes/ACLs/capabilities, unowned files, running processes or hook side effects.
A custom archive with equivalent managed bytes could previously have run a
malicious install script. Reinstall cannot prove that never happened or clean
arbitrary side effects. Current-content equivalence is not a clean-system or
historical-provenance attestation. Package scripts and configured system hooks
run with normal pacman semantics only during approved transactions.

Doctor performs no package synchronization, repair/install, key import, privileged
staging or persistent repo mutation. Metadata/query/GPG scratch files are temporary
unprivileged files, removed after use; host package/config state is read only.
Offline operation cannot establish new authoritative source evidence and reports
inconclusive inspection. Planning itself remains deterministic and I/O-free.

## AUR official dependencies

`OfficialDependency` records the qualified provider and every transaction member,
even when `pacman -T` succeeds. It walks independent official `Depends On`
metadata, retaining the entire closure including satisfied transitive providers.
Direct, exact, virtual, versioned, transitive and final retained bindings all use
`InstalledMatch`. A forged same-name satisfier creates visible repair work.
An unreadable/missing selected installed provider or ambiguous different-name
custom satisfier fails closed; ops does not infer absence from a query failure.

Native `%r/%n\t%P` resolution uses the independent source. After pinned-source
review and approval, revalidation rejects changed providers/repositories and new
members. Transactions may shrink after approved work only if omitted members
still pass authenticated-content checks. Concrete transactions are revalidated
before mutation, after installation, before makepkg and during final inspection.
D-R6 closure discovery and D-R7 retained binding checks remain intact. Pinned AUR
source review, build-signing-key approval and protected artifact handling remain
unchanged. These official checks do not authenticate AUR-built output as Arch.

## User Flathub contract

Inspection runs `flatpak remotes --user --show-disabled
--columns=options,name,url --json` and `flatpak list --user --app
--columns=origin,application --json`. C-locale JSON uses `options`, `name`, `url`,
`origin`, and `application_id` keys. JSON preserves record boundaries that an
unescaped tab table cannot guarantee. Duplicate keys, duplicate remote names,
multiple refs/origins for one app ID, missing/unknown fields, non-string values,
invalid identifiers, null and trailing data fail closed. An empty application
inventory can be zero bytes; an empty remote inventory must be a JSON array.
CLI failures and capture truncation are never interpreted as absence.
Both inventory commands require a read-only filesystem boundary using bubblewrap
(already required by Arch's Flatpak package), with isolated process, network and
IPC namespaces. Native queries can otherwise initialize or migrate user state.
There is no unsandboxed fallback. If existing state needs migration or isolation
is unavailable, inspection can fail; perform the necessary Flatpak maintenance
manually and rerun Doctor. No inventory query is allowed to repair that state.

The remote postcondition additionally requires bounded, read-only inspection of
`repo/config` and the exact per-remote `flathub.trustedkeys.gpg` bootstrap keyring.
CLI columns alone cannot establish trust. The supported state is full OSTree
Flathub at `https://dl.flathub.org/repo/`, enabled, with effective commit and
summary verification, no collection ID, no subset/filter, and no noncanonical
source/trust options. Missing or empty subset/filter values are unrestricted;
a stored subset `-` is restricted. Unknown security options fail closed.
Presentation title/comment/description/homepage/icon fields are optional.

The keyring is pinned by SHA256 to the independently reproduced public bootstrap
material. Missing, altered, extra, or differently serialized keys require manual
review; production inspection neither imports keys nor invokes GPG. This checks
configured source/trust identity, not historical installed application bytes.
Application readiness additionally requires origin exactly `flathub`.
See [D-R2 evidence and exact predicate](wave-d-r2-correction.md) for defaults,
collection interaction, supported fields, filter behavior and trust boundaries.

A missing remote is added after approval without `--if-not-exists`; an unexpected
namesake at execution time aborts. An otherwise canonical disabled remote is
explicitly shown in the pre-approval plan and corrected with `remote-modify --user
--enable flathub`. Both operations reinspect and verify the full postcondition.

Wrong-URL namesakes and incompatible options are reported for manual reconciliation.
Although `remote-modify --url` can change the URL, it preserves installed apps and
remote trust state. Relabeling that namesake cannot authenticate applications
previously obtained through it. No general safe source migration is assumed.
Other-origin installed apps are failures, not absence; no uninstall or reinstall
is issued. Flatpak documents `install --reinstall` as uninstalling first, which
is outside ops' automatic repair policy. Installs recheck remote and app origin
before mutation and verify both afterward. Doctor only inspects and reports;
final workstation reinspection uses the same rules.

## CLI evidence and regression probes

Verified locally against pacman 7.1.0 / libalpm 16.0.1 and Flatpak 1.18.2:

- Real read-only `-Si`, `-Qi`, `-Sp`, `-Sp --needed`, `-Sl`, and `pacman-conf`
  output, plus the installed pacman manual. The isolated synthetic-database test
  in `archrepo/cli_test.go` demonstrates qualified targets, `%r`/`%P`, custom
  dependency priority, filtered official resolution, multilib, native custom
  namesakes, equal-version `--needed` skipping, and absent local repository origin.
- Pacman upstream [CLI documentation](https://pacman.archlinux.page/pacman.8.html)
  and [local database implementation](https://gitlab.archlinux.org/pacman/pacman/-/blob/v7.1.0/lib/libalpm/be_local.c).
  [libalpm dependency resolution](https://gitlab.archlinux.org/pacman/pacman/-/blob/v7.1.0/lib/libalpm/sync.c)
  assigns dependency reasons to implicitly pulled packages.
- Actual Flatpak help, user inventory output, and an isolated temporary user
  installation created from the official bootstrap file. Remote creation,
  disable/enable, URL modification, and their JSON postconditions were exercised
  there. No real remote or app was changed. The empty-inventory regression uses
  an isolated `FLATPAK_USER_DIR` and XDG directories, with read-only queries.
- Flatpak's [command reference](https://docs.flatpak.org/en/latest/flatpak-command-reference.html)
  documents origin columns, remote modification and reinstall semantics.
  The [Flathub bootstrap](https://dl.flathub.org/repo/flathub.flatpakrepo)
  supplies `Url=https://dl.flathub.org/repo/` and public signing-key material.

Tests require no privileged package mutations. Real-CLI tests skip when the
executable is absent; unsupported/malformed CLI output fails closed. No VM,
real installation, release or publishing operation is part of this wave. Isolated
package-signing and synthetic release-test fixtures use disposable test keys only.
