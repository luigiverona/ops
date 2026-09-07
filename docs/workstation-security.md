# Workstation security

## Approval and privilege

Detection, configuration validation, inspection, resolution, and planning do
not install prerequisites. The top-level plan is approved before sudo is
requested. EOF is not approval. Pacman owns the interactive full-upgrade
transaction, including replacement/provider/key-import decisions. Later
privileged commands use the acquired noninteractive sudo authorization;
normal-user Git, Flatpak, SSH, GitHub, and makepkg operations do not use sudo.

There is no automatic rollback of a complete workstation run. Atomic replacement
protects individual managed files; after a partial failure actual state is
rediscovered. Failed core prerequisites stop dependent work.

## AUR

AUR HTTPS source discovery validates the exact package base and a single pinned
Git revision without invoking Git. The smart-protocol parser rejects malformed,
duplicate, truncated, zero-object, and trailing advertisements. Remote responses
are bounded; invalid JSON or `.SRCINFO` cannot authorize an install.

Application fetch checks the pinned object ID, displays terminal-sanitized
tracked files for explicit review, and compares declarative metadata and file
contents before execution. The current source is never silently substituted.
Normal-user makepkg receives EOF stdin and cannot delegate package installation
through an interactive helper. Providers, version expressions, output closure,
and concrete official dependency transactions are revalidated before mutation.
Source/compiler dependency drift fails closed. Unselected/debug package outputs
are excluded; staged root-owned copies and their package metadata are validated
before privileged artifact installation.

Source signing keys use exact full primary PGP fingerprints. Public keys are
retrieved through the fixed HKPS endpoint into an isolated keyring, checked,
then imported into the normal user's keyring only after approval. Existing
classic and keyboxd-backed storage is inspected without creating or changing
the user's keyring. No private release signing material is used for AUR.

Review is a trust decision, not a sandbox: an approved PKGBUILD executes as your
user and can access that user's data. Do not approve sources you do not trust.

## SSH and GitHub

The managed Ed25519 identity is `~/.ssh/ops` / `ops.pub`. Discovery pairs key
material by exact fingerprint. Symlinked SSH directories or managed identities
and nonregular managed targets are rejected. Unrelated files, authorized keys,
certificates, sockets, and ordinary known_hosts are not rewritten. When setup
requires key review, keeping unrelated identities is the default; deletion
names exact files and requires a separate default-no confirmation. Agent
unloading never deletes files.

A marked dispatcher in `~/.ssh/config` preserves the user's configuration in
`ops_user_config` for hosts other than GitHub. Isolated `ops_config` and
`ops_known_hosts` enforce the managed identity, `IdentitiesOnly yes`, and strict
host-key checking for GitHub. Keys come from validated official HTTPS metadata;
effective configuration is verified with `ssh -G`. Unmarked/conflicting managed
files are not overwritten. Temporary metadata unavailability never triggers a
host-key rewrite; malformed authoritative metadata fails closed.

GitHub device authentication is owned by `gh`. Ops does not itself store tokens
and requests `admin:public_key`, the minimum additional permission needed for
account SSH-key reconciliation. Existing insufficient sessions are explicitly
planned for refresh. Account keys are compared by exact fingerprint and
reinspected after login; failed inspection cannot authorize deletion or blind
registration. Unrelated remote deletion has separate default-no confirmation.
The managed key has a fingerprint-derived title; duplicates are avoided and
SSH access is verified after setup.

## Releases

Installer/updater trust, exact signing-subkey fingerprint, fail-closed signature
and checksum verification, and atomic binary replacement are independent of
workstation preparation. See [release security](release-security.md). Candidate
CI artifacts are unsigned and do not change production trust or hosting.
