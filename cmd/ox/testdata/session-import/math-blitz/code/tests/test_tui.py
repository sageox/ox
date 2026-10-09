import fcntl
import io
import os
import re
import select
import signal
import struct
import subprocess
import sys
import time
import unittest
from pathlib import Path
from unittest.mock import patch

from math_blitz import tui
from math_blitz.engine import Game
from math_blitz.questions import Question

ROOT = Path(__file__).resolve().parent.parent


class AvailabilityTests(unittest.TestCase):
    def tty(self, value):
        stream = io.StringIO()
        stream.isatty = lambda: value
        return stream

    def reason(self, stdin=True, stdout=True, env=None, have_curses=True):
        with patch.object(tui, "curses", object() if have_curses else None):
            return tui.unavailable_reason(self.tty(stdin), self.tty(stdout),
                                          {"TERM": "xterm"} if env is None else env)

    def test_available(self):
        self.assertIsNone(self.reason())

    def test_silent_fallback_when_not_a_terminal(self):
        self.assertEqual(self.reason(stdin=False), "")
        self.assertEqual(self.reason(stdout=False), "")

    def test_fallback_without_curses_says_so(self):
        self.assertIn("curses", self.reason(have_curses=False))

    def test_dumb_or_missing_term_falls_back(self):
        self.assertIn("plain mode", self.reason(env={"TERM": "dumb"}))
        self.assertIn("plain mode", self.reason(env={}))

    def test_run_without_curses_is_unavailable(self):
        with patch.object(tui, "curses", None):
            with self.assertRaises(tui.TuiUnavailable):
                tui.run(self.game_stub())

    @staticmethod
    def game_stub():
        return Game([Question(0, "1 + 1", 2, "add")])


class RunnerSelectionTests(unittest.TestCase):
    def test_plain_flag_skips_the_tui(self):
        from math_blitz import runner
        with patch.object(runner, "play_full_screen") as full, \
                patch.object(runner, "main_plain") as plain:
            runner.main(["--plain"])
        full.assert_not_called()
        plain.assert_called_once()

    def test_unavailable_tui_falls_back_to_plain(self):
        from math_blitz import runner
        with patch.object(runner, "play_full_screen", return_value=None), \
                patch.object(runner, "main_plain") as plain:
            runner.main([])
        plain.assert_called_once()

    def test_piped_run_is_plain_without_flag(self):
        result = subprocess.run([sys.executable, "math_game.py"], input="q\n", text=True,
                                cwd=str(ROOT), stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                env=dict(os.environ, MATH_BLITZ_HOME="/nonexistent/x"),
                                timeout=30)
        self.assertIn("Untimed practice", result.stdout)
        self.assertNotIn("\x1b[", result.stdout)


@unittest.skipUnless(os.name == "posix" and tui.curses is not None, "needs pty and curses")
class PtyBase(unittest.TestCase):
    """Helpers for driving the real curses front end in a pseudo-terminal."""

    def spawn(self, body, rows=24, cols=80):
        import pty
        import termios
        script = ("import sys, termios\n"
                  "saved = termios.tcgetattr(0)\n"
                  "from math_blitz import tui\n"
                  "from math_blitz.engine import Game\n"
                  "from math_blitz.questions import Question\n"
                  "game = Game([Question(i, '%d + 1' % i, i + 1, 'add') for i in range(N)],"
                  " time_limit=LIMIT)\n" + body +
                  "\nassert termios.tcgetattr(0)[3] == saved[3], 'terminal not restored'\n"
                  "print('RESTORED', flush=True)\n")
        self.script = script
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(str(ROOT))
            os.environ["TERM"] = "xterm"
            os.environ["PYTHONPATH"] = str(ROOT)
            os.environ.pop("ESCDELAY", None)
            os.environ.update(self.env)
            os.execv(sys.executable, [sys.executable, "-u", "-c", self.script.replace(
                "LIMIT", self.limit).replace("range(N)", "range(%d)" % self.questions)])
        self.resize(fd, rows, cols)
        return pid, fd

    limit = "5"
    questions = 2
    env = {}

    @staticmethod
    def resize(fd, rows, cols):
        fcntl.ioctl(fd, __import__("termios").TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))

    def read(self, fd, until, timeout=8, send=None):
        out, end, sent = b"", time.monotonic() + timeout, False
        while time.monotonic() < end:
            if select.select([fd], [], [], 0.05)[0]:
                try:
                    data = os.read(fd, 65536)
                except OSError:
                    break
                if not data:
                    break
                out += data
                if send and not sent and send[0] in out:
                    os.write(fd, send[1])
                    sent = True
                if until in out:
                    break
        return out

    def finish(self, pid, fd):
        end, status = time.monotonic() + 5, None
        try:
            while status is None:
                done, code = os.waitpid(pid, os.WNOHANG)
                if done:
                    status = code
                elif time.monotonic() > end:
                    os.kill(pid, signal.SIGKILL)
                    os.waitpid(pid, 0)
                    self.fail("child did not exit")
                else:
                    time.sleep(0.02)
        finally:
            os.close(fd)
        return status



