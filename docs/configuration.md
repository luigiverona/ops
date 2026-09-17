# Configuration

Edit `~/.config/ops/apps.toml`. The installer creates an apps.toml format 2
file only when absent. Existing files, including their comments, remain untouched.
Ops never rewrites or automatically migrates them.

The `version` field identifies the apps.toml format, independently of the ops
binary version: `version = 2` means format 2. Updating ops does not by itself
change this format number.

The generated default has empty application lists and comments explaining exact
identifiers, the three sources, and where to find names. Git, SSH, and GitHub
setup remains managed even with no declared applications. The path is relative
to your home directory; ops does not use `XDG_CONFIG_HOME` to relocate it.

## Format 2

These are the only supported top-level fields. `version` is a required integer;
the three source lists are arrays of strings and may be omitted or empty:

```toml
version = 2
pacman = []
aur = []
flatpak = []
```

A file containing only `version = 2` is also valid. There are no application
categories or source prefixes inside these lists.

Each identifier is exact and case-sensitive. Ops orders declarations by source
(pacman, AUR, Flatpak), then identifier. Duplicates within a source, a package
declared as both pacman and AUR, unknown fields, options, paths, version
expressions, and malformed identifiers are rejected. A missing exact package is an actionable
issue; ops does not search other sources. Removing a declaration never uninstalls
anything. Optional dependencies must be declared explicitly if wanted.

## Finding identifiers

| List | Identifier | Discovery |
| --- | --- | --- |
| `pacman` | Exact official Arch package name | [Arch packages](https://archlinux.org/packages/) or `pacman -Ss SEARCH_TERM` |
| `aur` | Exact AUR package name, not a differing package base | [AUR](https://aur.archlinux.org/) |
| `flatpak` | Full, case-sensitive Flatpak application ID from Flathub | [Flathub](https://flathub.org/) or `flatpak search SEARCH_TERM` if Flatpak is installed and the remote is configured |

Replace `SEARCH_TERM` with your search text. Copy the package name or full
application ID, not a display title, search summary, version, or repository
prefix. Source-native search helps you choose declarations; ops has no embedded
catalog or search command. See the [README example](../README.md#configuration).

The selected source is authoritative. A pacman declaration cannot fall back to
AUR, and neither can fall back to Flatpak. A confirmed missing identifier calls
for checking that declaration. An unavailable source or inconclusive query
does not establish absence: address the query failure and retry when the source
is accessible, without changing apps.toml merely because the lookup failed.

`pacman` covers only `core`, `extra`, and `multilib`. A custom repository's
same-name package does not provide official readiness evidence. Ops independently
resolves official archives, verifies their digest and official signature, and
compares installed managed contents. Historical origin is not asserted. Missing
cache evidence is reconstructed in disposable temporary storage without an
installation. Differing contents produce a visible official repair plan;
unavailable archive/source/read evidence is inconclusive. User Server and Include values do not
authenticate official repository content.

`flatpak` requires origin `flathub` in the user installation and an enabled remote
with URL `https://dl.flathub.org/repo/`. Ops can add a missing remote or enable an
otherwise canonical disabled remote after approval. Wrong URLs, unsafe remote
options, ambiguous inventories, and other app origins are reported for manual
reconciliation. Ops does not remove or reinstall wrong-origin Flatpaks.
See [package source contracts](package-source-provenance.md) for exact semantics.

## Format diagnostics

A missing file must be created before running `ops`; `ops doctor` can still
inspect the workstation without creating it. A missing `version` field or a
non-integer value is rejected. Check the file's structure against the format 2
example before repairing the field. Integers below 1 are unsupported and have
no known migration. Format 2 also strictly rejects invalid or unknown fields
and malformed TOML before workstation changes.

For a future integer format, the installed binary reports that it supports
format 2. Check for a compatible ops release; an available update may or may not
support that format. Do not simply lower the version field.

## Migration from format 1

Format 1 is rejected before workstation changes, with migration guidance.
Move each old `source:identifier` into the corresponding source list, remove
the source prefix and categories, and set the `version` field to `2`:

```toml
version = 1
[apps]
browser = ["pacman:librewolf", "aur:mullvad-browser-bin"]
mail = ["flatpak:com.tutanota.Tutanota"]
```

becomes:

```toml
version = 2
pacman = ["librewolf"]
aur = ["mullvad-browser-bin"]
flatpak = ["com.tutanota.Tutanota"]
```

Review the converted declarations before running ops. Changing only the
`version` field does not convert format 1 categories and source prefixes.
