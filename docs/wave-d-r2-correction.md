# Wave D D-R2 corrective pass — 2026-09-14

## Recovery

Found state B on `fix/package-source-provenance`, HEAD
`2cd1ff365ac7e9493582782ce0d618ecfa194f99`. Nothing staged, no stashes, no
interrupted-session commits. Fetch retained origin/main at
`2fc8614171d8c335ade870d045da4e73f44b79bd`. All earlier commits were preserved.

Recovered modified files:

- internal/app/aur_order_test.go
- internal/app/doctor_test.go
- internal/app/output_test.go
- internal/app/prepare_test.go
- internal/app/provenance_test.go
- internal/flatpak/flatpak.go
- internal/flatpak/provenance_test.go
- internal/flatpak/review_test.go
- internal/inspect/inspect_test.go
- internal/plan/plan_test.go
- internal/plan/provenance_test.go

Recovered untracked files (the last two were one directory entry in git status):

- internal/app/flatpak_fixture_test.go
- internal/flatpak/flatpak_fixture_test.go
- internal/flatpak/identity.go
- internal/flatpak/identity_test.go
- internal/inspect/flatpak_fixture_test.go
- internal/testpkg/flatpak.go
- internal/testpkg/flatpakdata/config
- internal/testpkg/flatpakdata/flathub.trustedkeys.gpg

The interrupted parser, pinned keyring, fixtures and regressions were inspected
and retained. This resume strengthened file opening, native query bounds,
within-inspection trust consistency, the immediate pre-install check, native
Doctor coverage and hidden drift coverage. It also updated this evidence and
`docs/package-source-provenance.md` / `docs/wave-d-independent-review.md`.
No D-R1/D-R4 implementation or test was changed.

## Original failure

`options,name,url` JSON omitted effective subset and summary-verification policy.
Thus a canonical-looking URL/name with `xa.subset=verified` or
`gpg-verify-summary=false` could satisfy readiness. More CLI display columns
would still omit other source/trust fields and key material.

## Canonical observation

Two preserved temporary bootstrap installations and a fresh independent bootstrap
in `/tmp/ops-d-r2-resume-1dq7ztqq` produced identical persistent config and keyring.
The command was `flatpak remote-add --user flathub
https://dl.flathub.org/repo/flathub.flatpakrepo`, with FLATPAK_USER_DIR and all XDG
state directories directed into isolated temporary directories. No real user
Flatpak installation was queried or changed by the bootstrap. Native versions:
Flatpak 1.18.2, OSTree 2026.4.

```ini
[core]
repo_version=1
mode=bare-user-only
min-free-space-size=500MB

[remote "flathub"]
url=https://dl.flathub.org/repo/
xa.title=Flathub
gpg-verify=true
gpg-verify-summary=true
xa.comment=Central repository of Flatpak applications
xa.description=Central repository of Flatpak applications
xa.icon=https://dl.flathub.org/repo/logo.svg
xa.homepage=https://flathub.org/
```

Config SHA256: `df1d440549b320d05f4377500431568617002dcebf3956307933da82e0e7502f`.
Public keyring: 2,888 bytes; SHA256
`c504fa5dc891df6cfcc10021dd9addf08459007f1787f40b9c6fd3c7e58416ea`.
Read-only `gpg --show-keys` in a disposable GNUPGHOME identified:

- Primary: `6E5C05D979C76DAF93C081354184DD4D907A7CAE`
- Signing subkey: `54A6CDDD8919FB204200D8AC562702E9E3ED7EE8`
- User ID: `Flathub Repo Signing Key <flathub@flathub.org>`

No private key generation or signing occurred. Bootstrap imported only the public
key into its disposable installation. Production inspection never runs GPG.

## Exact supported predicate

`Manager.Remotes` combines strict native JSON with `inspectRemoteIdentity` and
`supportedFlathub`; `Remote.Canonical` and `Ready` supply the shared classification.

| State | Predicate / response |
| --- | --- |
| READY | User remote `flathub`, exact repository URL, complete supported persistent identity below, enabled; CLI options empty |
| CORRECTABLE | Same canonical identity, disabled; only CLI `disabled` permitted; enable after top-level approval |
| MISSING | No `flathub` in CLI or persistent config; absent installation remains absent; add after approval |
| INCOMPATIBLE | Any materially different source/trust policy; manual reconciliation, never automatic rewrite |

Malformed, ambiguous, unreadable, oversized, unsafe or inconsistent evidence is
an inspection error, never ready/missing by fallback. Existing wrong-origin apps
remain manual failures and are never destructively migrated.

Required repository core: version `1`, mode `bare-user-only`, no parent/inherited
policy. The only other supported core keys are `min-free-space-size`,
`min-free-space-percent`, `fsync`, `locking`, `lock-timeout-secs`; these describe
storage/locking, not remote trust. Unknown core keys make Flathub incompatible.

The exhaustive supported remote fields are:

