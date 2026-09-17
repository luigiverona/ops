# Final independent Wave D review — 2026-09-16

## Recovery and reviewed input

Recovered state A: clean `fix/package-source-provenance`, HEAD
`cd0a6c36a9d387fd24d1059bbcb58acf5986a1a7`, no stashes, no later commits.
Fetch preserved `origin/main` at `2fc8614171d8c335ade870d045da4e73f44b79bd`.
No reset, clean, restore, rebase, amend, squash or work deletion was performed.
The four requested reports and the complete 84-file baseline diff were read.

Outside Git, `/tmp/ops-wave-d-final-review` contained an overlay and three test
files (`directory_test.go`, `inspect_test.go`, `signature_test.go`), but no report
or fixes. These were preserved and rerun before any production edit. The directory
mode, unverified core execution and native future-signature probes all failed.
The existing Go 1.26.7 at `/home/ah/.local/opt/go1.26.7/bin` is used throughout
with `GOENV=off`, `GOTOOLCHAIN=local`; the system default is a different version.

## Findings recorded before corrections

### D-F1 — Important: directory permission drift passes installed verification

Location: `internal/archtrust/content.go`, `matchEntry`.
Reproducer: an authenticated `private` directory specifies 0700; install it with
0777 and call `matches`. The recovered `TestFinalDirectoryMode` returns true.
All directories receive the shared-directory exception, without proving sharing;
even shared executable ancestors can become writable by other users.
Invariant: security-relevant managed permissions must remain equivalent.
Smallest reliable fix: require authenticated directory mode too; do not invent
a sharing exemption from local attacker-controlled ownership metadata. A pacman
reinstall that preserves an unsafe directory must fail its postcondition and
require manual permission reconciliation.

### D-F2 — Important: inspection executes unverified core package programs

Location: `internal/inspect/inspect.go`, `Local` and `External`.
Reproducer: `OfficialInstalled` returns false, but an SSH private-key fixture
causes `ssh-keygen` to execute; installed `github-cli` alone causes `gh` to
execute. Both recovered `TestFinalUntrustedCoreExecutables` subtests fail.
Invariant: core provenance gating must precede dependent use, including Doctor
and pre-approval inspection. Smallest fix: gate SSH discovery/config inspection
and GitHub commands on the same authenticated matches already used for git and
Flatpak; retain safe post-repair rediscovery and no overwrite behavior.

### D-F3 — Important: future package-signature creation time is accepted

Location: `internal/archtrust/signature.go`, `signatureStatus`.
Reproducer: a packager with three real disposable main-key certifications signs
with `--faked-system-time` one day ahead. GnuPG verification succeeds and ops
accepts its VALIDSIG; recovered `TestFinalNativeFutureSignature` fails.
Invariant: only currently usable signatures may authorize package evidence.
Smallest fix: parse and enforce VALIDSIG creation/expiration timestamps, with
malformed and expired values rejected. Retain GnuPG's subkey/binding/revocation
cryptographic checks and independently test them.

### D-F4 — Important: ordinary cache eviction forces unnecessary reinstallation

Location: `internal/archtrust/query.go`, `Source.CachedInstalled`.
Reproducer: converge a package with a matching authenticated cache archive;
remove that archive; the ENOENT branch returns `(false, nil)` without inspecting
the installed content, so all readiness callers plan repair. Every subsequent
cache cleanup repeats this churn. The existing documentation explicitly confirms
it, and the synthetic orchestration fixtures cannot detect it because they bypass
production archive acquisition.
Invariant: missing reconstructible evidence is not evidence of changed installed
content. Smallest fix: retrieve the snapshot-bound archive into disposable,
unprivileged storage for read-only authentication and comparison when cache
evidence is absent or has a mismatching digest. Never populate the pacman cache
or reinstall merely to inspect. Retrieval/trust failures remain inconclusive.

### Review qualifications requiring explicit disposition

- Distribution keyring material is an existing bootstrap assumption, but the
  phrase “entire trust base” is imprecise. A custom archlinux-keyring replacement
  alone can change all three files. Document this exact exclusion, including
  that ownership and stable reads cannot prove historical keyring authenticity.
