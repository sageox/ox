# 0003: Persistent high-score table

Status: Accepted

Date: 2026-10-01

## Context

Players want a top-10 table that survives restarts: name, score, best streak,
date, and difficulty reached. A single-player terminal game needs storage that
is small, portable, safe against crashes, and changeable later.

This decision extends [ADR-0001](0001-game-architecture.md) and
[ADR-0002](0002-adaptive-difficulty.md). The engine stays free of I/O, clock
reads, and randomness; Python 3.9 and the standard library remain the only
requirements; stored scores identify mode and rules version; local files are
not tamper-proof; and adaptive practice stays separate from fixed-challenge
rankings. `AdaptiveGame.ranked_eligible` is `False`, and the CLI currently runs
only the fixed ranked `Game`, which has no difficulty. A single table with a
difficulty column would therefore mix incomparable runs, so the table is
split by mode (see Decision).
<!-- SOURCE: sageox adr:docs/decisions/0001-game-architecture.md -->
<!-- SOURCE: sageox adr:docs/decisions/0002-adaptive-difficulty.md -->

## Options compared

| | JSON file | SQLite | shelve / pickle |
|---|---|---|---|
| Survives a crash mid-write | Yes, with write-to-temp, fsync, atomic replace | Yes, journaled | No: in-place writes, and `dbm` may span several files |
| Same on every platform | Yes | Yes, but a binary file | No: the `dbm` backend varies by platform |
| Format evolution | Explicit `schema_version` and migrations | `PRAGMA user_version` and migrations | Fragile: pickles bind to class paths |
| Safety of loading | Plain data | Plain data | Unpickling can run code |
| Fit for about 10 rows | Right-sized, hand-editable | Overkill | Wrong tool |

SQLite is the only serious alternative. Its built-in crash safety is real, but
atomic replacement gives a 10-row file the same guarantee, and a user can read
or repair JSON by hand. Revisit SQLite if we ever keep full run history or need
queries across it.

## Decision

Store the table in one JSON file, replaced atomically on every save, in a new
standard-library module `math_blitz/scores.py`. The engine does not import it.
`runner.py` builds a `ScoreEntry` from a finished game and calls the store; the
timestamp is the only wall-clock read and happens in the runner.

### Location

Per-user data directory by platform convention, resolved without third-party
packages:

| Platform | File |
|---|---|
| macOS | `~/Library/Application Support/math-blitz/scores.json` |
| Linux | `$XDG_DATA_HOME/math-blitz/scores.json` (if absolute), else `~/.local/share/math-blitz/scores.json` |
| Windows | `%LOCALAPPDATA%\math-blitz\scores.json`, falling back to `%APPDATA%` |

`MATH_BLITZ_HOME` overrides the directory (tests, portable installs). Scores
are machine-local data, so Windows uses the non-roaming location.

### Boards and eligibility

A board is the entries sharing a `(mode, rules_version)` pair, ranked by score,
then best streak, then earlier date, and trimmed to 10. Other boards in the file
are never touched.

- **`ranked`** is the only board wired into the CLI today. Its `difficulty` is
  `null`, because fixed runs have no difficulty, and the column is hidden.
- **`adaptive`** is supported by the format and renderer for when adaptive
  practice is wired in. There `difficulty` is the highest family level selected
  (the zero-based catalog index of ADR-0002). It remains a separate board, so
  adaptive runs never compete with fixed-challenge runs.

Only a completed 20-question timed run of a ranked-eligible game is recorded.
Untimed practice, quit, EOF, Ctrl-C, and adaptive runs are not. A score of zero
never qualifies. The game-over screen prints the table for eligible runs and
asks for a name only when the run makes the top 10; a tie ranks below existing
entries. Enter, EOF, or a timeout at the name prompt saves as `Anonymous`;
Ctrl-C declines to save. Names are stripped of non-printable characters
(blocking terminal escape sequences) and limited to 20 characters.

### Crash-safety rules

1. **Never write in place.** Write the full document to a temporary file in
   the same directory, `flush`, then `fsync` the file *before* `os.replace`.
   A crash before the replace leaves the old file; after it, the new one. The
   fsync is required: without it a power loss can leave a zero-length file
   after the rename on ext4 and APFS.
2. **macOS uses `F_FULLFSYNC`.** Plain `fsync` does not flush the drive cache.
3. **Windows retries `os.replace`** a few times on `PermissionError`, since
   antivirus or indexers can briefly hold the target.
4. **Sync the directory** on POSIX after the replace, best effort.
5. **Clean up the temp file** on any failure and re-raise.
6. **Worst case is losing the newest score, never the table.**
7. **Unreadable data is never silently overwritten or fatal.** Invalid UTF-8,
   invalid JSON, a wrong shape, or any malformed entry moves the whole file to
   `scores.corrupt-<UTC timestamp>.json`, the game says so, and play continues
   with an empty table.
