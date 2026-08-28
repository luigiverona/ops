# ops

`ops` is an opinionated workstation preparer for official Arch Linux x86_64.
It reconciles:

```text
workstation = core + declared applications
```

It installs missing declared applications and supporting functionality, but
never removes an application because it disappeared from the configuration.

## Platform and execution model

The supported platform is exactly official Arch Linux on x86_64. Arch Linux
ARM, derivatives such as Manjaro and EndeavourOS, other Linux distributions,
macOS, and Windows are rejected.

Run `ops` as the normal desktop user. `sudo ops` is rejected because AUR builds,
user Flatpaks, Git configuration, SSH keys, and GitHub authentication must
remain owned by that user. After an accepted plan, `ops` obtains one `sudo -v`
authorization and safely refreshes that timestamp while privileged work is
active. User-level work never uses sudo.

## Installation

The canonical command, once signing and hosting are provisioned, is:

```sh
curl -fsSL https://ops.luigiverona.dev/install | sh
```

The POSIX installer detects the exact platform, resolves the latest release,
downloads `ops-linux-x86_64`, `checksums.txt`, and `checksums.txt.sig`, verifies
the GPG signature and binary SHA-256, asks through `/dev/tty`, and atomically
installs `/usr/local/bin/ops`. It creates `~/.config/ops/apps.toml` only when
absent.

Release trust is intentionally not configured in this new repository. Until a
real offline primary key and release-signing subkey are provisioned, installer
and updater fail closed. See [Release security](docs/release-security.md).

## Commands

```text
ops             inspect, plan, confirm, and prepare the workstation
ops doctor      perform read-only diagnostics
ops update      verify and install a newer stable release
ops --help      show command help
ops --version   show the installed version
```

Preparation is interactive and requires a TTY. `doctor`, `--help`, and
`--version` remain line-oriented when redirected. There is no dry-run flag and
there are no other public commands.

## Applications

Configuration lives at `~/.config/ops/apps.toml`:

```toml
version = 1

[apps]
browser = ["aur:librewolf-bin"]
vpn = ["pacman:mullvad-vpn"]
vault = []
mail = ["flatpak:com.tutanota.Tutanota"]
social = []
music = []
game = ["pacman:steam"]
```

Supported sources are `pacman`, `aur`, and `flatpak`. Values use exact
`source:identifier` syntax. The declared source is authoritative: there is no
fuzzy matching or pacman-to-AUR-to-Flatpak fallback. Malformed TOML, unknown
versions/categories/sources, empty identifiers, and duplicates are fatal before
mutation or sudo. A valid but nonexistent identifier is `Unresolved` and does
not stop unrelated applications.

Required dependencies are handled by the source package manager. Compatible
direct optional dependencies are considered one level deep; conflicting
alternatives are skipped rather than guessed. Supporting packages retain
dependency reason semantics. Objectively necessary maintained services are
enabled. Themes, application accounts, UI preferences, and subjective defaults
are not changed.

## Core and Arch behavior

The always-managed core is:

```text
git  ssh  github  aur  paru  flatpak  flathub
```

It maps to official `git`, `openssh`, `github-cli`, and `flatpak` packages, a
reviewed normal-user `paru` bootstrap, user Flathub, and internal `base-devel`.

Repository prerequisites such as `multilib` are planned first. `pacman.conf`
changes are minimal, staged, checked for concurrent edits, validated by
`pacman-conf`, and atomically replaced. Package mutations get exactly one full
`pacman -Syu` before installs. `ops` never uses standalone `pacman -Sy`.

AUR instructions are untrusted community content. The paru bootstrap displays
sanitized tracked files and requires explicit review. Declared AUR applications
use paru's interactive review and are forced to the AUR source. `makepkg` and
paru run as the normal user, never through sudo. Flatpak and Flathub are always
user-scoped.

## Idempotency and recovery

There is no internal state database. Every run discovers real pacman, Flatpak,
Git, SSH, agent, gh, GitHub, and configuration state. Ready components are
skipped. Removing a declaration never uninstalls it. After interruption or a
partial run, rerun `ops`; completed operations are preserved and actual state is
rediscovered.

## Git, SSH, and GitHub

Git management is limited to missing/invalid `user.name` and `user.email`.
Valid values are preserved; no personal Git preferences are managed.

The managed SSH identity is Ed25519 at `~/.ssh/ops` and `~/.ssh/ops.pub`, with
passphrase handling delegated to `ssh-keygen`. Discovery validates key material,
pairs by fingerprint, ignores symlinks, and protects `config`, `known_hosts`,
`authorized_keys`, certificates, directories, and sockets. Existing identities
are reviewed individually. Deletion shows exact files and requires a second
confirmation defaulting to no.

Agent identities are separate from local files; unloading never deletes files.
An isolated `~/.ssh/ops_config` and first-match Include make `github.com` use
`~/.ssh/ops` with `IdentitiesOnly yes` after reboot while preserving unrelated
SSH config. Effective configuration is verified with `ssh -G`.

Authentication is delegated to `gh auth login --git-protocol ssh
--skip-ssh-key`; ops never stores tokens. Existing GitHub keys are reviewed one
at a time by title/fingerprint. Remote deletion gets a second default-no
confirmation. The managed key gets a fingerprint-derived title, duplicates are
avoided, and SSH access is verified.

## Doctor and update

`ops doctor` is read-only. It checks system, configuration, core, applications,
Git, SSH, and GitHub without sudo, installs, edits, authentication, service
changes, or key changes.

`ops update` uses the installer's isolated GPG and signed SHA-256 trust. Sudo is
requested only after verification. A staged version is verified and atomically
renamed; a prior regular binary is restored if the final postcondition fails.
Normal preparation never auto-updates.

## Exit codes

```text
0  success, including intentional skips
1  completed with application issues, or actionable doctor issues
2  fatal error; ops could not safely continue
```

## Troubleshooting

- Platform errors: verify `/etc/os-release` has `ID=arch` on official Arch and
  run directly as the desktop user, not through sudo.
- Configuration errors: fix the exact reported field; ops never guesses intent.
- Upgrade failures: resolve the pacman error and complete `pacman -Syu` before
  rerunning.
- AUR failures: inspect the reviewed PKGBUILD, AUR comments, and makepkg error.
- Trust errors: never bypass verification; confirm project release status and
  the independently published fingerprint.
- Interruptions: rerun ops to rediscover state and resume.

## Development

```sh
gofmt -w ./cmd ./internal
go vet ./...
go test ./...
go build ./...
sh -n script/install.sh
```

Tests use temporary homes and pacman fixtures, fake external commands/GitHub,
local HTTP servers, and ephemeral GPG keys. They never mutate real SSH, GitHub,
Flatpak, package-manager, or system configuration state.

Licensed under the MIT License.
