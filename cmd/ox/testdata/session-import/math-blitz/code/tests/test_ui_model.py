"""Unit tests for the pure UI part: line editor, view model, layout. No terminal."""
import random
import unittest
from dataclasses import FrozenInstanceError

from math_blitz import ui_model as m
from math_blitz.engine import Game, Outcome
from math_blitz.questions import Question
from math_blitz.ui_model import (EDITED, EOF, NONE, RESIZE, SUBMIT, AnswerEditor, Edit, View)


def keys(text):
    return [ord(c) for c in text]


def feed_all(editor, sequence):
    return [editor.feed(k) for k in sequence]


class AnswerEditorTests(unittest.TestCase):
    def test_digits_accumulate(self):
        editor = AnswerEditor()
        results = feed_all(editor, keys("42"))
        self.assertEqual([r.action for r in results], [EDITED, EDITED])
        self.assertEqual(editor.text, "42")

    def test_enter_submits_the_line_and_consumes_it(self):
        editor = AnswerEditor()
        feed_all(editor, keys("12"))
        self.assertEqual(editor.feed(10), Edit(SUBMIT, "12"))
        self.assertEqual(editor.text, "")
        self.assertEqual(editor.feed(13), Edit(SUBMIT, ""))  # carriage return too

    def test_every_enter_key_submits(self):
        for key in m.ENTER_KEYS:
            editor = AnswerEditor()
            feed_all(editor, keys("7"))
            self.assertEqual(editor.feed(key), Edit(SUBMIT, "7"))

    def test_empty_enter_submits_an_empty_line(self):
        self.assertEqual(AnswerEditor().feed(10), Edit(SUBMIT, ""))

    def test_backspace_removes_last_character(self):
        editor = AnswerEditor()
        feed_all(editor, keys("123"))
        self.assertEqual(editor.feed(127), Edit(EDITED))
        self.assertEqual(editor.text, "12")

    def test_every_backspace_key_edits(self):
        for key in m.BACKSPACE_KEYS:
            editor = AnswerEditor()
            feed_all(editor, keys("9"))
            self.assertEqual(editor.feed(key).action, EDITED)
            self.assertEqual(editor.text, "")

    def test_backspace_on_empty_line_does_nothing(self):
        editor = AnswerEditor()
        self.assertEqual(editor.feed(127), Edit(NONE))
        self.assertEqual(editor.text, "")

    def test_backspace_then_retype(self):
        editor = AnswerEditor()
        feed_all(editor, keys("12") + [127] + keys("3"))
        self.assertEqual(editor.text, "13")

    def test_minus_sign_and_letters_are_accepted_for_the_engine_to_judge(self):
        editor = AnswerEditor()
        feed_all(editor, keys("-5"))
        self.assertEqual(editor.text, "-5")
        editor = AnswerEditor()
        feed_all(editor, keys("quit"))
        self.assertEqual(editor.feed(10), Edit(SUBMIT, "quit"))

    def test_idle_tick_changes_nothing(self):
        editor = AnswerEditor()
        feed_all(editor, keys("5"))
        self.assertEqual(editor.feed(m.KEY_IDLE), Edit(NONE))
        self.assertEqual(editor.text, "5")

    def test_resize_and_eof_are_reported_without_touching_the_line(self):
        editor = AnswerEditor()
        feed_all(editor, keys("5"))
        self.assertEqual(editor.feed(m.KEY_RESIZE), Edit(RESIZE))
        self.assertEqual(editor.text, "5")
        for key in m.EOF_KEYS:
            self.assertEqual(editor.feed(key), Edit(EOF))
        self.assertEqual(editor.text, "5")

    def test_unprintable_and_special_keys_are_ignored(self):
        editor = AnswerEditor()
        feed_all(editor, keys("1"))
        ignored = [0, 1, 9, 27, 31, 127 + 1, 200, 255, 258, 259, 260, 261, 330, 999]
        for key in ignored:
            self.assertEqual(editor.feed(key), Edit(NONE), key)
        self.assertEqual(editor.text, "1")

    def test_printable_ascii_boundaries(self):
        editor = AnswerEditor()
        self.assertEqual(editor.feed(31), Edit(NONE))
        self.assertEqual(editor.feed(32), Edit(EDITED))   # space
        self.assertEqual(editor.feed(126), Edit(EDITED))  # ~
        self.assertEqual(editor.feed(127), Edit(EDITED))  # DEL is a backspace here
        self.assertEqual(editor.text, " ")

    def test_line_is_capped_and_backspace_still_works_at_the_cap(self):
        editor = AnswerEditor(limit=3)
        results = feed_all(editor, keys("12345"))
        self.assertEqual([r.action for r in results], [EDITED] * 3 + [NONE] * 2)
        self.assertEqual(editor.text, "123")
        editor.feed(127)
        editor.feed(ord("9"))
        self.assertEqual(editor.text, "129")

    def test_default_cap_matches_the_constant(self):
        editor = AnswerEditor()
        feed_all(editor, keys("1" * (m.MAX_ANSWER + 10)))
        self.assertEqual(len(editor.text), m.MAX_ANSWER)

    def test_matches_a_reference_model_on_random_keystrokes(self):
        rng = random.Random(1234)
        pool = [k for k in range(-1, 140) if k != 27] + [263, 343, 410]  # escapes: own tests
        for _ in range(200):
            editor, model, submitted = AnswerEditor(limit=8), [], []
            for _ in range(60):
                key = rng.choice(pool)
                edit = editor.feed(key)
                if key in (10, 13, 343):
                    submitted.append("".join(model))
                    self.assertEqual(edit, Edit(SUBMIT, submitted[-1]))
                    model = []
                elif key in (8, 127, 263):
                    if model:
                        model.pop()
                elif 32 <= key <= 126 and len(model) < 8:
                    model.append(chr(key))
                self.assertEqual(editor.text, "".join(model))
                self.assertTrue(editor.text.isascii() and editor.text.isprintable()
                                or editor.text == "")
                self.assertLessEqual(len(editor.text), 8)

    def test_lone_escape_is_ignored_and_ends_at_the_next_idle_tick(self):
        editor = AnswerEditor()
        feed_all(editor, keys("1"))
        self.assertEqual(editor.feed(m.ESC), Edit(NONE))
        self.assertEqual(editor.feed(m.KEY_IDLE), Edit(NONE))
        feed_all(editor, keys("2"))
        self.assertEqual(editor.text, "12")

    def test_unrecognized_csi_sequences_are_swallowed(self):
        for sequence in ("\x1b[A", "\x1b[1;5C", "\x1b[3~", "\x1b[H", "\x1b[?25h"):
            editor = AnswerEditor()
            feed_all(editor, keys("5" + sequence + "6"))
            self.assertEqual(editor.text, "56", repr(sequence))

    def test_ss3_sequences_are_swallowed(self):
        editor = AnswerEditor()
        feed_all(editor, keys("5\x1bOA\x1bOP6"))
        self.assertEqual(editor.text, "56")

    def test_bracketed_paste_markers_are_swallowed_but_the_paste_is_kept(self):
        editor = AnswerEditor()
        feed_all(editor, keys("\x1b[200~12\x1b[201~"))
        self.assertEqual(editor.text, "12")

    def test_alt_key_is_dropped_not_typed(self):
        editor = AnswerEditor()
        feed_all(editor, keys("\x1b7"))
        self.assertEqual(editor.text, "")
        feed_all(editor, keys("8"))
        self.assertEqual(editor.text, "8")

    def test_escape_then_a_control_key_still_acts(self):
        editor = AnswerEditor()
        feed_all(editor, keys("4"))
        editor.feed(m.ESC)
        self.assertEqual(editor.feed(10), Edit(SUBMIT, "4"))
        editor.feed(m.ESC)
        self.assertEqual(editor.feed(m.KEY_RESIZE), Edit(RESIZE))
        editor.feed(m.ESC)
        self.assertEqual(editor.feed(4), Edit(EOF))
        feed_all(editor, keys("1"))
        editor.feed(m.ESC)
        self.assertEqual(editor.feed(127), Edit(EDITED))  # Backspace after Esc
        self.assertEqual(editor.text, "")

    def test_double_escape_and_escape_inside_a_sequence_recover(self):
        editor = AnswerEditor()
        feed_all(editor, [m.ESC, m.ESC] + keys("[A") + keys("3"))
        self.assertEqual(editor.text, "3")
        editor = AnswerEditor()
        feed_all(editor, keys("\x1b[1") + [m.ESC] + keys("[B") + keys("4"))
        self.assertEqual(editor.text, "4")

    def test_truncated_or_aborted_sequences_do_not_wedge_the_editor(self):
        editor = AnswerEditor()
        feed_all(editor, keys("\x1b[1") + [m.KEY_IDLE] + keys("2"))   # cut off mid-sequence
        self.assertEqual(editor.text, "2")
        editor = AnswerEditor()
        feed_all(editor, keys("\x1b[1") + [10])                       # control byte aborts
        self.assertEqual(editor.text, "")
        feed_all(editor, keys("9"))
        self.assertEqual(editor.text, "9")
        editor = AnswerEditor()
        feed_all(editor, keys("\x1b[") + [200] + keys("7"))           # non-ASCII aborts
        self.assertEqual(editor.text, "7")

    def test_inserting_a_whole_escape_sequence_never_changes_the_text(self):
        rng = random.Random(99)
        pool = [k for k in range(-1, 140) if k not in (27, 10, 13, 4, 26)] + [263]
        sequences = [keys(x) for x in ("\x1b[A", "\x1b[1;5C", "\x1b[200~", "\x1b[201~",
                                        "\x1bOP", "\x1b9", "\x1b[3~")] + [[m.ESC, m.KEY_IDLE]]
        for _ in range(300):
            stream = [rng.choice(pool) for _ in range(30)]
            plain = AnswerEditor(limit=40)
            feed_all(plain, stream)
            noisy, at = AnswerEditor(limit=40), rng.randrange(len(stream) + 1)
            feed_all(noisy, stream[:at] + rng.choice(sequences) + stream[at:])
            self.assertEqual(noisy.text, plain.text)

    def test_key_constants_match_curses_when_available(self):
        try:
            import curses
        except ImportError:
            self.skipTest("curses not available")
        self.assertEqual(m.KEY_RESIZE, curses.KEY_RESIZE)
        self.assertEqual(m.KEY_BACKSPACE, curses.KEY_BACKSPACE)
        self.assertEqual(m.KEY_ENTER, curses.KEY_ENTER)

    def test_module_never_imports_curses(self):
        with open(m.__file__) as handle:
            self.assertNotIn("import curses", handle.read())


