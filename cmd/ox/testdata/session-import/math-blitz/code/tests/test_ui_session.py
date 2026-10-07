"""The full-screen game loop, driven by a scripted screen and a fake clock."""
import unittest

from math_blitz.engine import Game
from math_blitz.questions import Question
from math_blitz.ui_model import KEY_BACKSPACE
from math_blitz.ui_session import play


class FakeScreen:
    """Scripted keys; the fake clock advances by the wait of each key() call."""

    def __init__(self, clock, script, rows=24, cols=80):
        self.clock, self.script, self.rows, self.cols = clock, list(script), rows, cols
        self.views, self.flushes, self.resizes = [], 0, 0

    def size(self):
        return self.rows, self.cols

    def draw(self, view):
        self.views.append((self.clock.now, view))

    def flush_input(self):
        self.flushes += 1

    def resized(self):
        self.resizes += 1

    def key(self, timeout_ms):
        step = self.script.pop(0) if self.script else None
        if step is None:
            self.clock.now += timeout_ms / 1000
            return -1
        if isinstance(step, tuple):  # (seconds to wait, key)
            self.clock.now += step[0]
            step = step[1]
        if callable(step):
            step(self)
            return -1
        if step == "INT":
            raise KeyboardInterrupt
        return step


class Clock:
    now = 100.0

    def __call__(self):
        return self.now


def keys(text):
    return [ord(c) for c in text]


class PlayTests(unittest.TestCase):
    def game(self, n=2, limit=15.0):
        return Game([Question(i, "{} + 1".format(i), i + 1, "add") for i in range(n)], time_limit=limit)

    def play(self, game, script, **kw):
        clock = Clock()
        screen = FakeScreen(clock, script, **kw)
        play(screen, game, clock)
        return screen

    def test_full_run_scores_through_the_engine(self):
        game = self.game()
        screen = self.play(game, [(0.5, k) for k in keys("1\n")] + [None] * 30
                           + keys("2\n") + [None] * 30 + [32])
        self.assertEqual((game.correct, game.rounds, game.finished), (2, 2, True))
        self.assertGreater(game.score, 0)
        self.assertTrue(screen.views[-1][1].finished)

    def test_flash_follows_each_outcome(self):
        screen = self.play(self.game(1), keys("9\n") + [None] * 40 + [32])
        flashes = {v.flash for _, v in screen.views}
        self.assertIn("wrong", flashes)
        self.assertIn("Not quite. Answer: 1", [v.message for _, v in screen.views])

    def test_timeout_without_enter_and_bar_hits_zero(self):
        game = self.game(1, limit=0.3)
        screen = self.play(game, keys("1") + [None] * 60 + [32])
        self.assertEqual((game.correct, game.rounds, game.streak), (0, 1, 0))
        self.assertIn("timeout", {v.flash for _, v in screen.views})
        timed = [v.fraction for _, v in screen.views if v.flash is None and not v.finished]
        self.assertEqual(timed, sorted(timed, reverse=True))

    def test_wait_never_exceeds_time_remaining(self):
        game = self.game(1, limit=0.02)
        clock = Clock()
        waits = []
        screen = FakeScreen(clock, [None] * 60 + [32])
        real = screen.key
        screen.key = lambda ms: (waits.append(ms), real(ms))[1]
        play(screen, game, clock)
        self.assertLessEqual(waits[0], 20)
        self.assertEqual(game.rounds, 1)

    def test_invalid_text_keeps_the_clock_and_clears_buffer(self):
        game = self.game(1)
        screen = self.play(game, keys("x\n") + keys("1\n") + [None] * 40 + [32])
        self.assertEqual(game.correct, 1)
        self.assertTrue(any("timer keeps running" in v.message for _, v in screen.views))

    def test_backspace_and_printable_filter(self):
        game = self.game(1)
        self.play(game, keys("12") + [127, KEY_BACKSPACE] + [300] + keys("1\n")
                  + [None] * 40 + [32])
        self.assertEqual(game.correct, 1)

    def test_quit_and_eof_stop_without_a_miss(self):
        for script in (keys("q\n"), [4]):
            game = self.game()
            self.play(game, script)
            self.assertTrue(game.stopped)
            self.assertEqual(game.rounds, 0)

    def test_ctrl_c_propagates(self):
        with self.assertRaises(KeyboardInterrupt):
            self.play(self.game(), ["INT"])

    def test_input_is_flushed_at_question_boundaries(self):
        screen = self.play(self.game(2), keys("1\n") + [None] * 30 + keys("2\n")
                           + [None] * 30 + [32])
        self.assertGreaterEqual(screen.flushes, 3)

    def test_keys_during_flash_are_dropped(self):
        game = self.game(2)
        # Digits typed during the first flash must not leak into question 2.
        self.play(game, keys("1\n") + keys("2\n") + [None] * 30 + keys("2\n")
                  + [None] * 30 + [32])
        self.assertEqual(game.correct, 2)

    def test_start_is_held_while_window_is_too_small_and_clock_is_not_running(self):
        game = self.game(1)
        clock = Clock()
        sizes = []

        def grow(screen):
            screen.rows, screen.cols = 24, 80
            sizes.append(clock.now)

        screen = FakeScreen(clock, [None, None, grow] + keys("1\n") + [None] * 30 + [32],
                            rows=5, cols=10)
        play(screen, game, clock)
        self.assertEqual(game.correct, 1)
        self.assertAlmostEqual(game.started_at, sizes[0], places=1)

    def test_shrinking_mid_question_does_not_pause_the_clock(self):
        game = self.game(1, limit=1.0)

        def shrink(screen):
            screen.rows, screen.cols = 3, 10

        # Shrunk window: keystrokes still count, and time keeps passing.
        self.play(game, [shrink] + [None] * 100 + [32])
        self.assertEqual(game.rounds, 1)
        self.assertEqual(game.correct, 0)


if __name__ == "__main__":
    unittest.main()
