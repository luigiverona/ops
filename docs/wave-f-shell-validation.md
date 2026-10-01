# Wave F — shell validation correctness

Starting main/origin/main: `dda0bdf6ea363a290ee8c18837f35b0e94e9a1d5`.
Branch: `fix/shell-validation-correctness`. Evidence recorded 2026-10-01.
Preflight after fetch/prune: clean main, no stashes, no other local/remote
feature branches, no open PRs; protected `ci`, `build`, and `minimal-runtime`
checks successful on the starting SHA, strict protection enabled.

## Inventory and root cause

Whole tracked-tree searches covered literal shell commands, Go subprocess
arguments, syntax/noexec references, workflows, script references, shebangs,
and executable modes. These were all active syntax-validation sites at baseline:

| Site | Baseline behavior | Wave F disposition |
| --- | --- | --- |
| `README.md`, Development | Per-file loop, but sends Bash script to `sh` | Retain loop; enumerate five POSIX files and check Bash separately |
| `script/test-ci-arch.sh`, Build and shell validation | Multi-file `sh -n` checks only `install.sh` | One `sh -n` invocation per POSIX file, propagate failure |
| Same CI block, `bash -n` | Correct single-file Bash check | Unchanged |
| `internal/installer/installer_test.go`, `TestPOSIXSyntax` | Multi-file invocation skips `prepare-release.sh` | Test-local per-file helper and malformed-second-file regression |
| `internal/installer/publish_release_test.go`, `TestPublishReleasePOSIXSyntax` | Correct single-file `sh -n` | Unchanged |
| `internal/installer/render_install_test.go`, `TestRenderInstallScriptSyntax` | Correct single-file `sh -n` | Unchanged |
| `script/render-install.sh`, rendered temporary installer check | Correct single-file `sh -n` | Unchanged |

The first non-option shell operand is the command file; later operands become
its positional arguments. `-n` disables execution but does not turn those
arguments into additional command files. The fix uses one parser per script.
The small Go helper is confined to the existing test file and lets the new
regression exercise the same validation as `TestPOSIXSyntax`.

`.github/workflows/ci.yml` delegates to the guest helper;
`.github/workflows/arch.yml` runs the minimal-runtime script but contains no
syntax validator. The release workflow runs the existing Go suite. No other
active syntax-validation site or equivalent multi-file bug was found.
The multi-file command in the historical Wave E
`propagation-and-fixture-corrections.md:151–153` is retained as historical
evidence; it did **not** prove syntax validity of its later POSIX operands.
The canonical handoff's O-01 references describe the finding, not commands.

Actual shebangs and direct parse results:

| Tracked script | Shebang | Syntax exit status |
| --- | --- | --- |
| `script/install.sh` | `#!/bin/sh` | 0 |
| `script/prepare-release.sh` | `#!/bin/sh` | 0 |
| `script/publish-release.sh` | `#!/bin/sh` | 0 |
| `script/render-install.sh` | `#!/bin/sh` | 0 |
| `script/test-minimal-arch.sh` | `#!/bin/sh` | 0 |
| `script/test-ci-arch.sh` | `#!/bin/bash` | 0 |

No additional tracked shell files, including extensionless executable scripts,
were found. Historical patch content and generated test fixtures are not
standalone current shell scripts. At baseline no script was wholly absent
from validation commands, but the CI POSIX command missed four files and the
Go test missed `prepare-release.sh`; `test-minimal-arch.sh` relied on the README
for an effective check. All six now have correct README and CI parser coverage.

## Disposable reproduction and regression evidence

This exact bounded reproduction was run outside the repository. Here `/bin/sh`
resolves to Bash; the expected command-file semantics were confirmed.