| Field | Accepted effective value |
| --- | --- |
| url | Exactly `https://dl.flathub.org/repo/` |
| gpg-verify | Missing (OSTree default true), or true / 1 |
| gpg-verify-summary | Present and true / 1; missing defaults false and is incompatible |
| collection-id | Missing or empty |
| xa.subset, xa.filter | Missing or decoded empty |
| xa.disable | Missing/false/0 for ready; true/1 for correctable |
| xa.oci, tls-permissive, xa.noenumerate, xa.nodeps | Missing/false/0 |
| xa.subset-is-set | Either valid boolean; does not create a subset when its value is empty |
| xa.title, xa.comment, xa.description, xa.homepage, xa.icon | Optional valid GLib keyfile strings; values do not affect trust |
| Corresponding five presentation `-is-set` flags | Either valid boolean |
| Every other remote key | Incompatible (closed supported policy) |

Booleans use GLib's case-sensitive true/false/1/0 semantics and permitted edge
whitespace. Malformed booleans/escapes fail closed. Duplicate groups or keys are
rejected instead of adopting last-value-wins. This deliberately narrow keyfile
reader does not execute, interpolate, source or evaluate configuration contents.

### Subset and filter semantics

Flatpak normalizes missing and empty `xa.subset` to no subset. Every nonempty
value restricts source identity, including `verified`, `floss`, `verified_floss`,
literal `-`, and escaped whitespace. The CLI uses `-` as a missing-value display
placeholder too; the underlying config distinguishes the cases. `--subset=`
can persist an empty string with `xa.subset-is-set=true`, which is accepted.