- The live-database resolver test fails here because it inherits a configured
  repository without its DB. Replace that environmental dependency with isolated
  synthetic native provider databases; preserve actual libalpm execution.
- Resource review must account for archive retrieval, hashing/decompression,
  cancellation and temporary storage, beyond only HTTP response limits.

No finding is waived by the earlier green report. Final dispositions and fresh
validation follow below after corrective work.

## Resume recovery and additional findings

This resume recovered state D at `b37da126422214c9273b6cf26ac1d34c5af15bc7`:
three corrective commits after `cd0a6c3` (`bde9942`, `12fa39b`, `b37da12`),
unstaged changes in archtrust content/prepare/query/signature tests, and untracked
`final_cache_test.go`. Nothing staged and no stashes. Fetch retained the required
baseline. All original files, commits and `/tmp/ops-wave-d-final-review` were
preserved; copies of the recovered diffs/untracked test and new logs are in
`/tmp/ops-wave-d-resume-bVr7U4zL`.

### D-F5 — Important, unresolved: repository version drift becomes content repair

Locations: `internal/archrepo/query.go:InstalledMatch` (metadata comparison),
`internal/archtrust/source.go:Lookup`, `internal/archtrust/query.go:CachedInstalled`,
`internal/plan/plan.go:coreState`, and `internal/resolve/applications.go`.
Reproducer: install genuine official git N with its unchanged payload and valid
archive evidence; advance only the trusted repository to N+1. `InstalledMatch`
returns `(false, nil)` before asking for authenticated content. Doctor reports
official repair/reverification, and setup schedules a full upgrade followed by
an unconditional core reinstall even if the upgrade has already established N+1.
The same preliminary comparison affects declared apps and AUR dependencies.

Invariant: ordinary repository advancement is not evidence of forged installed
content, missing evidence, or a need to repair that content. The current source
stores only one version per name; neither a cached N archive nor its detached
signature can be selected through the production API once the snapshot is N+1.
Smallest complete correction needs version-aware authenticated installed evidence,
a distinct update/unknown classification through Doctor/planning, and reinspection
after the already approved full upgrade before deciding to reinstall. A historical
official source/retention policy is needed when the old archive is absent. Merely
accepting the local N tuple or labeling every old version ready is unsafe. This
review does not invent that product/evidence policy: **BLOCKED**.

### Validation defects in recovered review work

The new deadline wrapper hides the old deterministic context observer used by
`TestInstalledReplacementDuringVerification`; neither mutation actually ran.
Keep the deadline and cancellation checks, expose the inner comparison to the
package-local test, and assert that the deterministic mutation fired. Both rename
and in-place probes still require an inconclusive error, never readiness.

`TestRealPacmanProviderPrintFormatInIsolatedDatabase` copied the host's configured
repository assumptions and failed for a missing local DB. Use disposable native
libalpm databases with cargo, a transitive base-devel edge and versioned Java
provider; retain a conflicting custom provider. No host sync data is required.

The distribution-keyring exclusion is now explicit in the source contract:
custom replacement of `archlinux-keyring` alone can change the trust base. It is
not necessary to replace the entire platform. No keyring authentication claim is
derived from root ownership, stable reads, or package ownership metadata.

### D-F6 — Important: transaction preparation has no aggregate temporary-disk bound

Location: `internal/archtrust/prepare.go:Source.Prepare`, archive accumulation.
Each archive is limited to 8 GiB, but every selected archive remains open/on disk
until the complete transaction closes. Many individually valid selected identities
can consume an arbitrarily large transaction total before native pacman performs
its installation checks; privileged staging and cache copies add further storage.
Reproducer: two snapshot-bound 5 GiB package identities both pass the individual
size policy and preparation starts acquiring them without an aggregate check.
Invariant: new Wave D evidence acquisition needs an explicit total disk budget,
not merely an individual response limit. Smallest correction: preflight the entire
selected snapshot identity list against an 8 GiB cumulative archive budget before
any download or keyring processing; reject excess as unavailable preparation,
never absence or repair. This uses the existing largest supported archive limit
as the transaction ceiling and changes no package/dependency/toolchain policy.

## Final resume disposition — 2026-09-17

