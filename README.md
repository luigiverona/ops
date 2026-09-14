# ops

An opinionated workstation reconciler for official Arch Linux x86_64.
It prepares Git, SSH, GitHub access, and the applications you declare. Run it as
your normal user, never with `sudo ops`.

## Install

```sh
curl -fsSL https://ops.luigiverona.dev/install | sh
```

The installer verifies the signed release and installs the `ops` binary at
`/usr/local/bin/ops`.
It creates `~/.config/ops/apps.toml` only if absent; it does not install
workstation packages. Internet access, sudo, and the official Arch base system
are required. Git, an AUR helper, and Flatpak need not already exist.

Existing configuration files are preserved. If configuration setup fails after
the binary is installed, the installer reports that the binary remains installed
and gives repair guidance; installation is not transactional.

## Quick start

1. Install ops.
2. Optionally add applications to `~/.config/ops/apps.toml` with your preferred
   editor. The generated file explains identifiers and sources.
3. Run `ops` in a terminal, review the setup summary, and approve the work once.
   Each AUR package also requires source review and its own build/install approval.
4. Run `ops doctor` to check persistent workstation readiness without changes.

Empty application lists are valid; ops still manages Git, SSH, and GitHub setup.
Git identity input, an SSH key passphrase, and GitHub device authentication are
interactive when first needed.
Successful setup ends with `Workstation ready.` A subsequent run with nothing
to do ends with `Workstation already ready.` and needs no approval. Declining
the top-level setup approval ends with `No changes made.`

## Configuration

```toml
# apps.toml format, independent of the ops binary version.
version = 2

pacman = ["librewolf", "steam"]
aur = ["mullvad-browser-bin"]
flatpak = ["com.tutanota.Tutanota"]
```

Lists may be omitted or empty. Identifiers are exact and case-sensitive:
there is no fuzzy matching or fallback between sources. Unknown fields,
malformed identifiers, duplicates, and contradictory pacman/AUR declarations
are rejected before changes. Removing a declaration never uninstalls software.

The `version` field identifies the apps.toml format, independently of the ops
binary version. Format 1 requires a manual migration; ops never migrates or
rewrites your file.
See [configuration](docs/configuration.md) for the schema, identifier discovery,
and format 1 migration guidance.

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

Ops is not a general package manager or an app store. It does not replace
pacman or general AUR tooling, provide catalog/search commands, or manage
arbitrary SSH hosts. Applications are declared explicitly in apps.toml using
identifiers from the selected source; ops never chooses another source as a
fallback.

Official/AUR package installation is preceded by one full, interactive `pacman -Syu`.
Wave D's [independent review](docs/wave-d-independent-review.md) has unresolved
provenance findings; it is not ready for a PR.
The general upgrade uses all configured repositories, including custom repositories,
so their available rebuilds are included. Subsequent managed official installations
use only `core`, `extra`, and enabled `multilib`.
Installed official readiness means a match to current official sync metadata,
not proof of historical repository origin. Flatpak readiness requires the enabled
canonical user Flathub remote and the application's `flathub` origin.
Prerequisites are installed and checked before dependent work. AUR source review
precedes its build-dependency installation and normal-user build. A final
reinspection determines whether the workstation is ready.
See [architecture and dependency policy](docs/architecture.md).

The default output shows a short setup summary and high-level progress. A ready
workstation needs no confirmation. AUR builds open a separate paginated source
view: Enter advances, `b` goes back, and `q` skips that application. Review
PKGBUILD and the auxiliary tracked source files, then explicitly approve the build
and install (default: no). Declining or skipping one AUR package allows other
approved work to continue; a skipped declaration can leave setup incomplete.
Ctrl-C cancels the entire run. See [AUR review and trust](docs/workstation-security.md#aur).
Unrelated local SSH keys, agent identities, and GitHub keys are preserved without
per-key prompts. Key removal is a separate user-managed operation.

## Commands

| Command | Purpose |
| --- | --- |
| `ops` | Reconcile declared applications and managed workstation configuration |
| `ops doctor` | Check persistent workstation readiness without making changes |
| `ops update` | Update ops itself to a newer verified signed stable release |
| `ops --help` / `-h` | Show command help |
| `ops --version` / `-v` | Show the installed ops binary version |

Exit codes: `0` success or top-level decline; `1` actionable issues, unavailable
checks, or incomplete work; `2` invalid configuration, unsafe state, or a fatal
operation failure (including interruption or lost interactive input). No-op runs
and Doctor do not require a TTY. Setup changes and available updates require a
terminal. Use `ops --help` for all commands; command-specific help flags and
extra arguments are rejected with exit 2.

## Doctor and updates

Doctor is read-only: it never runs sudo, installs software, logs in, or edits
files. `Workstation healthy.` means the managed persistent configuration and
state pass its checks. It does not mean the network is reachable, ssh-agent is
running, a key's passphrase is unlocked, or current GitHub SSH authentication
succeeded. Missing applications may require read-only queries to their declared
sources. Setup separately checks the remote GitHub state it needs.

`ops update` checks for a newer stable ops release. When one exists, a single
approval covers download, verification, and installation to `/usr/local/bin/ops`;
signature and checksum verification finish before sudo or binary replacement.
This updates ops itself; it does not update workstation applications or change
the apps.toml format.

## Safety model

Release signatures require the exact embedded signing fingerprint; there is no
checksum-only fallback. The setup summary discloses required dependencies and
system work. Sudo is acquired only after approval and only for privileged work.

AUR instructions are untrusted: review pinned files before approving a build.
Approved build instructions run as your normal user and can access your files.
Setup never deletes SSH/GitHub keys. Managed files
use protected boundaries and atomic replacement; unrelated keys and host trust
are preserved. See [workstation security](docs/workstation-security.md) and
[release security](docs/release-security.md).

## Recovery / troubleshooting

Run `ops doctor` to inspect persistent state, address the reported cause, then
rerun `ops` when ready. Doctor diagnoses; it does not repair.

| Reported problem | Next step |
| --- | --- |
| Malformed or unsupported apps.toml | Correct the file using the [format documentation](docs/configuration.md); ops leaves it untouched |
| Exact identifier confirmed missing from its declared source | Check the identifier and selected source in apps.toml |
| Source/query outage or inconclusive lookup | Check source availability and retry when accessible; this is not evidence that the declaration is wrong |
| Build/install or other operation failure | Read the cause and any diagnostic excerpt, then inspect with Doctor before retrying |
| Final state still unmet, or final inspection unavailable | Follow the final report; an operation's successful exit alone does not prove readiness |

Eligible failures include short, sanitized diagnostic excerpts; sensitive output
may be withheld and already streamed output is not repeated. AUR build output
is not replayed because reviewed code can print private data. Final reinspection
reports actual state alongside earlier failures, including when an operation
reported an error but its intended state is now present. Retrying is not a
guarantee of success.

Ctrl-C stops subsequent work; completed changes are not rolled back as a whole.
Lost input never grants approval. A failed core upgrade stops dependent work;
unrelated application failures may allow other approved work to continue.
For pacman failures, resolve the error and complete a full `sudo pacman -Syu`;
never use a standalone partial upgrade. If AUR sources change, rerun ops and
review the newly resolved source. Never bypass signature or SSH host-key checks.

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
sh -c 'for script in script/*.sh; do sh -n "$script" || exit; done'
```

Tests use temporary homes, fake command boundaries, local HTTP servers, and
ephemeral test keys. CI keeps checkout/build tools outside its minimal Arch
runtime. Full VM convergence is a separate gate:
[pre-ops acceptance procedure](docs/vm-acceptance.md).

Licensed under the MIT License.