class ViewModelTests(unittest.TestCase):
    def game(self, n=3, timed=True, limit=15.0):
        return Game([Question(i, "{} + 1".format(i), i + 1, "add") for i in range(n)],
                    time_limit=limit, timed=timed)

    def test_view_is_immutable_and_transitions_return_new_views(self):
        view = View(number=1)
        with self.assertRaises(FrozenInstanceError):
            view.number = 2
        updated = m.with_buffer(view, "5")
        self.assertEqual((view.buffer, updated.buffer), ("", "5"))

    def test_question_view_for_a_timed_game(self):
        game = self.game()
        view = m.question_view(game, game.questions[0])
        self.assertEqual((view.number, view.total, view.text), (1, 3, "0 + 1"))
        self.assertEqual((view.fraction, view.seconds, view.flash), (1.0, 15.0, None))

    def test_question_view_untimed_has_no_bar(self):
        game = self.game(timed=False)
        self.assertIsNone(m.question_view(game, game.questions[0]).fraction)

    def test_question_view_carries_running_totals(self):
        game = self.game()
        game.start_question(0.0)
        game.submit(0, "1", 1.0)
        view = m.question_view(game, game.questions[1])
        self.assertEqual((view.number, view.score, view.streak, view.best_streak),
                         (2, game.score, 1, 1))
        self.assertGreater(view.score, 0)

    def test_clock_fraction_is_clamped(self):
        view = View(fraction=1.0, seconds=15.0)
        self.assertEqual(m.with_clock(view, 7.5, 15.0).fraction, 0.5)
        self.assertEqual(m.with_clock(view, 99.0, 15.0).fraction, 1.0)
        below = m.with_clock(view, -3.0, 15.0)
        self.assertEqual((below.fraction, below.seconds), (0.0, 0.0))

    def test_invalid_message_keeps_everything_else(self):
        view = m.with_invalid(View(score=10, buffer="x"))
        self.assertEqual(view.message, m.INVALID_MESSAGE)
        self.assertEqual((view.score, view.buffer), (10, "x"))

    def test_outcome_views(self):
        game = self.game()
        game.start_question(0.0)
        question = game.questions[0]
        base = m.question_view(game, question)
        outcome = game.submit(0, "1", 0.0)
        right = m.outcome_view(base, game, question, outcome)
        self.assertEqual(right.flash, "correct")
        self.assertEqual(right.message, "Correct! +{} points".format(outcome.points))
        self.assertEqual((right.score, right.streak), (game.score, 1))

        game.start_question(1.0)
        question = game.questions[1]
        base = m.with_clock(m.question_view(game, question), 5.0, 15.0)
        wrong = m.outcome_view(base, game, question, game.submit(1, "9", 2.0))
        self.assertEqual((wrong.flash, wrong.message), ("wrong", "Not quite. Answer: 2"))
        self.assertEqual(wrong.streak, 0)
        self.assertEqual(wrong.fraction, base.fraction)  # bar freezes where it was

    def test_timeout_flash_empties_the_bar(self):
        game = self.game(limit=1.0)
        game.start_question(0.0)
        question = game.questions[0]
        base = m.with_clock(m.question_view(game, question), 0.4, 1.0)
        flashed = m.outcome_view(base, game, question, game.expire(0, 5.0))
        self.assertEqual((flashed.flash, flashed.fraction, flashed.seconds),
                         ("timeout", 0.0, 0.0))
        self.assertEqual(flashed.message, "Too slow! Answer: 1")

    def test_every_flash_kind_has_a_duration(self):
        for kind in ("correct", "wrong", "timeout"):
            self.assertGreater(m.FLASH_SECONDS[kind], 0)
        self.assertLess(m.FLASH_SECONDS["correct"], m.FLASH_SECONDS["wrong"])

    def test_final_view(self):
        game = self.game(n=2)
        game.start_question(0.0)
        game.submit(0, "1", 0.0)
        game.start_question(1.0)
        game.expire(1, 99.0)
        view = m.final_view(game)
        self.assertTrue(view.finished)
        self.assertEqual((view.correct, view.rounds, view.best_streak, view.score),
                         (1, 2, 1, game.score))

    def test_key_wait_is_a_tick_but_never_past_the_deadline(self):
        self.assertEqual(m.key_wait_ms(10.0, True), m.TICK_MS)
        self.assertEqual(m.key_wait_ms(0.02, True), 20)
        self.assertEqual(m.key_wait_ms(0.0201, True), 21)  # rounds up, never early
        self.assertEqual(m.key_wait_ms(0.0001, True), 1)
        self.assertEqual(m.key_wait_ms(float("inf"), False), m.TICK_MS)

    def test_outcome_kinds_cover_what_the_engine_can_resolve(self):
        game = self.game()
        game.start_question(0.0)
        for kind in ("correct", "wrong", "timeout"):
            m.outcome_view(View(), game, game.questions[0], Outcome(kind))


