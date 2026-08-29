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
primary key fingerprint       62AC2C70AC1897C4E7E1A4E52F6B33C21650375C
release signing fingerprint   EB564BFFD8F63A984BF72A0237A80EDB682BBBFD
signing subkey expires        2027-08-29
embedded public key           provisioned
offline primary isolation     pending
release hosting               provisioned on Cloudflare R2
production release            not yet published
```

The reviewed public trust anchor lives in
`internal/release/signing-fingerprint` and `internal/release/signing-key.asc`.
Go embeds those files directly. `script/render-install.sh` renders the
standalone installer from the same reviewed values and fails if template
markers are missing, duplicated, or left unresolved.

The primary-key home and release-signing home are separate. The release-signing
home contains the usable signing-subkey secret but not the primary secret.
Before the first production release, the primary secret and revocation material
must be backed up to independent encrypted storage and the primary key isolated
from the networked release workstation. Provisioned hosting does not relax this
requirement, and no production release may be advertised until that isolation
is complete.

Release hosting uses the `ops-releases` Cloudflare R2 bucket and its public
custom domain. Bucket-scoped Object Read & Write credentials and the S3 API
endpoint exist only on the trusted release workstation. They must never be
stored in this repository or provided to GitHub Actions. CI remains limited to
building an unsigned candidate; production publication is always a local,
trusted-workstation operation using the AWS CLI.

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
11. From the same clean, exactly tagged checkout, run
    `script/publish-release.sh VERSION`. Supply the R2 S3 endpoint through
    `OPS_R2_ENDPOINT`; the AWS profile defaults to `ops-r2` and can be changed
    with `OPS_R2_PROFILE`. Keep the bucket-scoped profile and endpoint in local
    workstation configuration, never in the repository.
12. The publication helper independently repeats embedded-key, exact signing
    subkey, signature, signed checksum, and binary-version verification before
    any upload. It copies the verified release into a private temporary
    snapshot, verifies that snapshot again, and publishes only the snapshot. It
    renders `/install` from the tagged source rather than accepting an
    externally supplied installer.
13. Versioned release objects are created with conditional `PutObject` and are
    immutable. A retry accepts an existing object only when its bytes,
    `Content-Type`, and immutable `Cache-Control` exactly match the locally
    prepared artifact. Published immutable objects are not transactionally
    deleted if a later step fails; rerunning the helper safely resumes them.
14. Before any mutation, the helper reads authenticated R2 `releases/latest` as
    the rollback authority, validates its bytes and mutable metadata, and
    retains its ETag. The public HTTPS latest value must exactly agree with R2;
    only matching absence is accepted as the first-release state. Malformed
    state, access or network failures, disagreement, and semantic-version
    downgrades fail closed. Publishing the same version is a safe retry.
15. After immutable artifacts pass R2 and public HTTPS read-back, the helper
    publishes and verifies `/install`. It updates `/releases/latest` last with
    `If-None-Match` for an initially absent object or `If-Match` against the
    previously observed ETag. A concurrent change fails instead of being
    overwritten, and an earlier failure cannot advertise an incomplete release.
16. A nonblocking local `flock` in the repository's Git directory prevents two
    trusted-workstation publishers from interleaving `/install` and latest
    updates. The kernel releases the lock on normal exit, failure, signals, and
    crashes; the inert lock file does not represent stale ownership.

The signing environment supplies only these runtime values; neither contains
private material or a passphrase:

```sh
export OPS_SIGNING_FINGERPRINT=40_HEX_SIGNING_SUBKEY_FINGERPRINT
export OPS_SIGNING_GNUPGHOME=/path/to/dedicated/release-signing/gnupg-home
script/prepare-release.sh 1.0.0 /path/to/extracted/ci/ops-linux-x86_64

# Set OPS_R2_ENDPOINT in the trusted workstation environment from local
# configuration. OPS_R2_PROFILE defaults to ops-r2.
script/publish-release.sh 1.0.0
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
