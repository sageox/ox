# 0004: Full-screen terminal UI

Status: Accepted

Date: 2026-10-01

## Context

[ADR-0001](0001-game-architecture.md) deferred a live countdown and full-screen
rendering, and said a future UI would replace the terminal adapter while keeping
the rules. This is that UI. It amends ADR-0001's "live countdown and
full-screen rendering are deferred" and nothing else in 0001, 0002 or 0003. The
engine, scoring and `scores.py` are untouched. Python 3.9 and the standard
library remain the only requirements, which rules out `windows-curses`.

The questions it had to answer: how to run a live countdown while reading keys
without threads; how to leave the player's terminal intact after a crash; what
a resize or a too-small window does; and what to do where the standard library
has no curses (Windows).

## Decision

Add a curses front end that only drives `Game`, in three layers, and keep the
existing line-based `Terminal` as `--plain` mode.

| Module | Role | Imports curses? |
|---|---|---|
| `math_blitz/ui_model.py` | Pure state: the immutable `View` and the functions that produce the next one, `layout()`, and `AnswerEditor` (the line-editing state machine) | No |
| `math_blitz/ui_session.py` | The game loop, written against a five-method screen protocol (`size`, `draw`, `key`, `flush_input`, `resized`) with the clock passed in | No |
| `math_blitz/tui.py` | Availability checks, the curses adapter, signal handling and cleanup | Yes |

### Countdown without threads

One loop on the main thread. `getch` runs with a timeout of
`min(50 ms, time left)`, so it is both the keyboard read and the frame tick. The
bar is a view of `game.deadline`: frame rate changes how smooth it looks, never
who wins. Each keystroke is stamped right after `getch` returns and passed to
`game.submit`; expiry calls `game.expire`. The engine's strict rule (expiry wins
at equality) still decides every result.

### Question boundaries (from ADR-0001)

Input is flushed before each prompt, the frame is drawn before `start_question`,
and keys typed during the right/wrong flash (0.6 s correct, 1.2 s otherwise) are
discarded. A pasted run of answers therefore cannot answer later questions.

### Resize and small windows

`KEY_RESIZE` arrives through `getch`; the loop redraws and layout is recomputed
from the window size every frame. The minimum is 40x12; below it only a resize
message is shown. A question does not start until the window is big enough
(keys typed meanwhile are discarded). If the window shrinks mid-question the
clock keeps running and keys still count, so shrinking is not a pause button.
The deadline is absolute, so a resize neither helps nor hurts the player.

### Line editing

`AnswerEditor` accepts printable ASCII up to 128 characters; Backspace edits;
Enter submits and consumes the line (as plain mode does), and the engine decides
whether it is valid. Ctrl-D ends the run. After Esc it swallows an unrecognized
escape sequence (see Gotchas).

### Leaving the terminal intact

`curses.wrapper` ends curses in a `finally`. Failures are caught inside the
wrapper and re-raised after it, so a traceback prints on the normal screen
instead of vanishing with the alternate one. SIGTERM and SIGHUP become
`SystemExit` so cleanup runs. Ctrl-C works because cbreak keeps ISIG. Pending
input is flushed on exit (the macOS PENDIN gotcha from ADR-0001).

### High scores

The game-over screen waits for a key, then curses ends and the existing
`offer_high_score` runs on the normal terminal, unchanged.

### Selection and fallback

`--plain` forces line mode. Plain mode is also used automatically when stdin or
stdout is not a terminal (silently, as before), when `curses` cannot be imported
(Windows; one-line note), when `TERM` is unset or `dumb`, or when curses fails to
start. In every fallback nothing has been drawn and the game has not begun.

## Consequences

- Windows runs plain mode. A VT-sequence backend over `msvcrt` could sit behind
  the same `layout()` output; it needs a real Windows console to verify, as
  ADR-0001 already says of the existing adapter.
- Colors use the terminal's default background and fall back to bold/reverse
  when there is no color or `NO_COLOR` is set. The bar uses `#`/`-` when the
  locale is not UTF-8.
- Name entry happens outside the full-screen UI. Moving it in later does not
  touch the engine or store.
- Everything that decides how the game looks or edits input is unit-tested with
  no terminal; the curses layer is exercised in a pseudo-terminal.

## Gotchas

### `getch` and timeouts

- **`timeout(ms)` is a window setting, not a per-call argument.** It sticks
  until changed. `-1` blocks forever, `0` never waits (a busy loop), anything
  else returns `-1` when it expires. `CursesScreen.key` sets it on every call.
- **Never wait past the deadline.** Cap the timeout by the time left and round
  *up* (`ceil`, minimum 1 ms): rounding down wakes early and spins, and not
  capping lets an idle player outlive the clock. `key_wait_ms` owns this rule.
- **`-1` means "re-check the clock", not "nothing happened".** A signal can
  also cut a wait short. Every loop must recompute `remaining` after each return.
- **Timestamp the key immediately.** The stamp is when the key was *processed*,
  not when it was typed. A backlog (a big paste) can make an Enter look late,
  which is why input is flushed at question boundaries.