def text_of(draws):
    return "\n".join(d[2] for d in draws)


class LayoutTests(unittest.TestCase):
    def view(self, **kw):
        base = dict(number=3, total=20, text="6 x 7", score=1200, streak=4, best_streak=5,
                    fraction=0.5, seconds=7.5)
        base.update(kw)
        return View(**base)

    def test_score_and_streak_always_present(self):
        out = text_of(m.layout(self.view(), 24, 80))
        for part in ("Score 1200", "Streak 4", "Best 5", "Question 3/20", "6 x 7", "7.5s"):
            self.assertIn(part, out)

    def test_stats_are_present_in_every_state(self):
        for view in (self.view(), self.view(flash="correct", message="Correct! +1 points"),
                     self.view(flash="timeout", fraction=0.0, seconds=0.0), self.view(buffer="12")):
            out = text_of(m.layout(view, 12, 40))
            self.assertIn("Score 1200", out)
            self.assertIn("Streak 4", out)

    def test_typed_answer_and_cursor(self):
        self.assertIn("> 42_", text_of(m.layout(self.view(buffer="42"), 24, 80)))
        flashing = text_of(m.layout(self.view(buffer="42", flash="wrong", message="x"), 24, 80))
        self.assertIn("> 42", flashing)
        self.assertNotIn("> 42_", flashing)

    def test_long_answer_shows_its_tail(self):
        out = text_of(m.layout(self.view(buffer="1" * 30 + "789"), 24, 40))
        self.assertIn("789_", out)

    def test_bar_cells(self):
        self.assertEqual(m.bar_cells(1.0, 30), 30)
        self.assertEqual(m.bar_cells(0.5, 30), 15)
        self.assertEqual(m.bar_cells(0.001, 30), 1)
        self.assertEqual(m.bar_cells(0.0, 30), 0)
        self.assertEqual(m.bar_cells(-1.0, 30), 0)
        self.assertEqual(m.bar_cells(5.0, 30), 30)
        self.assertEqual(m.bar_cells(0.5, 0), 0)
        self.assertEqual(m.bar_cells(float("nan"), 30), 0)

    def test_bar_never_grows_as_time_drains(self):
        cells = [m.bar_cells(f / 100, 47) for f in range(100, -1, -1)]
        self.assertEqual(cells, sorted(cells, reverse=True))
        self.assertEqual((cells[0], cells[-1]), (47, 0))

    def test_bar_row_always_spans_the_same_width(self):
        for fraction in (1.0, 0.7, 0.33, 0.01, 0.0):
            draws = m.layout(self.view(fraction=fraction), 24, 80)
            bar = sum(len(d[2]) for d in draws if d[0] == 3 and d[2][0] in "█░")
            self.assertEqual(bar, 80 - 2 - len("Time ") - len("  7.5s"))

    def test_bar_color_by_time_left(self):
        def style(fraction):
            draws = m.layout(self.view(fraction=fraction), 24, 80)
            return next(d[3] for d in draws if d[0] == 3 and d[2].startswith("█"))
        self.assertEqual([style(0.9), style(0.4), style(0.1)], ["bar_ok", "bar_warn", "bar_low"])

    def test_untimed_has_no_bar_row(self):
        draws = m.layout(self.view(fraction=None), 24, 80)
        self.assertFalse([d for d in draws if d[0] == 3])

    def test_flash_styles_and_ascii_fallback(self):
        good = m.layout(self.view(flash="correct", message="Correct! +100 points"), 24, 80)
        bad = m.layout(self.view(flash="wrong", message="Not quite. Answer: 4"), 24, 80, False)
        self.assertIn("flash_good", [d[3] for d in good])
        self.assertIn("flash_bad", [d[3] for d in bad])
        self.assertNotIn("█", text_of(bad))
        self.assertIn("#", text_of(bad))

    def test_timeout_flash_is_a_bad_flash(self):
        draws = m.layout(self.view(flash="timeout", fraction=0.0, message="Too slow!"), 24, 80)
        self.assertEqual({d[3] for d in draws if d[2].strip().startswith("Too slow")},
                         {"flash_bad"})

    def test_every_size_stays_inside_the_window(self):
        views = (self.view(), self.view(flash="wrong", message="x" * 90, buffer="9" * 200),
                 View(finished=True, score=99999, correct=20, rounds=20))
        for rows in range(0, 30):
            for cols in range(0, 100, 3):
                for view in views:
                    for row, col, text, _ in m.layout(view, rows, cols):
                        self.assertTrue(0 <= row < rows and 0 <= col, (rows, cols, row, col))
                        self.assertLessEqual(col + len(text), cols, (rows, cols, text))
                        self.assertTrue(text)

    def test_too_small_threshold(self):
        self.assertFalse(m.too_small(m.MIN_ROWS, m.MIN_COLS))
        self.assertTrue(m.too_small(m.MIN_ROWS - 1, m.MIN_COLS))
        self.assertTrue(m.too_small(m.MIN_ROWS, m.MIN_COLS - 1))

    def test_too_small_shows_only_the_resize_message(self):
        out = text_of(m.layout(self.view(), m.MIN_ROWS - 1, 80))
        self.assertIn("Terminal too small", out)
        self.assertIn("need 40x12, have 80x11", out)
        self.assertNotIn("Score", out)
        self.assertNotIn("Score", text_of(m.layout(View(finished=True), 5, 20)))

    def test_exactly_minimum_size_shows_the_whole_game(self):
        out = text_of(m.layout(self.view(), m.MIN_ROWS, m.MIN_COLS))
        for part in ("MATH BLITZ", "Score", "What is 6 x 7?", "Enter: answer"):
            self.assertIn(part, out)

    def test_game_over_screen(self):
        out = text_of(m.layout(View(finished=True, score=2210, correct=14, rounds=20,
                                    best_streak=6), 24, 80))
        for part in ("GAME OVER", "Final score: 2210", "Correct: 14/20", "Best streak: 6",
                     "Press any key"):
            self.assertIn(part, out)

    def test_layout_is_pure(self):
        view = self.view()
        self.assertEqual(m.layout(view, 24, 80), m.layout(view, 24, 80))


if __name__ == "__main__":
    unittest.main()