**BLOCKED: zero Critical findings; one unresolved Important finding (D-F5).**
The original I-07/I-08 definitions supplied by the user are authoritative; they
came from an external audit, not a tracked file. I-07 concerns accepting custom
repositories as official, loss of repository identity, unqualified transactions,
and native/foreign classification as provenance. I-08 concerns missing app-origin
and complete Flathub source/enabled-state checks and unsafe namesake preservation.
Both original findings are closed under the documented bootstrap boundary. This
does not waive D-F5 or establish readiness for a PR.

### Corrective history and remaining finding

| Finding | Severity | Disposition |
| --- | --- | --- |
| D-F1 directory permissions | Important | Fixed in inherited `bde9942`; directory mode regressions pass. |
| D-F2 executing unverified core tools | Important | Fixed in inherited `12fa39b`; dependent execution is gated. |
| D-F3 package-signature lifetime | Important | Fixed in inherited `b37da12`; native future-signature and lifetime probes pass. |
| D-F4 cache-eviction reinstall churn | Important | Fixed in `8334b1f`; read-only evidence reconstruction and repeated readiness pass. |
| D-F5 repository advancement misclassified as repair | Important | Unresolved. `internal/archrepo/final_version_test.go` deliberately fails; retain it until version-aware evidence/classification is designed. |
| D-F6 aggregate archive storage | Important | Fixed in `625a60d`; over-budget transactions stop before acquisition. |

Other resume commits are `b60f40d` (bounded authentication/content verification),
`985d855` (isolated native provider/trust probes), `fd274c2` (native revocation and
expiry probes), and `f3eb1c1` (backup policy across versions). The recovered
unstaged work is incorporated with its tests, not discarded. A final report/test
commit records the blocker; its hash is available from Git history rather than a
self-referential hash in this file.

The D-F5 regression uses real isolated pacman metadata queries and the fixture's
explicit authenticated-content capability: the N control succeeds, then only
trusted sync metadata changes to N+1. The production preliminary comparison
returns definite mismatch before invoking content authentication. Separate native
GPG/archive tests establish actual byte/signature verification. This regression
is not an end-to-end installed historical-archive authentication test; that path
is precisely what production lacks. No acceptance of old metadata alone is proposed.

Optional follow-ups: use stronger types for qualified targets across API boundaries;
keep core repository mapping, fixed endpoint availability and pinned Flatpak
keyring serialization under maintenance. These are not waivers of Important work.

### Required security and behavior verdicts

