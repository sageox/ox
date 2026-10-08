"""Pure UI state: no curses, no I/O, no clock reads.

The view model (`View` plus the functions that produce new Views), the layout,
and the line editor for typed answers all live here, so every rule of the
full-screen UI is testable without a terminal.
"""
import math
from dataclasses import dataclass, replace
from typing import List, Optional, Tuple

MIN_COLS, MIN_ROWS = 40, 12
TICK_MS = 50
FLASH_SECONDS = {"correct": 0.6, "wrong": 1.2, "timeout": 1.2}
MAX_ANSWER = 128
INVALID_MESSAGE = "Enter a whole number (or q); the timer keeps running"

# ncurses key codes, spelled out so this module never imports curses. A test
# checks them against the real curses constants.
KEY_IDLE = -1       # getch timed out with no key
KEY_RESIZE = 410
KEY_BACKSPACE = 263
KEY_ENTER = 343
ENTER_KEYS = (10, 13, KEY_ENTER)
BACKSPACE_KEYS = (8, 127, KEY_BACKSPACE)
EOF_KEYS = (4, 26)  # Ctrl-D / Ctrl-Z, the end-of-input keys plain mode honors
ESC = 27
ESC_DELAY_MS = 25   # how long curses waits after Esc for a sequence to continue

Draw = Tuple[int, int, str, str]  # row, col, text, style name


# --- line editor -----------------------------------------------------------

NONE, EDITED, SUBMIT, EOF, RESIZE = "none", "edited", "submit", "eof", "resize"


@dataclass(frozen=True)
class Edit:
    """What one key did. `text` is the submitted line when action is SUBMIT."""
    action: str
    text: str = ""


class AnswerEditor:
    """Edits one line of typed input. Only printable ASCII is accepted.

    Enter consumes the line (it is returned and the buffer empties), exactly
    like plain mode, so the caller decides what an invalid line means.

    Curses turns the escape sequences it knows (arrows, F-keys, Delete) into
    single key codes. A terminal or multiplexer may send others, such as ESC [ A
    for Up in the wrong cursor mode or a bracketed-paste marker; their tails
    would otherwise arrive as typed characters. So after Esc the editor swallows
    a CSI sequence (ESC [ params final), an SS3 sequence (ESC O x), or one
    Alt-modified key. A lone Esc ends at the next idle tick.
    """

    def __init__(self, limit: int = MAX_ANSWER):
        self.limit = limit
        self._chars: List[str] = []
        self._escape: Optional[str] = None  # None, "esc", "csi" or "ss3"

    @property
    def text(self) -> str:
        return "".join(self._chars)

    def _swallow(self, key: int) -> bool:
        """True if `key` belongs to an escape sequence; otherwise leaves the
        sequence and lets the key be handled normally."""
        state, self._escape = self._escape, None
        if state == "esc":
            if key == ord("["):
                self._escape = "csi"
                return True
            if key == ord("O"):
                self._escape = "ss3"
                return True
            return 32 <= key <= 126  # Alt+key: drop the key
        if state == "csi":
            if 0x20 <= key <= 0x3F:  # parameter and intermediate bytes
                self._escape = "csi"
                return True
            return 0x40 <= key <= 0x7E  # the final byte ends the sequence
        return 32 <= key <= 126  # ss3: exactly one more byte

    def feed(self, key: int) -> Edit:
        if self._escape is not None and self._swallow(key):
            return Edit(NONE)
        if key == ESC:
            self._escape = "esc"
            return Edit(NONE)
        if key == KEY_IDLE:
            return Edit(NONE)
        if key == KEY_RESIZE:
            return Edit(RESIZE)
        if key in EOF_KEYS:
            return Edit(EOF)
        if key in ENTER_KEYS:
            line, self._chars = self.text, []
            return Edit(SUBMIT, line)
        if key in BACKSPACE_KEYS:
            if not self._chars:
                return Edit(NONE)
            self._chars.pop()
            return Edit(EDITED)
        if 32 <= key <= 126 and len(self._chars) < self.limit:
            self._chars.append(chr(key))
            return Edit(EDITED)
        return Edit(NONE)  # arrows, other controls, bytes above ASCII, a full line


# --- view model ------------------------------------------------------------

@dataclass(frozen=True)
class View:
    number: int = 0
    total: int = 0
    text: str = ""
    buffer: str = ""
    score: int = 0
    streak: int = 0
    best_streak: int = 0
    fraction: Optional[float] = None  # time left, 0..1; None when untimed
    seconds: float = 0.0
    flash: Optional[str] = None       # correct | wrong | timeout
    message: str = ""
    finished: bool = False
    correct: int = 0
    rounds: int = 0


def question_view(game, question) -> View:
    """The screen for a question that is about to start."""
    return View(number=game.rounds + 1, total=game.question_count, text=question.text,
                score=game.score, streak=game.streak, best_streak=game.best_streak,
                fraction=1.0 if game.timed else None,
                seconds=game.time_limit if game.timed else 0.0)


def with_clock(view: View, remaining: float, time_limit: float) -> View:
    """The same screen with the bar and seconds for `remaining` time left."""
    remaining = max(0.0, remaining)
    return replace(view, fraction=min(1.0, remaining / time_limit), seconds=remaining)


