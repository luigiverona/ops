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