```python
from pathlib import Path
import subprocess, tempfile
with tempfile.TemporaryDirectory(prefix="ops-wave-f-", dir="/tmp") as root:
    p = Path(root)
    (p / "first.sh").write_text("#!/bin/sh\n: valid\n")
    (p / "second.sh").write_text("#!/bin/sh\nif then\n")
    for args, expected in [
        (["sh", "-n", "first.sh", "second.sh"], 0),
        (["sh", "-n", "first.sh"], 0),
        (["sh", "-n", "second.sh"], 2),
    ]:
        result = subprocess.run(args, cwd=root, capture_output=True)
        assert result.returncode == expected, result
```

The actual CI validation block was extracted without executing the guest
bootstrap. A disposable copy of the six scripts passed; appending `if then`
to each non-first POSIX file in turn failed with exit 2. The Bash file failed
with exit 2 when similarly malformed. The README commands produced the same
results. The original CI block still exited 0 with malformed
`prepare-release.sh`, demonstrating the before/after difference.

To repeat the corrected CI malformed-later-file check from the repository root:

```python
from pathlib import Path
import subprocess, tempfile
repo = Path.cwd()
source = (repo / "script/test-ci-arch.sh").read_text()
block = source.split("go build ./...\n", 1)[1].split("printf '::endgroup::", 1)[0]
with tempfile.TemporaryDirectory(prefix="ops-wave-f-", dir="/tmp") as root:
    p = Path(root)
    (p / "script").mkdir()
    for script in (repo / "script").glob("*.sh"):
        (p / "script" / script.name).write_bytes(script.read_bytes())
    assert subprocess.run(["bash", "-e", "-c", block], cwd=p).returncode == 0
    later = p / "script/prepare-release.sh"
    later.write_text(later.read_text() + "\nif then\n")
    assert subprocess.run(["bash", "-e", "-c", block], cwd=p).returncode == 2
```

Permanent regression: `TestPOSIXSyntaxRejectsMalformedLaterScript` first
requires both valid fixtures to pass, then corrupts only the second and
requires failure identifying it. The focused tests passed. A temporary Go
overlay restored multi-file semantics in the test helper; the regression then
failed with `malformed second script must fail validation: <nil>` and
`go test` exit 1. Repository sources were not changed for this mutation proof.
All disposable fixtures/overlays were removed; no raw logs are committed.

## Validation and scope

Pinned environment: `PATH="$HOME/.local/opt/go1.26.7/bin:$PATH"`, `GOENV=off`,
`GOTOOLCHAIN=local`; `go version` reported `go1.26.7 linux/amd64`.
`go env GOENV GOTOOLCHAIN` reported an empty GOENV filename and `local`.

| Validation | Result |
| --- | --- |
| Five POSIX files with `sh -n`, Bash file with `bash -n` | PASS, each exit 0 |
| Extracted CI and README validation, valid and malformed later files | PASS, expected exits 0 / 2 |
| `go test -count=1 ./internal/installer -run 'Test(POSIXSyntax\|PublishReleasePOSIXSyntax\|RenderInstallScriptSyntax)' -v` | PASS |
| Temporary Go overlay restoring the bug | Regression correctly fails, exit 1 |
| `go mod verify` | PASS, all modules verified |
| `go test -count=1 ./...` | PASS |
| `go vet ./...` | PASS |
| `go build ./...` | PASS |
| `git diff --check` | PASS |
| Both executable evidence snippets above | PASS |

Self-review against the starting SHA: Critical 0, Important 0, Optional 0
within Wave F scope. O-01 is implementation-complete on this local branch;
it is **not closed**. Independent review, protected CI, merge, and merged-tree
verification remain required. The canonical roadmap is unchanged pending that
merged transition.

Only validation, its regression, and documentation change. No production Go,
subprocess ownership, cgroups, sudo, package/AUR behavior, SSH/Git/GitHub/GPG
behavior, installer runtime, release signing/publishing, signal/trap behavior,
terminal UI, toolchain/dependency pins, R2, or preserved VM changes. No Wave G
or later work. The complete local race suite and VM CI were not run for this
narrow change; protected CI is a later review gate.