| Area | Evidence and result |
| --- | --- |
| I-07 | Closed for the supplied original definition: independent source bytes establish official identity; repository-qualified targets and transaction membership survive resolution; native/non-foreign state alone never establishes readiness. General upgrades intentionally retain custom repositories. |
| I-08 | Closed for the supplied original definition: app origin, canonical source, enabled state and complete supported trust configuration are checked; wrong namesakes fail; safe add/enable work is visible before approval. |
| D-R1 | Closed: matching local metadata alone cannot pass; signed official archive content and inventory are required. Cache loss no longer causes reinstall by itself. D-F5 remains an independent false-mismatch defect. |
| D-R2 | Closed: full Flathub URL, commit/summary verification, subset/filter/collection and pinned bootstrap key material are checked. |
| D-R3 | Closed: general `-Syu` uses configured custom repositories; restricted official corrective transactions use independent evidence. |
| D-R4 | Closed: local `[core]`/`[extra]`/`[multilib]` names, URLs and databases cannot establish existence, dependencies, providers, members, filenames or digests for official operations. Conflicting native fixture databases exercise this separation. |
| D-R5 | Closed: Flatpak inventory requires a read-only bubblewrap boundary; legacy state remains unchanged and unavailable isolation has no fallback. |
| D-R6 | Closed: traversal inspects satisfied transitive official dependencies instead of stopping at `pacman -T` success. |
| D-R7 | Closed: approved AUR provider bindings are retained through final inspection and drift revalidation. |
| Source and snapshot | Fixed HTTPS endpoint, normal hostname validation, rejected redirects, validated repository/filename components and bounded input. Immutable selected package identity includes size, SHA256 and embedded signature. Changed download bytes or missing archives fail inconclusively. There is no separate detached-signature HTTP fetch to race. The three repository DB fetches are not a globally atomic mirror snapshot; per-package identity remains coherent, and dependency/partial-upgrade checks may reject mixed availability. A new source snapshot after successful full upgrade is intentional. |
| OpenPGP | Native GPG cryptographic verification plus policy checks cover primary/subkey binding, primary/subkey revocation, usable self-signed UID, UID/certification revocation and expiry, key expiry and package-signature creation/expiry. Three certifications means three distinct approved main fingerprints on one usable UID; duplicates, non-main issuers and certifications split among UIDs do not count. Native and synthetic malformed/weak/unsupported-policy probes fail closed. This is reviewed policy with regression evidence, not a formal proof of all OpenPGP inputs. |
| Distribution keyring | Reads reject symlink ancestors/leaves, nonregular/multiply linked files, oversized evidence and observed changes; stable rereads check replacement during verification. **Yes, a custom repository replacing archlinux-keyring can change ops's trust base.** Authentic distribution keyring material is a prerequisite; replacing that bootstrap component alone is explicitly outside the guarantee in package-source-provenance.md. Stable reads cannot establish historical authenticity. |
| Custom signer | A key merely trusted by local pacman never counts as official. Tests use disposable keys; only distribution-approved distinct main certifications satisfy ops policy. |
| Installed equivalence | Tests cover executable/library/regular content changes, same-size different bytes, missing entries, regular/symlink substitutions and changed targets, owner/mode/setuid/setgid, hostile ancestors, directory mode and deterministic rename/in-place races. Production checks expected directory type. Forged equal-version content remains not ready. Concurrent privileged mutation after the final observation is outside an atomic-snapshot guarantee. |
| Owned inventory | Authenticated archive inventory must match local owned paths and actual inspected filesystem entries. Extra owned files fail. Shared directories get no broad permissions exemption. Unowned files and historical hook side effects remain outside content equivalence. |
| Backup exception | Only authenticated PKGINFO backup metadata permits edited bytes. Type, existence, owner and mode still match. Forged local backup declarations cannot exempt executables; missing/symlink/wrong-owner/mode backups fail. A new-version fixture removing backup status rejects modified content despite stale local backup metadata. |
| Cache evidence | Cached paths alone confer no trust: descriptor, size/digest, signature and stable-file checks apply. Missing or digest-mismatching evidence is reconstructed in disposable storage. Source/signature failures remain unavailable; no persistent cache population occurs during inspection. |
| Cache deletion/idempotence | Signed-archive probe establishes readiness, removes the archive and repeats successful inspection without repair; corrupted-cache fallback and failed reconstruction are tested. This holds for the current selected identity while its archive is obtainable. |
| Version drift | **D-F5 open:** genuine N is classified as mismatch after the repository moves to N+1. Doctor and setup inherit the wrong classification; setup's pre-upgrade plan can reinstall after a successful full upgrade. Historical evidence/retention and update classification require an explicit decision. |
| Equal-version repair | Pre-approval visible work, independent archive authentication, no `--needed`, preserved reasons, protected staging and strong postcondition are covered by native/mocked layer tests. Matching second inspection is idempotent. No real privileged package repair or VM mutation was performed. |
| Core packages | git, openssh, github-cli and conditional flatpak share the strong predicate; forged/unavailable evidence gates dependent use. Real public-source archive verification passes for all four. Version drift affects this shared gate. |
| AUR dependencies/providers | Direct/exact/virtual/versioned/multiple/transitive provider cases and retained final bindings are covered. Provider/repository/member drift aborts; archive/signature and installed-payload drift cannot use a metadata-only fallback. Historical-version classification remains affected by D-F5. |
| Flatpak regressions | Wrong URL/origin, restricted subset, disabled summary verification, altered trust, legacy read-only inspection and unavailable isolation cases pass. No persistent user remote/app was mutated. |
| Doctor | No live sync, installation, key import, repository mutation, sudo repair or privileged staging. Metadata/archive downloads and disposable GPG/native query scratch are allowed read-only evidence acquisition. Unverified core tools are gated. |
| Planning | Build consumes data, sorts deterministically and performs no I/O. Source inspection belongs to earlier resolution/inspection. D-F5 supplies incorrect input classification; purity alone does not repair it. |
| Errors | Source/trust/signature/cache/keyring/filesystem failures propagate as unavailable rather than absence or automatic repair; absent-package inventory remains distinct. Confirmed content mismatch is repair work. D-F5 violates the desired update-versus-mismatch distinction. |
| Resource bounds | DB: 32 MiB compressed/256 MiB expanded each; description: 1 MiB; archive: 8 GiB; transaction archive total: 8 GiB before acquisition; manifest: 64 MiB/1 MiB line; regular-file hashing: 32 GiB; signature: 64 KiB; distribution evidence: 16 MiB/file; native capture: 2 MiB. HTTP timeout, GPG timeout and authentication/content deadlines bound work with cancellation checks. Entry count/path storage are indirectly bounded by bounded metadata, parser validation and filesystem limits, rather than a dedicated entry counter. Protected/cache copies increase disk consumption by a bounded factor. Space failures and cleanup are covered; maximum-size stress and every blocking filesystem behavior were not exercised. No unrelated I-11 fix was attempted. |

