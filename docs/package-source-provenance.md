# Package source provenance

## Official Arch contract

The centralized allowlist is `archrepo.Repositories()`: `core`, `extra`,
`multilib`. Repository names, configured official mirrors, local sync databases,
pacman's keyring and signature policy are trusted system configuration. Ops does
not authenticate a mirror or re-audit the root-owned pacman trust configuration.
Testing/staging repositories and custom repository names are outside this contract.

Installed readiness has this precise meaning: the package is installed and its
name, version, architecture, build date and packager equal the metadata from one
unambiguous supported official sync package. `pacman -Sl` identifies candidates;
`pacman -Qi -- name` and `pacman -Si -- repo/name` establish the comparison.
Missing or malformed metadata is an inspection failure. A differing tuple does
not establish readiness and setup plans an official reinstall. This may include
an older official installation when the sync database has a newer version.

This is a **current metadata match**, not historical origin, a signature check
on installed files, or a cryptographic content comparison. Matching metadata can
be copied; it cannot authenticate arbitrary previously installed bytes. Pacman's
local database has no historical repository field. `-Qn`/`-Qm` only classify
names relative to *all configured sync databases*, including custom ones. Neither
classification supplies official readiness evidence. AUR's existing foreign
classification and exact declaration isolation are unchanged.

Resolution accepts a local `-Si` result only with the exact requested name and
an allowed repository. A custom result can trigger the existing official Arch
HTTPS metadata lookup, whose response must identify exactly one allowed package;
it cannot trigger an AUR fallback. `plan.Package.Repository` survives into the
application plan and installation target. Multilib facts still request enabling
multilib before the full upgrade.

Core prerequisites use fixed official targets: `extra/git`, `core/openssh`,
`extra/github-cli`, and conditional `extra/flatpak`. They use the same installed
readiness evidence, qualified installation, and metadata verification before
dependent mutations. Installed managed targets are also qualified in the full
upgrade, preventing a higher-version custom namesake from remaining in place
merely because pacman's ordinary upgrade would not downgrade it.

Every sync installation first obtains `-Sp --noconfirm --print-format %r/%n`.
Unknown/custom repositories, malformed records, duplicate names (even across
repositories), and omitted requested targets fail closed. Actual targets are
qualified. All package mutations, including the full upgrade and AUR artifact
`-U`, inspect `pacman-conf`'s expanded configuration. If custom sections exist,
ops retains global policy and official repository sections, streams the result
into a root-owned protected staging directory, validates ownership/type/link
counts, and invokes pacman with that temporary `--config`. Includes must already
be expanded; duplicate sections and missing core/extra fail closed. The original
configuration is preserved. Cleanup also runs on failure.

The pre-approval plan discloses that custom repositories are excluded from the
upgrade. Their packages do not receive custom-repository updates through ops.
The full official upgrade remains one interactive `-Syu`; ops never performs
an isolated database refresh followed by a partial official upgrade.

Ordinary installations keep additional dependencies implicit so pacman retains
its dependency reasons. AUR build transactions specify every concrete approved
`repo/name` with `--asdeps`, then restore the explicit intent already carried by
the plan. Declared pacman apps are marked explicit after source verification.
Sync installs omit `--needed`: equal versions alone must not skip a planned
metadata-mismatch reinstall. Every printed transaction member is verified after
installation. Local admin changes to databases/configuration concurrently with
execution are outside the locking guarantees of these separate CLI processes.

## AUR official dependencies

`OfficialDependency` always records its qualified provider and every printed
transaction member, even when `pacman -T` says the requirement is satisfied.
The `%r/%n\t%P` output identifies native resolver providers, including virtual
ones, and every concrete repository. Successful `-T` additionally requires a
current official metadata match for the selected provider/transaction; a custom
installed satisfier is not accepted merely because it satisfies a version test.

After pinned-source review and approval, revalidation rejects changed providers,
changed repositories and new transaction members. Transactions may shrink after
earlier approved work, but omitted members must still match their planned official
repository metadata. Concrete installation is checked again immediately before
mutation. Provider and omitted-member provenance are rechecked after installation,
before makepkg. The pinned-source, review, signing-key and artifact staging models
remain intact; no new source fallback or package helper is introduced.

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

The remote postcondition is: a user remote named exactly `flathub`, URL exactly
`https://dl.flathub.org/repo/`, enabled, with none of the reported `disabled`,
`oci`, `no-enumerate`, `no-gpg-verify`, or `filtered` options. Unknown reported
options fail inspection. The bootstrap `.flatpakrepo` URL is distinct from the
stored repository URL. Application readiness additionally requires that the
application ID's origin is exactly `flathub`. This checks stored source identity,
not historical content authenticity or every remote keyring entry.

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
real installation, release, publishing or signing operation is part of this wave.
