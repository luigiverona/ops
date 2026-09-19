# Wave E implementation security review

Reviewed against baseline `50adbe8518aee4108ee7bd7b6532e6a9ad9daf7f` on
`fix/process-tree-cancellation`, using Go 1.26.7 and the retained architecture
proofs. This is the implementer's review; independent review is still required.

## Critical

No unresolved Critical findings.

- **Atomic admission:** CLONE_INTO_CGROUP is requested on every active-owner
  Start, including capability probes, sudo refreshes, bwrap, verification and
  updater recovery. There is no post-fork migration or unowned retry. Failed
  Start retains the original error and cleans only a proven-empty group.
- **Mutation gate:** top-level approval precedes activation; activation precedes
  sudo and mutation. Update verification/download is deliberately after activation.
  Missing/failed activation closes mutation admission. Doctor/no-op/decline remain
  independent. All production Exec construction occurs at DefaultRuntime; IO
  rebinding preserves its pointer. Wrapper copies preserve the controller.
- **Poisoning:** scope/capability or cleanup uncertainty is typed, permanent and
  shared. Admission and short persistent file changes serialize with poisoning.
  Core, per-application, identity and update flows stop on that classification;
  cleanup errors joined to cancellation cannot be discarded by sudo Keeper.Close.
- **Authority:** the ordinary user creates child groups and writes cgroup.kill,
  including UID-0 descendants and both sudo monitor/launcher processes. No
  privileged ownership operation or host sudo grant was introduced.

## Important

No unresolved Important findings caused by the implementation.

The following findings during implementation were corrected and retested:

1. Population can briefly remain nonzero after direct-child reaping. The inert
   activation probe now polls up to the population bound. Natural exit uses a
   short accounting-settle interval before diagnosing surviving descendants.
2. SIGKILL of sudo's PTY monitor can leave caller termios changed. Failed
   interactive commands restore the supplied TTY's saved modes; actual sudo PTY
   ordinary/race tests verify subsequent input and exact termios restoration.
3. Reconstructing Exec for terminal IO would lose the shared controller.
   WithIO and wrapper tests cover the default runtime, trusted source and
   cancellation wrapper; both Prepare and Update use the preserving method.
4. A single independent exit error must remain single for existing absent-state
   classifiers. Error composition preserves that shape; compound cancellation
   or cleanup failures retain their separate identities.
5. Keeper failures can race persistent file changes. SSH changes/deletions and
   GnuPG persistent-home preparation use the shared mutation lock, including
   after command-based inspection. Update success waits for keeper cleanup.
6. Public GnuPG tools legitimately auto-start helpers. Their explicit ephemeral
   declaration preserves synchronous public-key operations while still requiring
   kill/population-zero/removal before return. Classic and keyboxd repeats pass;
   updater signature verification avoids helper startup entirely.
7. The disposable VM initially lacked repository metadata for the existing
   installer-render test. A local fixture Git snapshot corrected the validation
   environment; the ordinary suite then passed with sudo revoked.

Additional review:

- Scope bootstrap subscribes before StartTransientUnit and matches its job,
  verifies returned unit properties and actual current-PID membership. It uses
  the user bus directly, has a finite activation deadline, and does not alter
  persistent systemd state or replay planning/approval.
- Cgroup names are generated internally. Retained root/directory descriptors and
  no-follow opens constrain interfaces to the command group. Production uses no
  descendant PID scan. Empty nested-directory cleanup is confined and bounded.
- cgroup.events, including nested population, is authoritative. Kill failures are
  ignored only when a fresh observation proves the group already empty. Removal
  and reap/drain failures poison; occupied/unproven groups are preserved.
- pidfd observation prevents inherited descriptors from hiding direct-child exit.
  WaitDelay remains secondary; natural descendant leakage is cleaned and reported.
  Unfinished buffers are not read if the exceptional reap/drain bound expires.
- Completion/cancellation arbitration has synchronized tests. Both canceled and
  deadline identities survive composed run.Error/exit/cleanup errors. Native
  failure injection preserves exit code 7 together with cleanup poison.
- No process-group/session attributes or foreground-group changes are added.
  The PTY fixture explicitly signals the owner because sudo can route keyboard
  Ctrl-C inside its own PTY. No stronger keyboard-interrupt claim is made.
- Output streaming, capture limits, interactive declarations, diagnostic privacy
  and Bubblewrap flags/no-fallback remain intact. Full ordinary and focused race
  suites retain the existing output/privacy tests.
- Manager-created services are deliberately outside descendant ownership. A
  native service test proves this distinction and explicitly stops its fixture.
- New native regressions use a dedicated build tag and exact invocation, adding
  no skipped test to the ordinary suite. Permanent privileged tests run before
  the existing cloud-init grant is removed; normal CI still verifies no sudo.
- Original evidence was hashed and committed intact before appending the
  implementation report. Secret/credential marker review found none. The 424 KiB
  original evidence is small reproducible source, structured results and reports;
  no VM image, private key or bulk build output is committed. The one preserved
  patch's blank context line needs its leading space; a file-specific attribute
  preserves those bytes while all production whitespace checks remain enabled.

## Optional / explicit follow-ups

- I-11's existing privacy stress cost remains unchanged. The initial focused
  race invocation timed out at ten minutes; a 30-minute timeout rerun passed in
  566.216 seconds. No complete repository race suite was run locally.
- Hard-crash/SIGKILL recovery needs a separate product decision. The transient
  scope persists while descendants live; the crash fixture proves this and
  performs test-only cleanup. No permanent supervisor is introduced.
- Uninterruptible kernel tasks and arbitrarily blocked supplied IO cannot be
  forcibly reaped/unblocked by Go. Cleanup reports failure and poisons rather
  than asserting success. These documented limits are outside the successful
  cleanup guarantee, not weaker fallback modes.
- Deliberately malicious root/same-UID cgroup manipulation remains outside the
  lifecycle threat model. Manager-created service recovery is operation policy.
- Protected CI's authoritative complete race run remains for later publication.
  This session executed the production privileged regression locally in a fresh
  real-Arch KVM guest, without pushing or invoking a new GitHub Actions run.