class PtyTests(PtyBase):
    def test_correct_answer_flashes_then_timeout_needs_no_enter(self):
        self.limit = "0.6"
        pid, fd = self.spawn("tui.run(game)\nprint('rounds', game.rounds, game.correct)")
        out = self.read(fd, b"Press any key", send=(b"MATH BLITZ", b"1\r"))
        # question 2 is never answered: it must expire on its own
        os.write(fd, b" ")
        out += self.read(fd, b"RESTORED")
        status = self.finish(pid, fd)
        text = out.decode(errors="replace")
        self.assertIn("Correct!", text)
        self.assertIn("Too slow!", text)
        self.assertIn("\x1b[?1049h", text)   # entered the alternate screen
        self.assertIn("\x1b[?1049l", text)   # and left it
        self.assertIn("rounds 2 1", text)
        self.assertEqual(os.WEXITSTATUS(status), 0, text)

    def test_resize_to_tiny_window_and_back(self):
        self.limit = "5"
        pid, fd = self.spawn("tui.run(game)\nprint('rounds', game.rounds)")
        out = self.read(fd, b"MATH BLITZ")
        self.resize(fd, 6, 20)
        os.kill(pid, signal.SIGWINCH)
        out += self.read(fd, b"too small")
        self.resize(fd, 24, 80)
        os.kill(pid, signal.SIGWINCH)
        out += self.read(fd, b"Score")
        os.write(fd, b"q\r")
        out += self.read(fd, b"RESTORED")
        self.finish(pid, fd)
        text = out.decode(errors="replace")
        self.assertIn("too small", text)
        self.assertIn("RESTORED", text)

    def test_crash_restores_terminal_and_traceback_is_readable(self):
        self.limit = "5"
        body = ("import math_blitz.tui as t\n"
                "orig = t.play\n"
                "def boom(*a, **k):\n"
                "    raise RuntimeError('kaboom')\n"
                "t.play = boom\n"
                "try:\n"
                "    tui.run(game)\n"
                "except RuntimeError as error:\n"
                "    print('CAUGHT', error)\n")
        pid, fd = self.spawn(body)
        out = self.read(fd, b"RESTORED")
        self.finish(pid, fd)
        text = out.decode(errors="replace")
        self.assertIn("CAUGHT kaboom", text)
        self.assertLess(text.index("\x1b[?1049l"), text.index("CAUGHT"))
        self.assertIn("RESTORED", text)

    def test_sigterm_restores_terminal(self):
        self.limit = "5"
        body = ("try:\n"
                "    tui.run(game)\n"
                "except SystemExit as error:\n"
                "    print('EXIT', error.code)\n")
        pid, fd = self.spawn(body)
        out = self.read(fd, b"MATH BLITZ")
        os.kill(pid, signal.SIGTERM)
        out += self.read(fd, b"RESTORED")
        self.finish(pid, fd)
        text = out.decode(errors="replace")
        self.assertIn("EXIT 143", text)
        self.assertIn("RESTORED", text)
        self.assertLess(text.index("\x1b[?1049l"), text.index("EXIT"))


