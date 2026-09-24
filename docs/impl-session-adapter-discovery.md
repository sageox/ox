Fix Claude and Pi adapter discovery for sessions stored in a different native project bucket or Pi's current directory layout. Repository selection from a parent directory is out of scope.

- [x] Claude: locate an exact native session ID in another Claude project bucket only when session metadata confirms a single requested repository.
- [x] Pi: support current and legacy directory names and timestamp-prefixed session filenames; validate session header ownership.
- [x] Add positive and cross-repository refusal tests.
- [x] Run adapter tests and inspect diff without committing or pushing.
- [x] Require Claude repo metadata for direct and timestamp-based lookups as well as cross-bucket lookup.
- [x] Remove the unscoped stop-time Claude scan that bypasses adapter checks.
- [x] Apply Pi's mtime filter before reading a candidate's header.
- [x] Reject Pi's newest-file fallback when a specified agent has no matching session; exact native ID still works.
- [x] Revalidate Claude before hook/daemon writes and final drains, preserving the cursor on mismatch.
- [x] Ignore an unterminated JSONL tail until it is complete so discovery and incremental reads can retry without losing a turn.
- [x] Pass both adapter test suites, scoped hook/stop/recovery tests, and diff-scoped lint.
- [x] Skip malformed/oversized Claude JSONL records only when the adapter skips them too; reject malformed or foreign ownership metadata on capturable turns.
- [x] Validate only newly read turns during hook/watcher capture, with a full scan at watcher start and before final import/upload.
- [x] Validate after the non-incremental read so turns appended while reading cannot be imported unchecked.
- [x] Allow legacy daemon recovery without WorkspacePath only if the captured header repo ID matches the daemon project; otherwise preserve data for manual ownership review.
- [x] Quarantine confirmed untrusted Claude sources without deleting recordings; skip repeated watcher, hook, and daemon scans.
- [x] Apply per-turn ownership checks during full-file validation too; reject nested repositories and deleted cwd paths.
- [x] Guard native reads against source replacement and prevent stop/recover from publishing quarantined caches.
- [x] Recheck dead hook-mode sources before daemon finalization; quarantine proven foreign turns and keep unverifiable deleted cwd paths retryable.
- [x] Refuse daemon Pi recovery and watcher restart when repository ownership is absent.
- [x] Preserve undiscovered Claude hook recordings through daemon recovery, explicit recover, and stale/ghost cleanup; test the header-only case.
- [x] Pass full `make lint` and focused adapter/capture/daemon tests.
- [x] Pass `make test` on current `origin/main` with reduced local concurrency (`OX_TEST_P=2 OX_TEST_PARALLEL=4`); 22,392 tests, no failures.
- [x] Keep the 4 MiB pre-push redaction regression in the slow tier (passed with `-race`); retain a 128 KiB line check in the routine tier.
- [x] Isolate Pi runtime signals in agent-detection tests so they pass inside Pi as well as CI.
- [x] Preserve upstream draft-liveness and native carrier-stamping behavior while integrating Claude quarantine and safe hook recovery.
- [x] Describe the user-visible capture fix in the Unreleased changelog.

The existing Claude session contains parent/sibling cwd records, so this strict cross-bucket lookup intentionally rejects it. Handling mixed-repository sessions is deferred.
