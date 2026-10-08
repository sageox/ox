import ast
import random
import subprocess
import sys
import unittest
from collections import Counter
from pathlib import Path
from unittest.mock import Mock

from math_blitz.engine import Game
from math_blitz.questions import Question, make_question, ranked_questions


class EngineTests(unittest.TestCase):
    def game(self, count=3, timed=True):
        return Game([Question(i, '6 / 2', 3, '/') for i in range(count)], timed=timed)

    def test_deadline_boundary_and_late_answer(self):
        for at, expected in [(24.999, 'correct'), (25, 'timeout'), (130, 'timeout')]:
            with self.subTest(at=at):
                game = self.game()
                game.start_question(10)
                self.assertEqual(game.submit(0, '3', at).kind, expected)
                self.assertEqual(game.rounds, 1)

    def test_invalid_input_preserves_absolute_deadline(self):
        game = self.game()
        game.start_question(10)
        self.assertEqual(game.submit(0, 'oops', 20).kind, 'invalid')
        self.assertEqual(game.deadline, 25)
        self.assertEqual(game.rounds, 0)
        self.assertEqual(game.expire(0, 24).kind, 'pending')
        self.assertEqual(game.expire(0, 25).kind, 'timeout')
        self.assertEqual(game.score, 0)

    def test_one_numeric_attempt_and_stale_events(self):
        game = self.game()
        game.start_question(0)
        self.assertEqual(game.submit(0, '4', 1).kind, 'wrong')
        self.assertEqual(game.submit(0, '3', 2).kind, 'ignored')
        game.start_question(2)
        self.assertEqual(game.submit(0, '3', 3).kind, 'ignored')
        self.assertEqual(game.expire(0, 100).kind, 'ignored')
        self.assertEqual(game.active.id, 1)
        self.assertEqual(game.score, 0)

    def test_wrong_answer_resets_streak_without_removing_earned_points(self):
        game = self.game(4)
        for question_id in range(2):
            game.start_question(question_id * 20)
            game.submit(question_id, '3', question_id * 20 + 3)
        earned = game.score
        self.assertEqual(game.streak, 2)
        game.start_question(40)
        outcome = game.submit(2, '999', 43)
        self.assertEqual((outcome.kind, outcome.points), ('wrong', 0))
        self.assertEqual((game.score, game.correct, game.rounds), (earned, 2, 3))
        self.assertEqual((game.streak, game.best_streak), (0, 2))
        game.start_question(60)
        self.assertEqual(game.submit(3, '3', 63).points, 140)
        self.assertEqual(game.streak, 1)

    def test_timeout_counts_as_one_miss_and_resets_streak(self):
        for through_submission in (False, True):
            with self.subTest(through_submission=through_submission):
                game = self.game()
                game.start_question(0)
                game.submit(0, '3', 3)
                earned = game.score
                game.start_question(20)
                if through_submission:
                    outcome = game.submit(1, '3', 35)
                else:
                    outcome = game.expire(1, 35)
                self.assertEqual((outcome.kind, outcome.points), ('timeout', 0))
                self.assertEqual((game.score, game.correct, game.rounds), (earned, 1, 2))
                self.assertEqual(game.rounds - game.correct, 1)
                self.assertEqual((game.streak, game.best_streak), (0, 1))
                self.assertIsNone(game.active)
                game.start_question(40)
                self.assertEqual(game.submit(2, '3', 43).points, 140)

    def test_spamming_correct_answers_cannot_multiply_rewards(self):
        game = self.game()
        game.start_question(0)
        self.assertEqual(game.submit(0, '3', 3).points, 140)
        for _ in range(100):
            self.assertEqual(game.submit(0, '3', 3).kind, 'ignored')
            self.assertEqual(game.expire(0, 15).kind, 'ignored')
        self.assertEqual((game.score, game.streak, game.correct, game.rounds), (140, 1, 1, 1))
        game.start_question(20)
        for _ in range(100):
            self.assertEqual(game.submit(0, '3', 21).kind, 'ignored')
        self.assertEqual(game.active.id, 1)
        self.assertEqual((game.score, game.streak, game.correct, game.rounds), (140, 1, 1, 1))

    def test_wrong_guess_cannot_be_retried_and_timeout_cannot_be_rescored(self):
        for resolution in ('wrong', 'timeout'):
            with self.subTest(resolution=resolution):
                game = self.game()
                game.start_question(0)
                if resolution == 'wrong':
                    game.submit(0, '999', 1)
                    retry_at = 2
                else:
                    game.expire(0, 15)
                    retry_at = 15
                for _ in range(100):
                    self.assertEqual(game.submit(0, '3', retry_at).kind, 'ignored')
                    self.assertEqual(game.expire(0, 16).kind, 'ignored')
                self.assertEqual((game.score, game.streak, game.correct, game.rounds), (0, 0, 0, 1))

    def test_invalid_input_spam_cannot_extend_deadline(self):
        game = self.game()
        game.start_question(0)
        for _ in range(100):
            self.assertEqual(game.submit(0, 'invalid', 14).kind, 'invalid')
        self.assertEqual(game.deadline, 15)
        self.assertEqual(game.rounds, 0)
        self.assertEqual(game.submit(0, 'invalid', 15).kind, 'timeout')
        self.assertEqual((game.score, game.correct, game.rounds), (0, 0, 1))

    def test_streak_speed_cap_and_reset(self):
        game = self.game(10)
        for i in range(7):
            game.start_question(i * 20)
            outcome = game.submit(i, '3', i * 20)
            self.assertEqual(outcome.points, 150 + min(50, i * 10))
        game.start_question(140)
        game.submit(7, '4', 141)
        self.assertEqual(game.streak, 0)
        self.assertEqual(game.best_streak, 7)
        game.start_question(160)
        self.assertEqual(game.submit(8, '3', 172).points, 110)
        game.start_question(180)
        game.expire(9, 195)
        self.assertEqual(game.streak, 0)
        self.assertTrue(game.finished)
        with self.assertRaises(ValueError):
            game.start_question(200)

    def test_practice_has_no_expiry_or_speed_bonus(self):
        game = self.game(timed=False)
        game.start_question(0)
        self.assertEqual(game.expire(0, 120).kind, 'pending')
        self.assertEqual(game.submit(0, '3', 120).points, 100)

    def test_quit_does_not_count_question(self):
        game = self.game()
        game.start_question(0)
        self.assertEqual(game.submit(0, ' QUIT ', 1).kind, 'quit')
        self.assertTrue(game.finished)
        self.assertEqual(game.rounds, 0)

    def test_duplicate_ids_and_invalid_times_rejected(self):
        q = Question(1, '1 + 1', 2, '+')
        with self.assertRaises(ValueError):
            Game([q, q])
        for limit in (0, -1, float('nan'), float('inf')):
            with self.assertRaises(ValueError):
                Game([q], time_limit=limit)
        game = self.game()
        game.start_question(10)
        with self.assertRaises(ValueError):
            game.submit(0, '3', 9)