@unittest.skipUnless(os.name == "posix" and tui.curses is not None, "needs pty and curses")
class AwkwardSituationTests(PtyBase):
    """Regression tests for things real terminals do: tiny windows, resizes,
    pastes, and Esc. Each reports `RESULT <rounds> <correct>` when the run ends."""

    def start(self, rows=24, cols=80, limit="5", questions=1, env=None):
        self.limit, self.questions, self.env = limit, questions, env or {}
        self.out = b""
        self.pid, self.fd = self.spawn(
            "tui.run(game)\nprint('RESULT', game.rounds, game.correct, flush=True)",
            rows=rows, cols=cols)

    def pump(self, seconds):
        end = time.monotonic() + seconds
        while time.monotonic() < end:
            if select.select([self.fd], [], [], 0.02)[0]:
                try:
                    data = os.read(self.fd, 65536)
                except OSError:
                    return
                self.out += data

    def window(self, rows, cols):
        self.resize(self.fd, rows, cols)
        os.kill(self.pid, signal.SIGWINCH)

    def result(self):
        """Dismiss the game-over screen and return (rounds, correct)."""
        end = time.monotonic() + 8
        while b"RESULT" not in self.out and time.monotonic() < end:
            try:
                os.write(self.fd, b" ")
            except OSError:
                pass
            self.pump(0.2)
        self.pump(0.2)
        self.finish(self.pid, self.fd)
        match = re.search(rb"RESULT (\d+) (\d+)", self.out)
        self.assertIsNotNone(match, self.out[-300:])
        self.assertNotIn(b"Traceback", self.out)
        return int(match.group(1)), int(match.group(2))

    def test_tiny_window_does_not_start_the_clock(self):
        self.start(rows=3, cols=10, limit="1.0")
        self.pump(1.6)  # longer than the whole allowance
        self.assertNotIn(b"RESULT", self.out)
        self.assertNotIn(b"Too slow", self.out)
        self.window(24, 80)
        self.pump(0.3)
        os.write(self.fd, b"1\r")
        self.assertEqual(self.result(), (1, 1))

    def test_one_by_one_window_is_survivable(self):
        self.start(rows=1, cols=1, limit="1.0")
        self.pump(0.5)
        self.assertNotIn(b"Traceback", self.out)
        self.window(24, 80)
        self.pump(0.3)
        os.write(self.fd, b"1\r")
        self.assertEqual(self.result(), (1, 1))

    def test_resize_mid_question_keeps_typed_text_and_clock(self):
        self.start()
        self.assertIn(b"MATH BLITZ", self.wait_for(b"MATH BLITZ"))
        os.write(self.fd, b"1")
        self.pump(0.2)
        self.window(6, 20)
        self.pump(0.4)
        self.window(24, 80)
        self.pump(0.3)
        os.write(self.fd, b"\r")  # the "1" typed before the resize is still there
        self.assertEqual(self.result(), (1, 1))

    def test_shrinking_past_the_deadline_still_expires(self):
        self.start(limit="1.0")
        self.wait_for(b"MATH BLITZ")
        self.window(4, 12)
        self.pump(1.6)
        self.assertEqual(self.result(), (1, 0))

    def test_resize_storm(self):
        import random
        rng = random.Random(7)
        self.start(limit="4")
        self.wait_for(b"MATH BLITZ")
        for _ in range(50):
            self.window(rng.randint(1, 40), rng.randint(1, 120))
            self.pump(0.01)
        self.window(24, 80)
        self.pump(0.3)
        os.write(self.fd, b"1\r")
        self.assertEqual(self.result(), (1, 1))

    def test_several_digits_pasted_at_once_are_one_answer(self):
        self.start()
        self.wait_for(b"MATH BLITZ")
        os.write(self.fd, b"12\r")  # one wrong answer, not "1" then "2"
        self.assertEqual(self.result(), (1, 0))

    def test_huge_paste_is_capped_and_does_not_hang(self):
        self.start(limit="3")
        self.wait_for(b"MATH BLITZ")
        data, sent = b"7" * 20000 + b"\r", 0
        flags = fcntl.fcntl(self.fd, fcntl.F_GETFL)
        fcntl.fcntl(self.fd, fcntl.F_SETFL, flags | os.O_NONBLOCK)
        end = time.monotonic() + 10
        while sent < len(data) and time.monotonic() < end:
            try:
                sent += os.write(self.fd, data[sent:sent + 1024])
            except OSError:
                pass
            self.pump(0.002)
        fcntl.fcntl(self.fd, fcntl.F_SETFL, flags)
        self.assertEqual(sent, len(data))
        self.assertEqual(self.result(), (1, 0))

    def test_multi_line_paste_does_not_answer_later_questions(self):
        self.start(limit="1.5", questions=2)
        self.wait_for(b"MATH BLITZ")
        os.write(self.fd, b"1\r2\r")  # the 2 is for question two, which must not take it
        self.assertEqual(self.result(), (2, 1))

    def test_non_ascii_paste_is_ignored(self):
        self.start()
        self.wait_for(b"MATH BLITZ")
        os.write(self.fd, "\uff11\u00e9\U0001f600".encode() + b"1\r")
        self.assertEqual(self.result(), (1, 1))

    def test_escape_sequences_never_reach_the_answer(self):
        # ESCDELAY=1000 in the environment must not matter either.
        for junk in (b"\x1b[A", b"\x1b[1;5C", b"\x1b1", b"\x1b[200~", b"\x1b"):
            with self.subTest(junk=junk):
                self.start(env={"ESCDELAY": "1000"})
                self.wait_for(b"MATH BLITZ")
                os.write(self.fd, junk)
                self.pump(0.3)
                os.write(self.fd, b"1\r")
                self.assertEqual(self.result(), (1, 1))

    def test_escape_does_not_delay_expiry(self):
        # One Esc just before the deadline: curses would wait ESCDELAY for the
        # rest of a sequence, and a user's ESCDELAY=1000 must not be honored.
        self.start(limit="1.0", env={"ESCDELAY": "1000"})
        self.wait_for(b"MATH BLITZ")
        began = time.monotonic()
        self.pump(0.9)
        os.write(self.fd, b"\x1b")
        while b"Too slow" not in self.out and time.monotonic() - began < 4:
            self.pump(0.02)
        self.assertIn(b"Too slow", self.out)
        self.assertLess(time.monotonic() - began, 1.5)
        self.assertEqual(self.result(), (1, 0))

    def test_resize_during_flash_and_game_over(self):
        self.start()
        self.wait_for(b"MATH BLITZ")
        os.write(self.fd, b"1\r")
        self.pump(0.1)
        self.window(8, 25)
        self.pump(0.2)
        self.window(24, 80)
        self.wait_for(b"GAME OVER")
        self.window(5, 12)
        self.pump(0.2)
        self.window(30, 100)
        self.assertEqual(self.result(), (1, 1))

    def wait_for(self, needle, timeout=5):
        end = time.monotonic() + timeout
        while needle not in self.out and time.monotonic() < end:
            self.pump(0.05)
        return self.out

    def test_ctrl_c_ctrl_d_and_hangup_leave_the_alternate_screen(self):
        for name, act in (("ctrl-c", lambda: os.write(self.fd, b"\x03")),
                          ("ctrl-d", lambda: os.write(self.fd, b"\x04")),
                          ("hangup", lambda: os.kill(self.pid, signal.SIGHUP))):
            with self.subTest(name):
                self.start(questions=2)
                self.wait_for(b"MATH BLITZ")
                os.write(self.fd, b"1")
                self.pump(0.2)
                act()
                self.pump(0.8)
                self.assertIn(b"\x1b[?1049l", self.out)
                try:
                    self.finish(self.pid, self.fd)
                except AssertionError:
                    self.fail(name + ": child did not exit")


if __name__ == "__main__":
    unittest.main()
