# Configuration

Edit `~/.config/ops/apps.toml`. The installer creates a minimal version 2
configuration only when the file is absent. Ops never rewrites it.

The generated default has no applications. See the small current example in
[README](../README.md#configuration); a file containing only `version = 2`
is also valid.

Lists may be omitted or empty. Each identifier is exact and case-sensitive.
Ops orders declarations by source (pacman, AUR, Flatpak), then identifier.
Duplicates within a source, a package declared as both pacman and AUR, unknown fields, options, paths, version expressions,
and malformed identifiers are rejected. A missing exact package is an actionable
issue; ops does not search other sources. Removing a declaration never uninstalls
anything. Optional dependencies must be declared explicitly if wanted.

## Migration from version 1

Version 1 is rejected before workstation changes, with migration guidance.
Move each old `source:identifier` into the corresponding source list, remove
the source prefix and categories, and change the version:

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

This is a breaking configuration change and calls for a major release under
semantic versioning. Release selection and publication are separate from this
source refactor.
