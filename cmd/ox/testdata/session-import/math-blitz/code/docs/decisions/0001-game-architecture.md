# 0001: Math Blitz game architecture

Status: Accepted

## Context

The original `math_game.py` mixes input, output, timing, progression, and scoring.
Its monotonic clock is appropriate, but timeout is evaluated only after blocking
input returns. Invalid input retries also block expiry. We want more question
types, meaningful streak and speed rewards, and room for adaptive difficulty,
high scores, and a full-screen interface. Python 3.9 and the standard library
are required. SageOx enrichment found no prior team decision for this topic.

## Options compared

1. **Pure engine plus blocking line input.** Small and portable, but a soft
   deadline only rejects answers after Enter. Suitable for untimed practice.
2. **Pure engine plus standard-library timed input.** True prompt expiry without
   dependencies. POSIX uses `select` and terminal control; Windows uses console
   key polling. We must maintain editing, buffer cleanup, and platform tests.
3. **Pure engine plus prompt_toolkit.** Originally recommended for cancellable
   prompts and a future full-screen UI. Its dependency violates the subsequently
   specified standard-library constraint, so it is not selected.

Threads around `input()` are not a cancellation mechanism: the blocked reader
can survive timeout and steal later input. Unix alarms are process-wide,
main-thread-only, unavailable on Windows, and do not solve input buffering.

## Decision

Choose option 2. Keep `math_game.py` as the executable entry point. A small
`math_blitz` package separates a deterministic engine, question generation,
scoring policy, orchestration, and terminal input. The engine receives questions,
answers, and monotonic timestamps as values; it performs no I/O, clock reads,
sleeping, or random draws. Generation receives an explicit random source.

Each question has a unique ID. Start its fixed 15-second allowance after rendering
and flushing the prompt. An answer must arrive strictly before the deadline;
expiry wins at equality. Invalid text may be corrected without resetting time.
One numeric answer resolves the question. Resolved or stale events cannot score
again. Timeout advances the game without Enter. Quit and EOF finish the run.

Ranked play is a finite 20-question run with an equal mix of addition, subtraction,
multiplication, and division. Division is generated from divisor and quotient,
so answers are always whole numbers. Baseline correct answers earn 100 points,
plus a linear speed bonus up to 50, plus 10 per previous consecutive correct
answer capped at 50. Wrong answers/timeouts award zero and reset the streak.
Points are rounded down. A fixed run and one numeric attempt prevent unlimited
guessing or point farming; local play cannot prevent external automation.

POSIX input uses cbreak mode (preserving Ctrl-C), readiness polling, and explicit
echo/editing. Windows uses `msvcrt` console input. Both clear pending input at
question boundaries and restore/clean up on exit. Non-TTY or unsupported consoles
use explicitly untimed practice, with no speed bonus and no ranked eligibility.
This is a responsive deadline, not a hard real-time scheduling guarantee.

## Consequences

- Rules can be tested with supplied timestamps, without a terminal or real waits.
- A future UI replaces the terminal adapter while retaining the rules.
- We own cross-platform input behavior. Test actual consoles as well as POSIX
  pseudo-terminals; Windows console behavior cannot be verified on macOS.
- The minimal line editor supports numeric answers, backspace, and quit; rich
  editing, a live countdown, and full-screen rendering are deferred.
- Buffered multiline paste is discarded between questions. Input arriving after
  a new prompt becomes active belongs to that prompt; this is not an anti-bot system.
- Adaptive selection and persistent high scores are future extensions, not part
  of this first implementation. Adaptive practice should remain separate from
  fixed-challenge rankings until difficulty calibration exists. Stored scores
  must identify mode and rules version; local files are not tamper-proof.
- Future question types own parsing/checking; this version accepts integer answers.
- No terminal dependency or async framework is introduced into the engine.

## Gotchas

- **Pipes do not test deadlines.** Both stdin and stdout must be interactive
  terminals for timed play; piping answers or redirecting output selects untimed
  practice. A scripted 16-second answer was accepted. Use a pseudo-terminal to
  exercise expiry without Enter. Practice still awards streak bonuses.
- **One absolute deadline per question.** Garbage input does not restart it.
  A submission at exactly 15 seconds loses; expiry discards partial input,
  counts one miss, and resets the streak. There is no live countdown yet.
- **Quit is a submitted command.** `q`/`quit` requires Enter and must be the
  entire stripped reply; `12q` is invalid. Backspace can remove a partial answer
  before typing `q`; Ctrl-C exits immediately. Quit/EOF does not count the pending
  question as a miss.
- **Input belongs to the active prompt.** Buffered paste is flushed between
  questions, so do not pre-send a whole run to a timed terminal. Keystrokes arriving
  after the next prompt opens can still be interpreted as its answer. The editor
  supports backspace, not full shell-style cursor editing.
- **Fast does not always mean 150 points.** Speed points are rounded down:
  even a tiny positive delay can yield 149 rather than 150 before streak bonuses.
  CLI questions are randomized; deterministic tests inject a seeded generator.
- **Keep terminal cleanup intact.** POSIX cbreak settings must be restored even
  on exceptions, with pending input cleared afterward (including macOS PENDIN).
  Windows uses console key polling, not stdin `select`; its mocked tests still
  need a real Windows console smoke test. Python 3.9 syntax was checked, but the
  available runtime for these play-tests was Python 3.14.