Missing/empty `xa.filter` means no filter. Nonempty values name filter files;
`-` is a path, not an unrestricted sentinel. If a configured file disappears,
Flatpak can load `repo/flathub.filter`; missing or malformed effective files can
cause loading errors. Ops rejects every configured filter path without loading
its contents, including an initially permissive/empty file, because that mutable
external policy is outside the full-source contract. A leftover backup without
an effective configured path is ignored, matching Flatpak. Native tests cover
valid, permissive, malformed, empty and missing referenced files and backup state.
See the getters and `flatpak_dir_lookup_remote_filter` in the
[Flatpak 1.18.2 implementation](https://github.com/flatpak/flatpak/blob/1.18.2/common/flatpak-dir.c).

### Summary verification and collection interaction

The supported bootstrap has **no collection ID** and requires summary
verification. No raw-key exception applies to that mode: false/0 or missing
`gpg-verify-summary` is incompatible, independently of commit verification.
Empty collection ID is normalized to absence and accepted with summary checking.
Every nonempty collection ID, including `org.flathub.Stable`, is a different
source contract and incompatible with either summary boolean. Historical
collection-based modes which ignored summary policy are not inferred to be
canonical. Current Flatpak explicitly migrates older collection remotes toward
summary verification; its summary-index path consults the flag directly.
Tests cover canonical absent/empty collection modes and noncanonical combinations.
Sources: [Flatpak migration and index verification](https://github.com/flatpak/flatpak/blob/1.18.2/common/flatpak-dir.c),
[OSTree option defaults](https://github.com/ostreedev/ostree/blob/v2026.4/man/ostree.repo-config.xml).

### Other source and trust state

`contenturl` overrides content downloads separately from metadata URL. Any
persistent override, even empty or textually equal to the URL, is outside this
bootstrap contract. Metalinks, mirrorlist URLs, custom backends, custom CA/client
TLS paths, proxy settings, alternative gpgkeypath, branches, unconfigured state,
authenticators/options/install flags, token type, default branch/main ref,
priority, signature lookaside, redirect/deploy-collection/key-update fields and
unknown future options are incompatible. Explicit normal TLS and OSTree mode
are supported; permissive TLS and OCI mode are not. Nonempty per-remote
`flathub.cookies.txt` also makes source access policy incompatible.
See [OSTree remote options](https://github.com/ostreedev/ostree/blob/v2026.4/man/ostree.repo-config.xml)
and [Flatpak remote options](https://docs.flatpak.org/en/latest/flatpak-command-reference.html#flatpak-remote).

Required `repo/flathub.trustedkeys.gpg` must match the entire pinned SHA256.
Absent/empty/malformed/replaced/appended material fails closed; another remote's
keyring never supplies the evidence. OSTree selects a present per-remote keyring
instead of deprecated global keyrings; extra `gpgkeypath` and inherited policy
are rejected. This validates exact trust material without implementing OpenPGP.
Source: [OSTree verifier preparation and keyring lookup](https://github.com/ostreedev/ostree/blob/v2026.4/src/libostree/ostree-repo.c).

## Read-only, concurrency and mutation

D-R5's mandatory bubblewrap boundary is unchanged: read-only host root including
runtime/shared-memory paths, private process/network/IPC namespaces and `/proc`,
D-Bus endpoints disabled, no unsandboxed fallback. Both native inventory calls
now have a ten-second deadline, shortened by any caller deadline; output remains
bounded at the existing runner boundary. Needed native initialization/migration
fails closed. Doctor's complete path is tested against native inventory, with
byte/file-mode/mtime snapshots for canonical, legacy, restricted, malformed and
absent installation state.

Direct reads use descriptor-relative ancestor traversal without symlinks,
O_PATH to inspect file type without opening devices/FIFOs, descriptor reopening
for regular files only, O_NOATIME, and a 1 MiB limit per identity file. Access
permission failures are not bypassed. Config, keyring and cookie bytes are read
again and repository directory identity rechecked before accepting evidence.
CLI existence, URL and enabled state must agree with persistent config.

Add/enable require the approved missing/disabled state still to hold immediately
before their mutation. Install checks both origin and trust, then repeats trust
after app inventory immediately before mutation. All operations use the exact
same complete predicate afterward. Final workstation inspection uses it too.
Hidden summary/subset/filter/content/keyring drift is covered at all these gates.
This detects observed concurrent drift; separate reads and CLI mutations do not
provide atomic exclusion against a same-user process changing state after the
last check. No such atomicity is claimed.

## Validation and boundaries

All Go commands use `/home/ah/.local/opt/go1.26.7/bin/go`, `GOENV=off`,
`GOTOOLCHAIN=local`. No dependency/toolchain change. The exact final validation
results are recorded in the accompanying review checkpoint and session report.

The tests cover all 24 requested D-R2 categories, including the original native
summary/subset reproducers, trust material, full-source defaults, unexpected
options, ambiguity, special files, bounded FIFO queries, native Doctor,
pre/post/final drift and preserved origin/approval behavior. Existing fixtures
now supply persistent config/keyring alongside mock CLI output; CLI text alone
cannot manufacture evidence.

Intentional boundaries: exact serialization changes/key rotation require review;
there is no online revocation, current-summary fetch or installed-content audit.
Unknown or harmless-but-unsupported configurations may require manual
reconciliation. The normal system TLS store and installed Flatpak/OSTree tools
remain platform trust assumptions. Historical app origin/content authentication,
atomic mutation exclusion and D-R1/D-R4 are not newly claimed.

Full `go test ./...`, full repository race, signing/release tests, privileged
repair and VM/end-to-end mutation validation are deliberately excluded. D-R1 and
D-R4 are run separately and must retain their original failures. No push, PR,
merge, tag, release, publication, R2, VM mutation, signing, toolchain/dependency
upgrade or next-wave work is authorized or performed.

### Focused results

| Check | Result |
| --- | --- |
| go version / go env | go1.26.7 linux/amd64; local; environment-file mechanism disabled |
| go mod verify | All modules verified |
| gofmt -l . | Empty |
| go test -count=1 ./internal/flatpak | PASS, 1.651s |
| go test -count=1 ./internal/inspect | PASS, 0.013s |
| go test -count=1 ./internal/plan | PASS, 0.003s |
| go test -count=1 ./internal/app | PASS, 2.923s |
| go test -count=1 ./internal/run | PASS, 34.042s |
| ./internal/testpkg | Builds; no test files |
| go vet ./... / go build ./... | PASS |
| git diff --check | PASS |

Commands for focused packages were batched into one `go test` invocation with
all six explicit package arguments; no package or adversarial test was excluded.

### Preserved blockers (run separately)

`go test -count=1 -v ./internal/archrepo -run '^TestReviewRejectsForgedCustomPackage$'`
exited 1, FAIL (0.280s), with the original messages:

```text
D-R1: forged custom archive satisfies InstalledMatch
D-R1: forged custom package satisfies core and pacman declaration
D-R1: forged satisfier accepted for git: {Requirement:git Provider:extra/git Packages:[extra/git] Satisfied:true}
D-R1: forged satisfier accepted for ops-virtual: {Requirement:ops-virtual Provider:extra/git Packages:[extra/git] Satisfied:true}
D-R1: forged satisfier accepted for ops-virtual>=2: {Requirement:ops-virtual>=2 Provider:extra/git Packages:[extra/git] Satisfied:true}
```

Its custom/official archive hashes remain respectively
`5378cedd1b58272e6a7f550fc891612330397726faf0beb4e74c8011378ce683` and
`283af437b18fb352ae4e666657bc6069f81f6f355a0168760a1b48da20edc854`.

`go test -count=1 -v ./internal/archrepo -run '^TestReviewRejectsOfficialNameSpoof$'`
exited 1, FAIL (0.005s):

```text
D-R4: custom repository content trusted solely by official section names
```

### Targeted race results

| Package / selection | Result |
| --- | --- |
| internal/flatpak (entire package) | PASS, 2.820s |
| internal/inspect (entire package) | PASS, 1.042s |
| internal/plan (entire package) | PASS, 1.012s |
| internal/app (entire package) | PASS, 6.602s |
| internal/run (entire package) | PASS, 566.573s |
| internal/run, additional `-run '^TestReadOnly'` | PASS, 1.026s |
| internal/testpkg | Builds under race; no test files |

All five requested D-R2 packages were run in full with `-race -count=1` and
passed. The unchanged run package's existing diagnostics stress suite accounted
for the long runtime; it finished within the normal ten-minute timeout. Its
D-R5 read-only boundary also passed the additional targeted race invocation.
