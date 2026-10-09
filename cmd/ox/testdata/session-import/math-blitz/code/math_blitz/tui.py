"""Thin curses layer: availability checks, the screen adapter, and cleanup.

Nothing here decides what the game looks like or how it plays; see `ui_model`
(state, layout, line editing) and `ui_session` (the game loop).
"""
import locale
import os
import signal
import sys
from typing import Optional, Tuple

from .ui_model import ESC_DELAY_MS, View, layout
from .ui_session import play

try:
    import curses
except ImportError:  # Windows, or a Python built without _curses
    curses = None


class TuiUnavailable(Exception):
    """Curses could not start; nothing has been shown or played."""


def unavailable_reason(stdin=None, stdout=None, environ=None) -> Optional[str]:
    """None when the full-screen UI can run; "" for a silent fallback."""
    stdin, stdout = stdin or sys.stdin, stdout or sys.stdout
    environ = os.environ if environ is None else environ
    if not (stdin.isatty() and stdout.isatty()):
        return ""
    if curses is None:
        return "Full-screen mode needs curses, which is not available here; using plain mode."
    if environ.get("TERM", "dumb") == "dumb":
        return "This terminal cannot do full-screen mode; using plain mode."
    return None


class CursesScreen:
    def __init__(self, stdscr, unicode_ok: bool):
        self.stdscr, self.unicode_ok = stdscr, unicode_ok
        try:
            curses.curs_set(0)
        except curses.error:
            pass
        stdscr.keypad(True)
        if hasattr(curses, "set_escdelay"):  # 3.9+; the env var covers older builds
            curses.set_escdelay(ESC_DELAY_MS)
        reverse = curses.A_REVERSE | curses.A_BOLD
        self.styles = {"normal": 0, "bold": curses.A_BOLD, "dim": curses.A_DIM,
                       "bar_ok": 0, "bar_warn": curses.A_BOLD, "bar_low": curses.A_BOLD,
                       "flash_good": reverse, "flash_bad": reverse}
        if curses.has_colors() and "NO_COLOR" not in os.environ:
            try:
                curses.start_color()
                curses.use_default_colors()
                for pair, color in enumerate(
                        (curses.COLOR_GREEN, curses.COLOR_YELLOW, curses.COLOR_RED), 1):
                    curses.init_pair(pair, color, -1)
                green, yellow, red = (curses.color_pair(i) for i in (1, 2, 3))
                self.styles.update(bar_ok=green, bar_warn=yellow, bar_low=red | curses.A_BOLD,
                                   flash_good=green | reverse, flash_bad=red | reverse)
            except curses.error:
                pass

    def size(self) -> Tuple[int, int]:
        return self.stdscr.getmaxyx()

    def key(self, timeout_ms: int) -> int:
        self.stdscr.timeout(timeout_ms)
        return self.stdscr.getch()

    def flush_input(self) -> None:
        curses.flushinp()

    def resized(self) -> None:
        if hasattr(curses, "update_lines_cols"):
            curses.update_lines_cols()

    def draw(self, view: View) -> None:
        rows, cols = self.size()
        self.stdscr.erase()
        for row, col, text, style in layout(view, rows, cols, self.unicode_ok):
            try:
                self.stdscr.addstr(row, col, text, self.styles[style])
            except curses.error:  # writing the bottom-right cell raises
                pass
        self.stdscr.refresh()


def _terminate(signum, frame):
    raise SystemExit(128 + signum)


def run(game) -> None:
    """Play `game` full-screen. Always leaves the terminal as it found it.

    Raises TuiUnavailable if curses cannot start (nothing was shown), and
    re-raises any failure from inside the game only after leaving curses, so
    a traceback prints on the normal screen instead of vanishing with it.
    """
    if curses is None:
        raise TuiUnavailable("curses is not available")
    try:
        locale.setlocale(locale.LC_ALL, "")
    except locale.Error:
        pass
    unicode_ok = "utf" in (locale.getpreferredencoding(False) or "").lower()
    # Override, not setdefault: a user's ESCDELAY=1000 would freeze input (and
    # delay expiry) for a second after every Esc. Read by initscr.
    os.environ["ESCDELAY"] = str(ESC_DELAY_MS)
    started, failure = [], []

    def main(stdscr):
        started.append(True)
        try:
            play(CursesScreen(stdscr, unicode_ok), game)
        except BaseException as error:
            failure.append(error)
        finally:
            try:
                curses.curs_set(1)
            except curses.error:
                pass

    previous = {}
    for name in ("SIGTERM", "SIGHUP"):
        number = getattr(signal, name, None)
        if number is not None:
            try:
                previous[number] = signal.signal(number, _terminate)
            except ValueError:  # not the main thread
                pass
    try:
        curses.wrapper(main)
    except curses.error as error:
        if not started:
            raise TuiUnavailable(str(error))
        raise
    finally:
        for number, handler in previous.items():
            signal.signal(number, handler)
        _discard_pending_input()
    if failure:
        raise failure[0]


def _discard_pending_input() -> None:
    try:
        import termios
        termios.tcflush(sys.stdin.fileno(), termios.TCIFLUSH)
    except Exception:  # not a POSIX terminal; nothing to flush
        pass
