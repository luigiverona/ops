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

The canonical install command is:

```sh
curl -fsSL https://ops.luigiverona.dev/install | sh
```

The POSIX installer detects the exact platform, resolves the latest release,
downloads `ops-linux-x86_64`, `checksums.txt`, and `checksums.txt.sig`, verifies
the GPG signature and binary SHA-256, asks through `/dev/tty`, and atomically
installs `/usr/local/bin/ops`. It creates `~/.config/ops/apps.toml` only when
absent.

Release trust is provisioned with a reviewed embedded public key and the exact
release-signing subkey fingerprint
`EB564BFFD8F63A984BF72A0237A80EDB682BBBFD`. Installer and updater require that
exact active signing subkey and fail closed on invalid trust or signatures.
Release hosting is provisioned at `https://ops.luigiverona.dev`. See
[Release security](docs/release-security.md) for the signing and publication
model.

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

The preparation plan lists changes rather than repeating every discovered
ready state. Actions use concrete verbs, application sources occupy a separate
column, and already satisfied work is reduced to an `Unchanged` summary. For
example:

```text
Plan

System
  full system upgrade  upgrade  pacman; confirm transaction in pacman

Core
  paru  install  AUR bootstrap; review required

Applications
  bitwarden              install  pacman
  com.tutanota.Tutanota  install  flatpak

Identity and access
  SSH identities                review        unrelated local keys
  github.com SSH configuration  configure     managed identity and host trust
  github                        authenticate  CLI login
  GitHub SSH keys               inspect       reconcile after login
  GitHub SSH key                configure     register after login, if missing

Unchanged
  5 core components
  6 applications

Prepare this workstation? [Y/n]
```

Preparation generally follows inspect -> plan -> confirm -> mutate -> verify.
After confirmation, `Progress` records identify each operation owned by ops.
One `Progress` block covers an uninterrupted sequence of operations; a later
block starts only after `Review` content deliberately interrupts that sequence.
`Issues` groups unresolved and failed work, and `Final` is emitted once with the
observed outcome. A plan containing only diagnostics or unavailable checks is a
true no-op: ops does not request confirmation or sudo and reports `Final`
directly.
That single plan confirmation authorizes deterministic listed actions; AUR package
review remains intentionally separate, as do prompts that collect required values,
drive external authentication/passphrase flows, or make explicit security review
and deletion decisions.

For v1.0.1, a full `pacman -Syu` is also intentionally interactive. After the ops
plan is approved, Pacman presents and owns the final system transaction review and
confirmation. ops does not use `--noconfirm` or automate Pacman prompts, because
Pacman can make package replacement, provider-selection, or key-import decisions
that ops cannot yet represent safely before confirmation.
Noninteractive subprocess output is captured and included in actionable errors.
Programs that require package review, upstream decisions, passwords,
passphrases, or account authentication retain their interactive terminal
streams. ops marks those streams in `Progress` with an `external` row before the
program runs. In v1.0.2, GitHub login requests `admin:public_key`, the minimum
scope needed for ops-managed account SSH-key reconciliation; an existing session
without it is explicitly planned for `gh auth refresh`. Deferred key
reconciliation is `inspect`; `Review` appears only when keys are actually
available for review.

Application dependencies and maintained services are separate planned actions,
with their owning application retained in the item name:

```text
Applications
  mullvad-vpn -> libfoo                  install  pacman
  mullvad-vpn                            install  pacman
  mullvad-vpn -> mullvad-daemon.service  enable   systemd
```

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
Git, SSH, agent, gh, GitHub account keys, and configuration state. Ready
components are skipped. Removing a declaration never uninstalls it. After
interruption or a partial run, rerun `ops`; completed operations are preserved
and actual state is rediscovered.

## Git, SSH, and GitHub

Git management is limited to missing/invalid `user.name` and `user.email`.
Valid values are preserved; no personal Git preferences are managed.

The managed SSH identity is Ed25519 at `~/.ssh/ops` and `~/.ssh/ops.pub`, with
passphrase handling delegated to `ssh-keygen`. Discovery validates key material,
pairs by fingerprint, ignores symlinks, and protects `config`, `known_hosts`,
`authorized_keys`, certificates, directories, and sockets. Existing identities
are inspected before planning. When SSH setup is required, unrelated identities
are reviewed individually. Deletion shows exact files and requires a second
confirmation defaulting to no.

Agent identities are separate from local files; unloading never deletes files.
An isolated, marked `~/.ssh/ops_config` makes `github.com` use only
`~/.ssh/ops` with `IdentitiesOnly yes` after reboot. Because OpenSSH
`IdentityFile` directives are additive, a marked dispatcher in `~/.ssh/config`
excludes the byte-for-byte preserved `~/.ssh/ops_user_config` only for
`github.com` and includes it for every other host. The preserved file remains
the user's configuration; ops owns only its marked dispatcher and isolated
managed files. Ops obtains GitHub's current public SSH host keys from GitHub's
official HTTPS metadata, validates them, and atomically maintains marked
`~/.ssh/ops_known_hosts` with
`StrictHostKeyChecking yes`. The user's ordinary `known_hosts` is preserved and
is not required. Recognized local configuration and host-key freshness are
inspected separately. A temporary metadata transport/service failure is
reported as unavailable and never causes a rewrite; malformed authoritative
metadata remains a hard error. Existing unmarked ops-specific files and unsafe
symlinks are refused rather than overwritten. Effective configuration is
structurally verified with `ssh -G`.

Authentication is delegated to `gh auth login --git-protocol ssh
--skip-ssh-key`; ops never stores tokens. When key reconciliation is required,
unrelated existing GitHub keys are reviewed one at a time by title/fingerprint.
Remote deletion gets a second default-no confirmation. The managed key gets a
fingerprint-derived title, duplicates are avoided, and SSH access is verified.
When already authenticated, ops reads the account's keys before planning and
matches the managed key by fingerprint. When authentication is unavailable,
the plan explicitly makes key inspection and conditional registration dependent
on login; no remote mutation occurs during inspection.

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
go version # release validation uses exactly go1.26.7
gofmt -w ./cmd ./internal
go vet ./...
go test ./...
go build ./...
sh -n script/install.sh script/prepare-release.sh script/render-install.sh script/publish-release.sh
```

Tests use temporary homes and pacman fixtures, fake external commands/GitHub,
local HTTP servers, and ephemeral GPG keys. They never mutate real SSH, GitHub,
Flatpak, package-manager, or system configuration state.

Licensed under the MIT License.
