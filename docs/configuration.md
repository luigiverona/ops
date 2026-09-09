# Configuration

Edit `~/.config/ops/apps.toml`. The installer creates an apps.toml format 2
file only when absent. Existing files, including their comments, remain untouched.
Ops never rewrites or automatically migrates them.

The `version` field identifies the apps.toml format, independently of the ops
program version: `version = 2` means format 2, including when running ops 2.1.0.

The generated default has empty application lists and comments explaining exact
identifiers, the three sources, and where to find names. Git, SSH, and GitHub
setup remains managed even with no declared applications. See the small current
example in [README](../README.md#configuration); a file containing only
`version = 2` is also valid.

Lists may be omitted or empty. Each identifier is exact and case-sensitive.
Ops orders declarations by source (pacman, AUR, Flatpak), then identifier.
Duplicates within a source, a package declared as both pacman and AUR, unknown fields, options, paths, version expressions,
and malformed identifiers are rejected. A missing exact package is an actionable
issue; ops does not search other sources. Removing a declaration never uninstalls
anything. Optional dependencies must be declared explicitly if wanted.

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
