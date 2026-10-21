# Plan review certification

[#1294](https://github.com/sageox/ox/issues/1294), audited 2026-10-20.
Source tests pass. Release and consuming-host certification remain pending.
The [Ledger contract](../specs/plan-review-ledger-contract.md) defines the format;
its v0.20.0 and `651ac29a` measurements describe those older builds.

## Versions

| Component | Version measured |
|---|---|
| CLI source | `5a1ff9e1ae2de406265f8b74ddb6aa7250063cf9` plus this change |
| Go module | `pkg/planreview/v0.4.0`, commit `c8dea6ae29abcf6585f5f740fc6f3c2e58a33b86` |
| Browser package | `packages/plan-review/v0.6.0`, commit `96371222406a24a552030043aad6b06d736c4ba8`, protocol 3; [publication receipt](https://github.com/sageox/ox/actions/runs/39497717414) |
| Embedded source digest | `45fcb6044d28725176504dfc1cbacc4f3672de4584476b0fe19c3655ce38675f`; file hashes are in `internal/plan/assets/review.manifest.json` |
| Released CLI | [v0.23.0](https://github.com/sageox/ox/releases/tag/v0.23.0), commit `25bef8e5759e086d84c7d90d0d3257b1f72816ae`; lists drawing labels/notes but cannot author drawings |

No fully capable CLI release is certified yet. The new `resolution-ids` fixture
and transport tests need a new Go module tag; they are not in v0.4.0.

## Test matrix

| Behavior | Tests | What they check |
|---|---|---|
| Stored records | `TestContractFixtures`, `TestReviewContractFixtures` | 120 rounds, both resolution layouts, record names, malformed/oversized input, remaps and unknown fields |
| Transport reads | `TestContractFixtures` | Reversed inventories and reconstruction from feedback files alone agree; missing reads remain incomplete |
| Retried and conflicting IDs | `same-id`, `v2-records`, `resolution-ids`; `TestPrior_AnswersARetryFromWhatIsStored` | Keep all copies, use original acceptance time, report changed payloads/reviewers |
| Local file access | `TestPublish_*`, `TestCapture_*`, `TestReadEntry_NeverWaitsOnAFIFO` | Create-only writes, confined paths and incomplete reads |
| Concurrent clones | `TestPlanReviewDurability_RacingClonesAndLostResponses` | Three clones preserve rounds and resolutions through moved heads and lost responses |
| All three tools | `TestPlanReviewDurability_MixedToolsRecoverAcrossClones` | Element, highlight and drawing survive failed commit/push, restart, verification and reopen; retry bytes stay unchanged |
| Commit scope | `TestPlanReviewDurability_RetryPublishesOnlyTheOriginalRecord`, `TestPlanReviewCommit_CarriesOnlyTheRecord`, `TestFlushPending_*` | One operation cannot publish authoring edits or clear another operation's retry |
| Lifecycle and save retry | `TestPlanLifecycle_*`, `TestPlanSave*` | Append-only event writers and failed-save recovery |
| Session stop | `TestReconcileProducedPlansAtStop_CommitsOnlyTheFilesItWrites` | Staged and unstaged authoring edits stay local |
| GC recovery | `TestGC_CarriesPlanRetriesIntoTheNewClone`, `TestGC_PlanRetriesRetainedWhenCarryIsRefused`, `TestGC_PlanRetriesSurviveInterruptedSwap` | Preserve both clones' entries, retain refused/conflicting copies, recover interrupted swaps automatically |
| Browser outbox | `state.test.mjs`; Chrome `UnstorableReviewIsNeverSent`, `LostAnswerAndAnEditDuringSendDeliverOnce`, `TwoTabsKeepEachOthersMarks` | Storage failure, account/tab isolation, frozen requests, lost acknowledgments and newer drafts |
| Untrusted content and permissions | Chrome `PlanCannotDriveTheComposer`, `LedgerTextCannotStyleOrScriptTheRail`, `ForgedCapturesAreCheckedAndRestamped`, `ResolveOfferedOnlyToReviewersWhoMay` | Plan/record input cannot control the host; local reviewers may only verify; hosted authorization is host-owned |
| Drawings | `TestRegion_SharedCases`, Node `region.test.mjs`, seven drawing Chrome tests | Bounds, placement and independent reopen; `ViewOriginalAfterAnEdit` checks the visible truncation notice, retained text/ink/boxes and inert markup with the source iframe removed |
| Shared format and assets | Go/Node anchor and region vectors, Node protocol tests, Chrome document vectors, `TestReviewAssets_MatchTheirManifest`, `make plan-review-check` | Go/JS agreement, protocol 3, source/asset hashes, stale-asset rejection and reproducible offline package installation |

Root tests use the released module in `go.mod`. Test `pkg/planreview` separately:
root `go test ./...` does not enter nested modules.

```sh
make plan-review-check
go -C pkg/planreview test -race -count=1 ./...
go test -race -count=1 ./internal/plan
go test -race -count=1 -timeout=10m ./cmd/ox -run 'Test(PlanReviewDurability_|PlanReviewCommit_|FlushPending_|RunPlanFeedback|ReviewJSON_|BrowserRoundTrip_|ReconcileProducedPlansAtStop_)'
go test -count=1 -timeout=10m ./cmd/ox -run 'Test(PlanLifecycle_|PlanSave)'
go test -race -count=1 -timeout=5m ./internal/daemon -run '^(TestGC_|TestTriggerGC_|TestCheckAndRunGC_|TestLedgerSyncWedged_)'
python3 - <<'CHECK'
import subprocess
deps = subprocess.check_output(
    ['go', '-C', 'pkg/planreview', 'list', '-deps'], text=True).splitlines()
assert not any(d.startswith('github.com/sageox/ox/internal/') for d in deps), deps
CHECK
make test-browser
make lint
make test
```

The combined race run exceeded ten minutes on large save/upload fixtures;
those run separately above. CI checks that all 22 selected Chrome tests pass,
so missing Chrome or `dist/` cannot pass through skips. Check local skips too.

Measured browser scope: mouse in desktop Chrome/Chromium, on local pages and
sandboxed frames. Touch, pen, Safari, Firefox and consuming-host UIs are untested.
Drawing context is inline; there is no attachment pipeline. GC tests simulate
process interruption, not power cuts. GC syncs copied entries before deleting
sources on POSIX. Unsupported hard links or conflicting bytes retain the source
and report failure.

## Local receipts

Logs: `.context/1294-certification/` (gitignored). These results do not certify a
release or a consuming host.

| Check | Result |
|---|---|
| Public Go module | Race tests and vet pass. Deliberately dropping failures, trusting transport order or ignoring reviewer conflicts makes tests fail |
| npm and embedded assets | `make plan-review-check` passes: 37 Node tests, reproducible packing, offline install, file hashes, exports and consumer types. Local tarball SHA-256: `ea747cbb3959c38e16459027ff9e3f218e3d4d55606cc24bce5c848e961ba03a` |
| Stale asset | Clean copied source passes; altering `review.js` makes `build.mjs --check` fail |
| Chrome | 53 tests pass. Removing the truncation notice makes its regression fail |
| CLI binary | Nine default-limit fixtures agree through `feedback show --json` and render successfully. The nested module tests `oversized` with its custom limits. Binary SHA-256: `ebad6d2f557dba2fa4bc67839b0762eec2e67851d986aaa6625e0d22ddc8540c`; worktree build reporting 0.23.0, not the release asset |
| GC | Race tests pass; loss, refusal and interrupted-swap regressions first failed against the unsafe implementation |
| CLI recovery | Review/concurrency race suite and lifecycle/save suite pass; staged/unstaged session-stop cases pass |
| Lint | Passes with isolated golangci-lint 2.14.0 built with Go 1.27.0; installed 2.11.4 cannot read Go 1.27 export data |
| Repository fast tier | 25,547 passed, 1,985 skipped, zero failures |

## Remaining release work

| Dependency | Required result |
|---|---|
| [#1409](https://github.com/sageox/ox/issues/1409) / [#1444](https://github.com/sageox/ox/pull/1444) | Cleanup/session upload exclude staged and unstaged authoring files; stranded saves recover with consent; retain #1406 save/retry behavior |
| [#1442](https://github.com/sageox/ox/issues/1442) | Session-ID backfill, GitHub migration and unpushed-commit/LFS squash exclude staged plan edits |
| [#1443](https://github.com/sageox/ox/issues/1443) | Ignored-artifact cleanup succeeds while real content deletions remain guarded |
| [#1348](https://github.com/sageox/ox/pull/1348) | Finish the existing README correction for public package visibility |
| [ADR-025](../adr/ADR-025-plan-annotation-and-feedback-delivery.md) | Record Ryan's review of the drawing's `q-<id>` anchor; #1325's merge is not sign-off |

After these merge, repeat the matrix. Publish readers before writers using the
existing Go tags, npm workflow and CLI release workflow. Record the fixture tag,
module checksum, npm protocol/source digest/tarball integrity/attestation, and
first fully capable CLI tag and binary checksum. Those release receipts are pending.

Each consuming host must pin the releases through its existing dependency update
process, run the contract's §5 transport probes and its UI tests, and record the
owner, versions, run links and browser/input scope. Host receipts are pending.

For rollback, disable new writers first and retain all records and retry bytes.
Keep readers that understand existing records. v0.20.0 misses resolutions and
incomplete reads; v0.23.0's drawing labels do not qualify its authoring/recovery
behavior. A tested rollback pair remains pending.
