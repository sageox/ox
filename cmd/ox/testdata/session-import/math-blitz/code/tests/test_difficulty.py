import math
import random
import statistics
import unittest

from math_blitz.difficulty import EloDifficulty
from math_blitz.engine import AdaptiveGame, Game
from math_blitz.questions import Question


class EloDifficultyTests(unittest.TestCase):
    def test_engine_converges_for_synthetic_players(self):
        # Exercise selection AND engine feedback, using the simulation's model.
        # Aggregate independent deterministic trials, rather than asserting a
        # noisy individual player's last few answers happen to hit the target.
        for ability in (0.0, 2.0, 4.0):
            with self.subTest(ability=ability):
                early, late, observed = [], [], []
                for seed in range(20):
                    rng = random.Random(20261001 + seed)
                    policy = EloDifficulty()
                    game = AdaptiveGame(["*"] * 1600, policy, timed=False)
                    probabilities, answers = [], []
                    for question_number in range(1600):
                        request = game.request_question()
                        difficulty = policy.family_ratings[request.level]
                        probability = 1 / (1 + math.exp(difficulty - ability))
                        correct = rng.random() < probability
                        game.start_question(question_number, Question(
                            request.id, "synthetic", 1, request.operation, request.level))
                        outcome = game.submit(request.id, "1" if correct else "0",
                                              question_number)
                        self.assertEqual(outcome.kind, "correct" if correct else "wrong")
                        probabilities.append(probability)
                        answers.append(correct)
                    self.assertTrue(game.finished)
                    early.extend(probabilities[:100])
                    late.extend(probabilities[-300:])
                    observed.extend(answers[-300:])
                self.assertLess(abs(statistics.mean(late) - .775),
                                abs(statistics.mean(early) - .775))
                self.assertGreaterEqual(statistics.mean(late), .75)
                self.assertLessEqual(statistics.mean(late), .80)
                self.assertGreaterEqual(statistics.mean(observed), .75)
                self.assertLessEqual(statistics.mean(observed), .80)

    def test_stays_at_easy_boundary_for_repeated_misses(self):
        policy = EloDifficulty()
        for _ in range(10000):
            level = policy.select("*")
            self.assertEqual(level, 0)
            policy.update("*", level, False)

    def test_reaches_hard_boundary_without_exceeding_it(self):
        policy = EloDifficulty()
        hardest = len(policy.family_ratings) - 1
        for _ in range(10000):
            level = policy.select("*")
            self.assertGreaterEqual(level, 0)
            self.assertLessEqual(level, hardest)
            policy.update("*", level, True)
        self.assertEqual(policy.select("*"), hardest)

    def test_twenty_lucky_answers_cannot_launch_beginner_to_hardest(self):
        policy = EloDifficulty()
        for _ in range(20):
            level = policy.select("*")
            policy.update("*", level, True)
        # One perfect run may move a few neighboring families, never the whole
        # catalog. This pins the production learning rates, not just bounds.
        self.assertLessEqual(policy.select("*"), 4)
        self.assertLess(policy.select("*"), len(policy.family_ratings) - 1)

    def test_operation_state_is_independent_and_selection_is_stable(self):
        policy = EloDifficulty()
        for _ in range(100):
            before = policy.select("+")
            self.assertEqual(policy.select("+"), before)
            policy.update("+", before, True)
        self.assertGreater(policy.select("+"), 0)
        self.assertEqual(policy.select("*"), 0)
        self.assertEqual(policy.select("-"), 0)
        self.assertEqual(policy.select("/"), 0)

    def test_invalid_configuration_and_feedback_are_rejected(self):
        for catalog in ([], [float("nan")], [1, 1], [2, 1]):
            with self.assertRaises(ValueError):
                EloDifficulty(catalog)
        for target in (0, 1, float("inf")):
            with self.assertRaises(ValueError):
                EloDifficulty(target=target)
        for k in (0, -.1, 1.1, float("nan")):
            with self.assertRaises(ValueError):
                EloDifficulty(player_k=k)
        for margin in (0, -1, float("nan"), float("inf")):
            with self.assertRaises(ValueError):
                EloDifficulty(rating_margin=margin)
        for drift in (-1, float("nan"), float("inf")):
            with self.assertRaises(ValueError):
                EloDifficulty(item_drift=drift)
        policy = EloDifficulty()
        for level in (-1, 33, True):
            with self.assertRaises(ValueError):
                policy.update("*", level, True)
        with self.assertRaises(ValueError):
            policy.update("*", 0, 1)
        with self.assertRaises(ValueError):
            policy.select("?")