class GenerationTests(unittest.TestCase):
    def test_every_subtraction_operand_pair_is_nonnegative(self):
        # Exhaust the generator's entire operand range, including reversed and
        # equal draws, rather than relying on a random sample to hit boundaries.
        for first in range(1, 51):
            for second in range(1, 51):
                with self.subTest(first=first, second=second):
                    rng = Mock(spec=random.Random)
                    rng.randint.side_effect = [first, second]
                    question = make_question(1, '-', rng)
                    a, symbol, b = question.text.split()
                    a, b = int(a), int(b)
                    self.assertEqual(symbol, '-')
                    self.assertEqual(sorted((a, b)), sorted((first, second)))
                    self.assertGreaterEqual(a, b)
                    self.assertEqual(question.answer, a - b)
                    self.assertGreaterEqual(question.answer, 0)

    def test_every_division_draw_produces_an_exact_integer_answer(self):
        for divisor in range(1, 13):
            for quotient in range(1, 13):
                with self.subTest(divisor=divisor, quotient=quotient):
                    rng = Mock(spec=random.Random)
                    rng.randint.side_effect = [divisor, quotient]
                    question = make_question(1, '/', rng)
                    a, symbol, b = question.text.split()
                    a, b = int(a), int(b)
                    self.assertEqual(symbol, '/')
                    self.assertGreater(b, 0)
                    self.assertEqual(a % b, 0)
                    self.assertIsInstance(question.answer, int)
                    self.assertEqual(question.answer * b, a)
                    self.assertEqual(question.answer, quotient)

    def test_balanced_reproducible_run(self):
        questions = ranked_questions(random.Random(7))
        self.assertEqual(questions, ranked_questions(random.Random(7)))
        self.assertEqual(Counter(q.operation for q in questions), dict.fromkeys('+-*/', 5))
        self.assertEqual(len({q.id for q in questions}), 20)

    def test_operation_answers_and_whole_number_division(self):
        rng = random.Random(9)
        for op in '+-*/':
            for i in range(100):
                q = make_question(i, op, rng)
                a, _, b = q.text.split()
                a, b = int(a), int(b)
                expected = {'+': a + b, '-': a - b, '*': a * b, '/': a // b}[op]
                self.assertEqual(q.answer, expected)
                if op == '/':
                    self.assertGreater(b, 0)
                    self.assertEqual(a % b, 0)
                if op == '-':
                    self.assertGreaterEqual(q.answer, 0)


class EntryPointTests(unittest.TestCase):
    def test_pipe_is_explicit_practice_and_quits(self):
        result = subprocess.run([sys.executable, 'math_game.py'], input='bad\n3\nq\n',
                                text=True, capture_output=True, timeout=3)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('Untimed practice', result.stdout)
        self.assertIn('Enter a whole number', result.stdout)
        self.assertIn('Final score:', result.stdout)

    def test_eof_exits(self):
        result = subprocess.run([sys.executable, 'math_game.py'], input='', text=True,
                                capture_output=True, timeout=3)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('Correct: 0/0', result.stdout)

    def test_run_is_finite(self):
        result = subprocess.run([sys.executable, 'math_game.py'], input='0\n' * 25,
                                text=True, capture_output=True, timeout=3)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('[20/20]', result.stdout)
        self.assertNotIn('[21/20]', result.stdout)

    def test_python39_syntax(self):
        for path in [Path('math_game.py')] + list(Path('math_blitz').glob('*.py')):
            ast.parse(path.read_text(), filename=str(path), feature_version=(3, 9))


if __name__ == '__main__':
    unittest.main()