8. **Save errors never end the game.** The runner reports `OSError` and still
   shows the table.
9. **Concurrent games take an exclusive lock around the whole
   read-modify-write.** `add` holds an OS advisory lock (`flock` on POSIX,
   `msvcrt.locking` on Windows) on a separate `scores.json.lock` file while it
   reloads, merges, trims, and replaces. Atomic replacement protects readers
   from torn files; it does not stop two writers working from the same old
   contents. See the lost-update gotcha below.
10. **The lock is never on `scores.json` itself.** That file is replaced on
    every save, and a lock held on a replaced file excludes no one.
11. **Loading also takes the lock**, because loading may move a damaged file
    aside; that must not race with another game saving a fresh one.
12. **The lock cannot go stale or hang the game.** The OS releases it if the
    holder crashes. Waiters poll for at most 15 seconds, then fail with
    `OSError`; the runner reports "Could not save" and the game ends normally.
    The empty `scores.json.lock` file is left in place on purpose.

### Format and versioning rules

```json
{"schema_version": 1, "entries": [
  {"mode": "ranked", "rules_version": 1, "name": "Ann", "score": 3810,
   "best_streak": 20, "date": "2026-10-01T22:12:24Z", "difficulty": null}
]}
```

- `schema_version` versions the file layout; `rules_version` versions scoring
  rules. They are independent. Bumping `rules_version` starts a new board and
  keeps old scores on disk, since results under different rules are not
  comparable.
- Changing the layout means bumping `SCHEMA_VERSION` and adding a
  `MIGRATIONS[n]` function from version n to n+1. Loading runs the chain in
  memory and never writes. The next save first copies the original to
  `scores.json.v<n>.bak` (once), then writes the new version.
- A file with a **newer** `schema_version` than the running game is read-only:
  its readable entries are shown with a warning, and nothing is saved, so an
  older build can never destroy newer data.
- Unknown fields on entries and at the top level are preserved on save, which
  allows purely additive changes without a version bump.
- Dates are UTC ISO 8601 strings, not monotonic time.

## Consequences

- Scores are plain data that tests exercise with injected paths, entries, and
  timestamps. No test needs the real file, a terminal, or real waits.
- The format is human-readable and editable; it is also trivially forgeable.
  This matches ADR-0001: the table is a local keepsake, not a ranking
  authority.
- The CLI shows only the ranked board until adaptive practice is wired in. The
  adaptive board's "difficulty reached" definition (highest level selected
  across operations) needs an engine accessor or runner tracking when that
  work happens; `AdaptiveGame` does not expose it today.
- Windows behavior (path resolution, `os.replace` retry) is covered by mocked
  tests only and needs a real Windows smoke test, as with the terminal adapter
  in ADR-0001.
- We do not fsync-verify hardware; a drive that ignores flush requests can
  still lose the latest write.

## Gotchas

- **Two games at once could lose a score (found and fixed; regression tests
  exist).** The first version reloaded the file inside `add()` and believed that
  made concurrent games safe. It did not. The sequence *read file, add entry,
  write file* is a read-modify-write, and atomic `os.replace` only makes the
  final write indivisible. Games A and B both read the same table, each add
  their own entry, and each replaces the file; whichever replaces last silently
  erases the other's score. The reload merely shrank the window, but that window
  includes JSON serialization and `fsync`, which take milliseconds, so it was
  wide, not narrow. With four processes saving 12 scores each, only 13 to 17 of
  48 survived, and a shared top-10 board came out wrong on every run. The root
  cause was treating "atomic file replacement" as "safe concurrent update".
  Atomic replacement guarantees a *consistent* file, not a *complete* one. The
  fix is mutual exclusion over the whole read-modify-write (rules 9 to 12).
  Do not reintroduce a path that loads, modifies, and saves without the lock;
  `save()` writes the in-memory table as is and exists only for completeness.
- **The same race applied to damaged files.** Two games starting together could
  both read a corrupt file; the first would move it aside and save a good one,
  and the second would then move the good file aside. Locking `load()` closes
  it; a forced-interleaving test covers it deterministically, because the
  stress test alone catches it only by luck.
- **Advisory locks assume a local filesystem.** `flock` is unreliable on some
  network filesystems. Keep the data directory local; the file is machine-local
  by design. The Windows lock path is untested on a real Windows machine.

- **Tests must set `MATH_BLITZ_HOME` or pass a path.** Otherwise they would
  write the developer's real score file.
- **Ties favor the incumbent.** A run equal in score and streak to the 10th
  place does not qualify; a run equal to a higher place ranks just below it.
- **Keystrokes typed during the final question** are flushed before the name
  prompt so they do not become the name.
- **A migration must be pure** (dict in, dict out) and must preserve unknown
  fields; load can run it repeatedly until the file is saved.
