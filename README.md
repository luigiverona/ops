# ops

An opinionated, secure workstation reconciler for official Arch Linux x86_64.
It prepares Git, SSH, GitHub access, and the applications you declare. Run it as
your normal user, never with `sudo ops`.

## Install

```sh
curl -fsSL https://ops.luigiverona.dev/install | sh
```

The installer verifies the signed release and installs only the `ops` binary.
It creates `~/.config/ops/apps.toml` only if absent; it does not install
workstation packages. Internet access, sudo, and the official Arch base system
are required. Git, an AUR helper, and Flatpak need not already exist.

## Quick start

1. Install ops.
2. Edit `~/.config/ops/apps.toml` with your preferred editor.
3. Run `ops` in a terminal and review its plan.
4. Run `ops doctor` to check the result.

An empty application configuration is valid. Git identity input, an SSH key
passphrase, and GitHub device authentication are interactive when first needed.
After convergence, another `ops` run reports no changes.

## Configuration

```toml
version = 2

pacman = ["librewolf", "steam"]
aur = ["mullvad-browser-bin"]
flatpak = ["com.tutanota.Tutanota"]
```

Lists may be omitted or empty. Identifiers are exact and case-sensitive:
there is no fuzzy matching or fallback between sources. Unknown fields,
malformed identifiers, duplicates, and contradictory pacman/AUR declarations
are rejected before changes. Removing a declaration never uninstalls software.

Version 1 requires an explicit edit; ops never silently migrates your file.
See [configuration and migration](docs/configuration.md).

## What ops manages

- Always: official `git`, `openssh`, and `github-cli`; Git identity; a managed
  SSH identity and GitHub host trust; GitHub authentication and key reconciliation.
- When declared: official applications, pinned/reviewed AUR applications, and
  user-scoped Flatpaks.
- Only when needed: multilib, AUR build prerequisites including `base-devel`,
  Flatpak and the user Flathub remote, and required application services.

`paru` is not required. Optional package dependencies are not guessed or
automatically selected. Themes, application accounts, UI preferences, and
unrelated personal configuration are not managed.

Package installation is preceded by one full, interactive `pacman -Syu`.
Prerequisites are installed and checked before dependent work. AUR source review
precedes its build-dependency installation and normal-user build. A final
re-inspection determines whether the workstation actually converged.
See [architecture and dependency policy](docs/architecture.md).

## Commands

| Command | Purpose |
| --- | --- |
| `ops` | Inspect, resolve, plan, confirm, apply, and verify |
| `ops doctor` | Read-only diagnostics; never sudo, installation, login, or edits |
| `ops update` | Verify and atomically install a newer signed stable release |
| `ops --help`, `ops --version` | Help and installed version |

Exit codes: `0` success or top-level decline; `1` actionable issues, unavailable
checks, or incomplete work; `2` invalid configuration, unsafe state, or a fatal
operation failure. No-op runs and doctor do not require a TTY.

## Safety model

Release signatures require the exact embedded signing fingerprint; there is no
checksum-only fallback. Plans do not authorize hidden prerequisite installs.
Sudo is acquired only after confirmation and only for privileged work.

AUR instructions are untrusted: review pinned files before approving a build.
SSH/GitHub key deletion requires separate, explicit confirmation. Managed files
use protected boundaries and atomic replacement; unrelated keys and host trust
are preserved. See [workstation security](docs/workstation-security.md) and
[release security](docs/release-security.md).

## Recovery / troubleshooting

Run `ops doctor`, resolve the reported cause, then rerun `ops`. There is no
private convergence database to repair, and interrupted work is not rolled back
as a whole. A failed core upgrade stops dependent work; unrelated application
failures are reported separately.

For package-manager failures, resolve the pacman error and complete a full
`sudo pacman -Syu`; never use a standalone partial upgrade. For source drift,
review a newly resolved plan. For unavailable remote checks, retry when the
service is reachable. Never bypass signature or SSH host-key verification.

## Development

Use exactly Go 1.26.7 with `GOENV=off GOTOOLCHAIN=local`:

```sh
go env GOVERSION
go version
go mod verify
gofmt -l .
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
go build ./...
git diff --check
sh -n script/install.sh script/prepare-release.sh script/render-install.sh script/publish-release.sh script/test-minimal-arch.sh
```

Tests use temporary homes, fake command boundaries, local HTTP servers, and
ephemeral test keys. CI keeps checkout/build tools outside its minimal Arch
runtime. Full VM convergence is a separate gate:
[pre-ops acceptance procedure](docs/vm-acceptance.md).

Licensed under the MIT License.