- **Every loop must call `getch`.** A loop that sleeps instead (the flash, the
  too-small hold, the game-over wait) would never see `KEY_RESIZE` or Ctrl-C.
  Use short `getch` timeouts, never `sleep`.

### `KEY_RESIZE`

- **It is delivered through `getch`.** Redraw from `getmaxyx()` on every frame
  and never cache the size. Resizes also arrive during the flash, the too-small
  hold and the game-over screen; each of those loops handles them.
- **Resizing does not pause the clock** and never discards the typed text. Do
  not "fix" the too-small screen by pausing: shrinking the window would become
  a free pause button.
- **Writing the bottom-right cell raises `curses.error`.** All drawing goes
  through one guarded loop in `CursesScreen.draw`; do not call `addstr` elsewhere.
- **A terminal that reports 0x0 is treated as 24x80.** ncurses falls back to
  the terminfo size, so the too-small screen cannot trigger there (seen in a
  pty with no size set). A 1x1 window draws nothing visible; that is expected.
- **Expect more than one `KEY_RESIZE` per resize.** In a pty the size change
  raises SIGWINCH by itself, and the tests send one too, which produced two
  keys for one resize. Handle it idempotently: redraw, nothing else.

### The Escape-key delay

- **ncurses waits `ESCDELAY` after an Esc byte** to see whether it starts a
  sequence, and its documented default is 1000 ms. During that wait the loop is blocked, so
  one Esc near the deadline would delay expiry by up to a second. We use 25 ms.
- **Override, do not default.** The first version used `os.environ.setdefault`,
  so a user who exported `ESCDELAY=1000` got the 1-second stall back (measured:
  1003-1027 ms to deliver a lone Esc). We now assign the environment variable
  before `initscr` and also call `curses.set_escdelay` (3.9+; the variable
  covers older builds). A pty test sets `ESCDELAY=1000` and checks expiry.
- **Auto-repeating Esc does not exercise the delay**, because the next byte
  arrives within the wait. Test with one Esc just before the deadline.
- **Curses only decodes the sequences terminfo lists.** `ESC O A` (Up) becomes
  `KEY_UP` and is ignored, but a terminal in the other cursor mode, tmux, ssh
  with a mismatched `TERM`, or a bracketed-paste marker can send `ESC [ A`, and
  its tail arrived as typed characters (`[A` in the answer). `AnswerEditor` now
  swallows CSI (`ESC [ ... final`), SS3 (`ESC O x`) and Alt+key after an Esc.
- **Limits of that.** A lone Esc is forgotten at the next idle tick. A sequence
  split by a gap longer than one tick (a very slow link) leaks its tail, and a
  character typed within one tick of an Esc is treated as Alt+key and dropped.
  Both are far shorter than a human can type.
- **Ctrl-Z is not delivered as a key.** Cbreak keeps ISIG, so it raises SIGTSTP,
  which ncurses handles (leave, stop, re-enter). In a pty the process kept its
  typed text and screen, but a real suspend and `fg` was not testable there. The
  clock keeps running while suspended. The `26` in `EOF_KEYS` matters only on a
  platform that delivers it.

### Restoring the terminal after an error

- **Catch inside the wrapper, re-raise after it.** An exception that escapes
  `curses.wrapper` is printed by Python only after `endwin`, which is fine, but
  catching it inside makes the order explicit and lets `run` distinguish "curses
  never started" (`TuiUnavailable`, fall back to plain) from a game failure.
- **Fatal signals need handlers.** Python's default SIGTERM and SIGHUP skip
  `finally`; `run` converts them to `SystemExit` and restores the old handlers.
  SIGKILL cannot be handled; `reset` fixes that terminal.
- **Call `curs_set(1)` before the wrapper ends.** After `endwin` it fails, and a
  hidden cursor survives some failures.
- **Flush pending input on exit**, even on error, or typed digits reach the
  shell prompt.
- **Test it with a real pty.** Compare `termios.tcgetattr` before and after,
  and look for the alternate-screen exit sequence (`ESC [ ? 1049 l`) before the
  traceback. Fake screens cannot show this.

### Input and testing

- **Paste is just fast typing.** Several digits at once are one answer; a
  multi-line paste answers only the question that is open, and the rest is
  flushed. The cap is 128 characters; 20,000 pasted characters were consumed in
  about half a second. Bytes above ASCII (full-width digits, emoji) are ignored.
- **Tests that run the real program in a pty must pass `--plain`**, or they
  start the full-screen UI.
- **A pty test must not block writing a large paste** while nothing reads the
  child's output; send non-blocking or pump output between writes.
- **Fake-screen tests loop forever if the scripted keys run out** before the
  game-over screen. Give them enough idle ticks.
- **The child's `time.monotonic()` is not comparable with the test's.** Measure
  latency from the parent side.
- **Set `locale.setlocale(LC_ALL, "")` before `initscr`** or wide characters
  (the bar) garble; `run` picks ASCII when the encoding is not UTF-8.
- **Windows is untested.** It runs plain mode by import failure, which is
  covered by a mock only.
