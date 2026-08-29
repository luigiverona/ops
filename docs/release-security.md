# Release security

## Trust model

Each release consists of `ops-linux-x86_64`, `checksums.txt`, and
`checksums.txt.sig`. The detached signature authenticates the checksum
manifest; the exact entry in that authenticated manifest then authenticates the
binary with SHA-256.

Installer and updater use `gpg` with `--no-options` inside an isolated,
permission-restricted temporary GPG home. Public-key inspection, import, and
signature verification all use that isolated home; the user's normal keyring
and `gpg.conf` are not consulted. The pinned fingerprint must identify an
active, signing-capable subkey in the embedded public key.

Verification consumes GnuPG status output, not human-readable diagnostics. It
requires exactly one `VALIDSIG` for the exact pinned signing-subkey fingerprint
and a successful verification process. `REVKEYSIG`, `EXPKEYSIG`, `EXPSIG`, bad
or error signature states, missing status, multiple signatures, and wrong
signers are rejected. A checksum is considered only after this signature
decision succeeds; there is no checksum-only fallback.

The intended hierarchy is an offline primary key and dedicated release-signing
subkey. Private material must never be committed or placed in general CI.
CI builds unsigned candidates for independent validation. CI is not a
production-artifact trust root, and arbitrary CI-produced bytes must never be
signed merely because a workflow uploaded them.

## Current bootstrap status

```text
release signing fingerprint   not yet assigned
embedded public key           not yet provisioned
release hosting               not yet provisioned
```

This is deliberately fail-closed. Go trust variables are empty and the
installer contains explicit rendering markers. Neither accepts unsigned
content. Releases must not be advertised until provisioning is complete.

## Provisioning

1. Generate and securely back up an offline certification-capable primary key.
2. Generate a signing-only subkey with an appropriate expiration.
3. Export only public key material.
4. Independently verify the full 40-hex signing-subkey fingerprint.
5. Embed the export/fingerprint in Go and render the installer from the same
   reviewed values.
6. Publish the fingerprint through an independent authenticated channel.
7. Exercise the complete installer and updater path before publication.

## Trusted production release procedure

Production releases are built and signed in a dedicated trusted release
environment. `script/prepare-release.sh` encodes the required ordering and
fails before signing unless every precondition succeeds:

1. Start from a clean clone with no tracked or untracked changes.
2. Check out the exact intended release commit and create/review the intended
   `vMAJOR.MINOR.PATCH` tag. The helper requires that exact tag to identify
   `HEAD`.
3. Install the official `go1.26.7` toolchain independently of CI. For Linux
   x86_64, verify the official archive SHA-256
   `ffb5f8de10c62550dfddab66b36b57030721e0a44a3218e9e1181d7b59f121ca`
   before extraction. The helper
   disables per-user Go environment configuration and verifies
   `go env GOVERSION` and `go version` before doing any build work.
4. Run `go mod verify`, formatting validation, `go vet`, and the test suite.
5. Independently build `ops-linux-x86_64` with the pinned compiler,
   `CGO_ENABLED=0`, `GOOS=linux`, `GOARCH=amd64`, `-trimpath`, read-only module
   mode, VCS stamping, and the exact tag version in the binary.
6. Execute the binary and require `ops VERSION` from `--version`.
7. Optionally provide an extracted CI candidate binary path or authenticated
   HTTPS URL as the second helper argument. When supplied, byte-for-byte
   comparison is mandatory. A mismatch prints both SHA-256 values and refuses
   signing. The independent binary remains authoritative.
8. Generate the final checksum manifest locally from the independent binary.
9. Recheck that the repository did not change during preparation.
10. In the dedicated signing GPG home, require the exact active signing subkey,
    sign only that final manifest, and verify the resulting status against the
    same exact fingerprint.

The signing environment supplies only these runtime values; neither contains
private material or a passphrase:

```sh
export OPS_SIGNING_FINGERPRINT=40_HEX_SIGNING_SUBKEY_FINGERPRINT
export OPS_SIGNING_GNUPGHOME=/path/to/dedicated/offline/gnupg-home
script/prepare-release.sh 1.0.0 /path/to/extracted/ci/ops-linux-x86_64
```

Only the helper's final `dist/release-v1.0.0/` output is eligible for hosting.
The CI checksum manifest is informational and is never the manifest signed for
production.

## Rotation

Routine signing-subkey rotation is certified by the offline primary and must
be completed before the current signing subkey expires. Ship a release trusted
by the still-current old subkey that contains the new public material, publish
the new fingerprint independently, then sign later releases with the new
subkey. Update the hosted installer through a separately reviewed deployment.

## Revocation

Prepare primary-key revocation material with offline backups. If a signing
subkey is compromised: revoke it with the primary, stop publication, remove
affected artifacts, publish the incident/revocation independently, rotate the
subkey, and require a trusted recovery path. Never fall back to checksum-only
installation or a key fetched from an arbitrary keyserver.