def with_buffer(view: View, text: str) -> View:
    return replace(view, buffer=text)


def with_invalid(view: View) -> View:
    return replace(view, message=INVALID_MESSAGE)


def outcome_view(view: View, game, question, outcome) -> View:
    """The flash after a resolved question; `game` has already scored it."""
    message = {
        "correct": "Correct! +{} points".format(outcome.points),
        "wrong": "Not quite. Answer: {}".format(question.answer),
        "timeout": "Too slow! Answer: {}".format(question.answer),
    }[outcome.kind]
    flashed = replace(view, flash=outcome.kind, message=message, score=game.score,
                      streak=game.streak, best_streak=game.best_streak)
    if outcome.kind == "timeout":
        flashed = replace(flashed, fraction=0.0, seconds=0.0)
    return flashed


def final_view(game) -> View:
    return View(total=game.question_count, score=game.score, best_streak=game.best_streak,
                correct=game.correct, rounds=game.rounds, finished=True)


def key_wait_ms(remaining: float, timed: bool) -> int:
    """How long to wait for a key: a frame tick, never past the deadline."""
    if not timed:
        return TICK_MS
    return max(1, min(TICK_MS, math.ceil(remaining * 1000)))


# --- layout ----------------------------------------------------------------

def too_small(rows: int, cols: int) -> bool:
    return rows < MIN_ROWS or cols < MIN_COLS


def bar_cells(fraction: float, width: int) -> int:
    """Filled cells; rounds up so the bar is never empty while time remains."""
    if width <= 0 or not fraction > 0:
        return 0
    return min(width, math.ceil(min(fraction, 1.0) * width))


def _clip(row, col, text, rows, cols, style="normal") -> List[Draw]:
    if not (0 <= row < rows) or col >= cols:
        return []
    if col < 0:
        text, col = text[-col:], 0
    text = text[:cols - col]
    return [(row, col, text, style)] if text else []


def _center(row, text, rows, cols, style="normal") -> List[Draw]:
    return _clip(row, max(0, (cols - len(text)) // 2), text, rows, cols, style)


def layout(view: View, rows: int, cols: int, unicode_ok: bool = True) -> List[Draw]:
    """Everything to paint, clipped to the window."""
    if too_small(rows, cols):
        lines = ["Terminal too small", "need {}x{}, have {}x{}".format(
            MIN_COLS, MIN_ROWS, cols, rows), "resize to continue"]
        draws: List[Draw] = []
        for i, line in enumerate(lines):
            draws += _center(max(0, rows // 2 - 1) + i, line, rows, cols, "bold")
        return draws
    if view.finished:
        return _game_over(view, rows, cols)

    draws = _clip(0, 1, "MATH BLITZ", rows, cols, "bold")
    counter = "Question {}/{}".format(view.number, view.total)
    draws += _clip(0, cols - len(counter) - 1, counter, rows, cols)
    stats = "Score {}   Streak {}   Best {}".format(view.score, view.streak, view.best_streak)
    draws += _clip(1, 1, stats, rows, cols, "bold")

    if view.fraction is not None:
        label, tail = "Time ", " {:4.1f}s".format(max(0.0, view.seconds))
        width = cols - 2 - len(label) - len(tail)
        filled = bar_cells(view.fraction, width)
        style = ("flash_good" if view.flash == "correct" else
                 "flash_bad" if view.flash in ("wrong", "timeout") else
                 "bar_ok" if view.fraction > 0.5 else
                 "bar_warn" if view.fraction > 0.2 else "bar_low")
        full, empty = ("█", "░") if unicode_ok else ("#", "-")
        draws += _clip(3, 1, label, rows, cols)
        draws += _clip(3, 1 + len(label), full * filled, rows, cols, style)
        draws += _clip(3, 1 + len(label) + filled, empty * (width - filled), rows, cols, "dim")
        draws += _clip(3, 1 + len(label) + width, tail, rows, cols)

    q_row = max(5, rows // 2 - 1)
    draws += _center(q_row, "What is {}?".format(view.text), rows, cols, "bold")
    answer = "> " + view.buffer[-(cols - 6):] + ("" if view.flash else "_")
    draws += _center(q_row + 2, answer, rows, cols)
    if view.message:
        style = {"correct": "flash_good", "wrong": "flash_bad",
                 "timeout": "flash_bad"}.get(view.flash or "", "normal")
        draws += _center(q_row + 4, " {} ".format(view.message), rows, cols, style)
    draws += _clip(rows - 1, 1, "Enter: answer   q + Enter: quit", rows, cols, "dim")
    return draws


def _game_over(view: View, rows: int, cols: int) -> List[Draw]:
    top = max(1, rows // 2 - 3)
    draws = _center(top, "GAME OVER", rows, cols, "bold")
    draws += _center(top + 2, "Final score: {}".format(view.score), rows, cols, "bold")
    draws += _center(top + 3, "Correct: {}/{}   Best streak: {}".format(
        view.correct, view.rounds, view.best_streak), rows, cols)
    draws += _center(top + 5, "Press any key", rows, cols, "dim")
    return draws