class RecordingPolicy:
    """An unrelated policy demonstrates engine substitutability."""
    def __init__(self):
        self.calls = []
        self.selections = 0

    def select(self, operation):
        self.selections += 1
        return len(self.calls)

    def update(self, operation, level, correct):
        self.calls.append((operation, level, correct))

    def reset(self):
        self.calls.clear()
        self.selections = 0


class AdaptiveEngineTests(unittest.TestCase):
    def start(self, game, now):
        request = game.request_question()
        question = Question(request.id, "2 * 3", 6, request.operation, request.level)
        game.start_question(now, question)
        return request

    def test_swappable_policy_observes_only_terminal_outcomes_once(self):
        policy = RecordingPolicy()
        game = AdaptiveGame(["*", "+", "/"], policy)
        first = game.request_question()
        self.assertEqual(game.request_question(), first)
        self.assertEqual(policy.selections, 1)
        self.start(game, 0)
        self.assertEqual(game.submit(first.id, "garbage", 1).kind, "invalid")
        self.assertEqual(game.expire(first.id, 2).kind, "pending")
        self.assertEqual(game.submit(999, "6", 2).kind, "ignored")
        self.assertEqual(policy.calls, [])
        game.submit(first.id, "6", 3)
        game.submit(first.id, "6", 4)
        game.expire(first.id, 20)
        self.assertEqual(policy.calls, [("*", 0, True)])
        second = self.start(game, 20)
        game.submit(second.id, "0", 21)
        third = self.start(game, 40)
        game.expire(third.id, 55)
        game.expire(third.id, 56)
        self.assertEqual(policy.calls, [("*", 0, True), ("+", 1, False), ("/", 2, False)])
        self.assertTrue(game.finished)
        self.assertFalse(game.ranked_eligible)
        with self.assertRaises(ValueError):
            game.request_question()

    def test_wrong_question_metadata_or_timestamp_cannot_start(self):
        policy = RecordingPolicy()
        game = AdaptiveGame(["*"], policy)
        request = game.request_question()
        for question in (Question(99, "x", 6, "*", 0),
                         Question(request.id, "x", 6, "+", 0),
                         Question(request.id, "x", 6, "*", 1),
                         Question(request.id, "x", 6, "*")):
            with self.assertRaises(ValueError):
                game.start_question(0, question)
            self.assertIsNone(game.active)
        with self.assertRaises(ValueError):
            game.start_question(float("nan"), Question(request.id, "x", 6, "*", 0))
        self.assertEqual(policy.calls, [])

    def test_quit_stop_and_empty_schedule_do_not_train(self):
        for finish in ("quit", "stop"):
            with self.subTest(finish=finish):
                policy = RecordingPolicy()
                game = AdaptiveGame(["*"], policy)
                request = self.start(game, 0)
                if finish == "quit":
                    game.submit(request.id, "q", 1)
                else:
                    game.stop()
                self.assertEqual(policy.calls, [])
                self.assertEqual(game.rounds, 0)
                self.assertTrue(game.finished)
        self.assertTrue(AdaptiveGame([]).finished)

    def test_deadline_and_scoring_match_fixed_game(self):
        for reply, at in (("6", 3), ("0", 3), ("6", 15)):
            with self.subTest(reply=reply, at=at):
                adaptive = AdaptiveGame(["*"])
                request = self.start(adaptive, 0)
                fixed = Game([Question(request.id, "2 * 3", 6, "*")])
                fixed.start_question(0)
                self.assertEqual(adaptive.submit(request.id, reply, at),
                                 fixed.submit(request.id, reply, at))
                self.assertEqual(adaptive.score, fixed.score)

    def test_instant_answers_and_timeouts_do_not_stick_at_either_edge(self):
        for first_correct in (True, False):
            with self.subTest(first_correct=first_correct):
                policy = EloDifficulty()
                game = AdaptiveGame(["*"] * 10050, policy)
                for i in range(10000):
                    request = self.start(game, i * 20)
                    if first_correct:
                        game.submit(request.id, "6", i * 20)
                    else:
                        game.expire(request.id, i * 20 + 15)
                        self.assertEqual(game.expire(request.id, i * 20 + 16).kind, "ignored")
                    state = policy._operations["*"]
                    self.assertTrue(policy.ability_bounds[0] <= state.ability
                                    <= policy.ability_bounds[1])
                    for rating, (low, high) in zip(state.families, policy.family_bounds):
                        self.assertTrue(low <= rating <= high)
                boundary = 32 if first_correct else 0
                self.assertEqual(policy.select("*"), boundary)
                # Reverse behavior: timeout after perfection, correct after
                # walk-away. Recovery must be bounded, not proportional to how
                # long the player previously stayed at the boundary.
                moved_after = None
                for i in range(50):
                    request = self.start(game, (10000 + i) * 20)
                    if first_correct:
                        game.expire(request.id, (10000 + i) * 20 + 15)
                    else:
                        game.submit(request.id, "6", (10000 + i) * 20)
                    if policy.select("*") != boundary:
                        moved_after = i + 1
                        break
                self.assertIsNotNone(moved_after)
                self.assertLessEqual(moved_after, 40)

    def test_speed_does_not_change_difficulty_feedback(self):
        instant = AdaptiveGame(["*"] * 200)
        slow = AdaptiveGame(["*"] * 200)
        for i in range(200):
            a = self.start(instant, i * 20)
            b = self.start(slow, i * 20)
            self.assertEqual(a.level, b.level)
            instant.submit(a.id, "6", i * 20)
            slow.submit(b.id, "6", i * 20 + 14)
        self.assertEqual(instant.policy._operations, slow.policy._operations)
        self.assertGreater(instant.score, slow.score)

    def test_restart_resets_progress_ratings_and_rejects_abandoned_events(self):
        policy = EloDifficulty(target=.78, player_k=.2, item_k=.005)
        game = AdaptiveGame(["*"] * 100, policy, time_limit=10)
        for i in range(60):
            request = self.start(game, i * 20)
            game.submit(request.id, "6", i * 20)
        self.assertGreater(policy.select("*"), 0)
        abandoned = self.start(game, 1200)
        game.restart()
        self.assertEqual((game.score, game.rounds, game.streak, game.correct,
                          game.best_streak), (0, 0, 0, 0, 0))
        self.assertIsNone(game.active)
        self.assertFalse(game.finished)
        self.assertEqual((policy.target, policy.player_k, policy.item_k), (.78, .2, .005))
        self.assertEqual(game.time_limit, 10)
        fresh = self.start(game, 1300)
        self.assertGreater(fresh.id, abandoned.id)
        self.assertEqual(fresh.level, 0)
        state_before = repr(policy._operations)
        self.assertEqual(game.submit(abandoned.id, "6", 1301).kind, "ignored")
        self.assertEqual(game.expire(abandoned.id, 1400).kind, "ignored")
        self.assertEqual(repr(policy._operations), state_before)
        self.assertEqual(game.rounds, 0)
        self.assertEqual(game.submit(fresh.id, "6", 1301).kind, "correct")

    def test_restart_after_stop_finish_or_pending_request(self):
        for mode in ("stopped", "finished", "requested"):
            with self.subTest(mode=mode):
                game = AdaptiveGame(["*"], timed=False)
                old = game.request_question()
                if mode != "requested":
                    self.start(game, 0)
                    if mode == "stopped":
                        game.stop()
                    else:
                        game.submit(old.id, "6", 1)
                game.restart()
                new = game.request_question()
                self.assertGreater(new.id, old.id)
                self.assertEqual(new.level, 0)
                self.assertFalse(game.timed)
                self.assertFalse(game.finished)


if __name__ == "__main__":
    unittest.main()
