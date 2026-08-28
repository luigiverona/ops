# Release security

## Trust model

Each release consists of `ops-linux-x86_64`, `checksums.txt`, and
`checksums.txt.sig`. The detached signature authenticates the checksum
manifest; the exact entry in that authenticated manifest then authenticates the
binary with SHA-256.

Installer and updater use an isolated temporary GPG keyring and require GPG's
`VALIDSIG` status to name the exact pinned release-signing subkey. Arbitrary
keys in the user's normal keyring are never trusted.

The intended hierarchy is an offline primary key and dedicated release-signing
subkey. Private material must never be committed or placed in general CI.
Candidates are built in CI, downloaded to an isolated signing environment,
reviewed and signed offline, and only then published.

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
7. Build a candidate, sign its reviewed manifest offline, and validate the
   complete installer and updater path before publication.

## Rotation

Routine signing-subkey rotation is certified by the offline primary. Ship a
release trusted by the old key that contains the new public material, publish
the new fingerprint independently, then sign later releases with the new
subkey. Update the hosted installer through a separately reviewed deployment.

## Revocation

Prepare primary-key revocation material with offline backups. If a signing
subkey is compromised: revoke it with the primary, stop publication, remove
affected artifacts, publish the incident/revocation independently, rotate the
subkey, and require a trusted recovery path. Never fall back to checksum-only
installation or a key fetched from an arbitrary keyserver.