GPG status/colon-output interpretation was cross-checked against upstream
[GnuPG DETAILS](https://github.com/gpg/gnupg/blob/master/doc/DETAILS); the distribution
certification policy was compared with Arch's
[keyring trust implementation](https://github.com/archlinux/archlinux-keyring/blob/master/libkeyringctl/trust.py).
Native disposable-key probes supplement interpretation of status records.

### Validation and preserved evidence

All review Go commands used Go **1.26.7**, `GOENV=off`, `GOTOOLCHAIN=local`.
No toolchain or dependency was changed. `go version` confirms that version;
`go env GOTOOLCHAIN GOENV` prints `local` and an empty GOENV path with GOENV off.
`go mod verify` passes and `gofmt -l .` is empty.

New/expanded probes reside in archtrust's `final_cache_test.go`,
`final_bounds_test.go`, `final_packets_test.go`, `final_backup_test.go`,
`signature_test.go` and `content_test.go`; the native provider fixture was made
independent of host sync databases. Inherited directory, signature-policy and
core-execution regressions remain. `archrepo/final_version_test.go` preserves the
unresolved reproducer without skip/xfail.

The focused eight-package run passes archtrust, arch, resolve, inspect, plan, app
and flatpak. Archrepo fails only `TestFinalRepositoryAdvanceIsNotInstalledContentMismatch`
(D-F5). The full `go test -count=1 ./...` likewise fails only that regression;
all other package results pass. `go vet ./...`, `go build ./...` and
`git diff --check` pass. The public read-only `TestOfficialArchIntegration`, enabled
explicitly with `OPS_ARCH_INTEGRATION=1`, passes against real signed archives for
acl, git, openssh, github-cli and flatpak. It performs no installation.

Targeted race was not run during this resume because the requested prerequisite
of zero unresolved Important/Critical findings is not met. Earlier implementation
race results are historical, not fresh final-review evidence. Full repository
race was omitted because of known I-11 cost; protected CI remains required later.

Fresh command logs and recovered diffs are preserved in
`/tmp/ops-wave-d-resume-bVr7U4zL`, including `focused-complete.log`,
`full-complete.log` and `public-integration.log`. The original review evidence
folders remain. The baseline-to-final diff, inherited changes and new corrections
were reviewed; no Wave E implementation was introduced.

Remaining uncertainty is explicit: old-version evidence policy is unresolved;
no preserved-VM or real root installation exercised repair; filesystem checking
is not an atomic root-attacker snapshot; maximum-size resource stress and a fresh
race run are absent. These limits are not converted into passing claims.

The branch remains `fix/package-source-provenance`; main and existing history
were not changed. No push, PR, merge, tag, release, publication, R2 access,
preserved-VM mutation, release signing, toolchain/dependency upgrade or next-wave
action occurred. Disposable package-signature and existing release-unit-test
fixtures use synthetic test keys only. Final Git identity/status are reported
in the review response after the report commit.

Wave D final independent review complete: BLOCKED.
