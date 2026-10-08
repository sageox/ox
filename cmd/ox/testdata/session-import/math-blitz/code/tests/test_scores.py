import io
import json
import os
import tempfile
import unittest
from contextlib import redirect_stdout
from datetime import datetime, timezone
from pathlib import Path
from unittest import mock

from math_blitz import scores
from math_blitz.engine import AdaptiveGame, Game
from math_blitz.questions import Question
from math_blitz.runner import offer_high_score, run_is_ranked
from math_blitz.scores import ScoreEntry, ScoreStore, ScoresReadOnly
from math_blitz.terminal import Reply


def entry(score, streak=0, date="2026-10-01T00:00:00Z", mode="ranked", **kw):
    return ScoreEntry(mode, "P", score, streak, date, **kw)


class TempDirCase(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.dir = Path(tmp.name) / "data"
        self.path = self.dir / "scores.json"

    def write(self, data):
        self.dir.mkdir(parents=True, exist_ok=True)
        self.path.write_text(data if isinstance(data, str) else json.dumps(data))


class PathTests(unittest.TestCase):
    def test_platform_locations(self):
        home = "/home/u"
        self.assertEqual(scores.default_path({}, "darwin", home),
                         "/home/u/Library/Application Support/math-blitz/scores.json")
        self.assertEqual(scores.default_path({}, "linux", home),
                         "/home/u/.local/share/math-blitz/scores.json")
        self.assertEqual(scores.default_path({"XDG_DATA_HOME": "/x"}, "linux", home),
                         "/x/math-blitz/scores.json")
        self.assertEqual(scores.default_path({"XDG_DATA_HOME": "rel"}, "linux", home),
                         "/home/u/.local/share/math-blitz/scores.json")
        self.assertEqual(scores.default_path({"LOCALAPPDATA": r"C:\L", "APPDATA": r"C:\R"},
                                             "win32", r"C:\Users\u"),
                         r"C:\L\math-blitz\scores.json")
        self.assertEqual(scores.default_path({"APPDATA": r"C:\R"}, "win32", r"C:\Users\u"),
                         r"C:\R\math-blitz\scores.json")

    def test_override_wins(self):
        self.assertEqual(Path(scores.default_path({"MATH_BLITZ_HOME": "/t"}, "darwin", "/h")),
                         Path("/t/scores.json"))


class TableTests(TempDirCase):
    def test_ordering_ties_and_trim(self):
        store = ScoreStore(self.path)
        for i in range(12):
            store.add(entry(100 + i, date="2026-10-01T00:00:%02dZ" % i))
        board = store.board("ranked")
        self.assertEqual(len(board), 10)
        self.assertEqual([e.score for e in board], list(range(111, 101, -1)))
        store.add(entry(111, streak=3))   # same score, longer streak wins
        store.add(entry(111, streak=3, date="2026-10-02T00:00:00Z"))  # later loses tie
        board = store.board("ranked")
        self.assertEqual([(e.score, e.best_streak) for e in board[:3]],
                         [(111, 3), (111, 3), (111, 0)])
        self.assertEqual(board[1].date, "2026-10-02T00:00:00Z")

    def test_projected_rank(self):
        store = ScoreStore(self.path)
        self.assertIsNone(store.projected_rank("ranked", 0, 0))
        self.assertEqual(store.projected_rank("ranked", 1, 0), 1)
        for i in range(10):
            store.add(entry(100 + i))
        self.assertIsNone(store.projected_rank("ranked", 100, 0))   # ties rank last
        self.assertEqual(store.projected_rank("ranked", 100, 1), 10)
        self.assertEqual(store.projected_rank("ranked", 500, 0), 1)

    def test_boards_are_separate_by_mode_and_rules(self):
        store = ScoreStore(self.path)
        store.add(entry(100))
        store.add(entry(200, mode="adaptive", difficulty=7))
        store.add(entry(300, rules_version=2))
        self.assertEqual([e.score for e in store.board("ranked")], [100])
        self.assertEqual([e.score for e in store.board("adaptive")], [200])
        self.assertEqual([e.score for e in store.board("ranked", 2)], [300])
        self.assertEqual(store.board("adaptive")[0].difficulty, 7)

    def test_add_returns_rank_and_none_when_cut(self):
        store = ScoreStore(self.path)
        for i in range(10):
            store.add(entry(100 + i))
        self.assertEqual(store.add(entry(1000)), 1)
        self.assertIsNone(store.add(entry(1)))

    def test_round_trip_and_unknown_fields_preserved(self):
        self.write({"schema_version": 1, "future_top": [1],
                    "entries": [dict(entry(5).to_dict(), future="keep")]})
        store = ScoreStore(self.path)
        store.add(entry(9))
        data = json.loads(self.path.read_text())
        self.assertEqual(data["future_top"], [1])
        self.assertEqual([e["score"] for e in data["entries"]], [9, 5])
        self.assertEqual(data["entries"][1]["future"], "keep")
        self.assertEqual(ScoreStore(self.path).board("ranked")[0].score, 9)

    def test_second_instance_scores_are_not_lost(self):
        a, b = ScoreStore(self.path), ScoreStore(self.path)
        a.add(entry(1))
        b.add(entry(2))
        self.assertEqual(len(ScoreStore(self.path).entries), 2)

    def test_name_sanitizing(self):
        self.assertEqual(scores.sanitize_name("  Ann\x1b[31m  "), "Ann[31m")
        self.assertEqual(scores.sanitize_name("\x07\n "), "Anonymous")
        self.assertEqual(len(scores.sanitize_name("x" * 99)), scores.NAME_LIMIT)

    def test_format_board_shows_level_only_when_present(self):
        text = scores.format_board([entry(100)], highlight=1)
        self.assertNotIn("Level", text)
        self.assertIn("<-- you", text)
        self.assertIn("Level", scores.format_board([entry(100, difficulty=4)]))


class DamageTests(TempDirCase):
    def assert_quarantined(self, content):
        self.write(content)
        store = ScoreStore(self.path)
        self.assertEqual(store.entries, [])
        self.assertFalse(self.path.exists())
        kept = list(self.dir.glob("scores.corrupt-*.json"))
        self.assertEqual(len(kept), 1)
        self.assertIn(kept[0].name, store.notice)
        store.add(entry(5))   # a fresh file is created beside the moved one
        self.assertEqual(len(ScoreStore(self.path).entries), 1)
        self.assertEqual(len(list(self.dir.glob("scores.corrupt-*.json"))), 1)

    def test_corrupt_files_are_moved_aside(self):
        for content in ("", "{not json", "[]", '{"schema_version": 0, "entries": []}',
                        '{"schema_version": 1, "entries": [{"score": "x"}]}',
                        '{"schema_version": 1, "entries": [{"score": true}]}'):
            with self.subTest(content=content):
                self.setUp()
                self.assert_quarantined(content)

    def test_non_utf8_is_quarantined(self):
        self.dir.mkdir(parents=True)
        self.path.write_bytes(b"\xff\xfe\x00")
        self.assertEqual(ScoreStore(self.path).entries, [])
        self.assertEqual(len(list(self.dir.glob("scores.corrupt-*.json"))), 1)

    def test_failed_replace_keeps_old_file_and_cleans_up(self):
        store = ScoreStore(self.path)
        store.add(entry(5))
        before = self.path.read_bytes()
        with mock.patch("os.replace", side_effect=OSError("disk gone")):
            with self.assertRaises(OSError):
                store.add(entry(9))
        self.assertEqual(self.path.read_bytes(), before)
        self.assertEqual(sorted(p.name for p in self.dir.iterdir()),
                         ["scores.json", "scores.json.lock"])  # no *.tmp left behind

    def test_data_is_fsynced_before_replace(self):
        calls = []
        real_replace = os.replace
        with mock.patch.object(scores, "_fsync", lambda fd: calls.append("fsync")), \
                mock.patch("os.replace",
                           lambda a, b: (calls.append("replace"), real_replace(a, b))):
            ScoreStore(self.path).add(entry(5))
        self.assertEqual(calls[:2], ["fsync", "replace"])

    def test_newer_schema_is_read_only_and_untouched(self):
        doc = {"schema_version": 99, "entries": [entry(5).to_dict(), {"junk": 1}]}
        self.write(doc)
        before = self.path.read_bytes()
        store = ScoreStore(self.path)
        self.assertTrue(store.read_only)
        self.assertEqual([e.score for e in store.board("ranked")], [5])
        with self.assertRaises(ScoresReadOnly):
            store.add(entry(9))
        self.assertEqual(self.path.read_bytes(), before)

    def test_migration_runs_and_keeps_backup_of_old_file(self):
        self.write({"schema_version": 1, "entries": [{
            "mode": "ranked", "rules_version": 1, "name": "A", "score": 5,
            "best_streak": 1, "date": "d", "difficulty": None}]})
        original = self.path.read_bytes()

        def v1_to_v2(data):
            data = dict(data, schema_version=2)
            data["entries"] = [dict(e, tag="v2") for e in data["entries"]]
            return data

        with mock.patch.object(scores, "SCHEMA_VERSION", 2), \
                mock.patch.object(scores, "MIGRATIONS", {1: v1_to_v2}):
            store = ScoreStore(self.path)
            self.assertEqual(self.path.read_bytes(), original)  # load never writes
            store.add(entry(9))
            self.assertEqual((self.dir / "scores.json.v1.bak").read_bytes(), original)
            data = json.loads(self.path.read_text())
            self.assertEqual(data["schema_version"], 2)
            self.assertEqual(data["entries"][1]["tag"], "v2")


class FakeTerminal:
    def __init__(self, replies=()):
        self.replies, self.prompts, self.cleared = list(replies), 0, 0

    def clear_pending(self):
        self.cleared += 1

    def read(self, deadline):
        self.prompts += 1
        if isinstance(self.replies[0], BaseException):
            raise self.replies.pop(0)
        return Reply(self.replies.pop(0), 0.0)


def finished_game(score, timed=True, streak=3, rounds=2):
    game = Game([Question(i, "6 / 2", 3, "/") for i in range(rounds)], timed=timed)
    game.rounds, game.score, game.best_streak = rounds, score, streak
    return game


class GameOverTests(TempDirCase):
    NOW = datetime(2026, 10, 1, 12, 0, 0, tzinfo=timezone.utc)

    def run_offer(self, game, terminal, store):
        out = io.StringIO()
        with redirect_stdout(out):
            offer_high_score(game, terminal, store, self.NOW)
        return out.getvalue()

    def test_name_requested_only_when_score_qualifies(self):
        store = ScoreStore(self.path)
        for i in range(10):
            store.add(entry(1000 + i))
        low = FakeTerminal([])
        text = self.run_offer(finished_game(500), low, store)
        self.assertEqual(low.prompts, 0)
        self.assertNotIn("New high score", text)
        self.assertIn("High scores", text)
        high = FakeTerminal(["Zed"])
        text = self.run_offer(finished_game(5000), high, store)
        self.assertEqual((high.prompts, high.cleared), (1, 1))
        self.assertIn("placed #1", text)
        self.assertIn("<-- you", text)
        top = store.board("ranked")[0]
        self.assertEqual((top.name, top.score, top.best_streak, top.date),
                         ("Zed", 5000, 3, "2026-10-01T12:00:00Z"))

    def test_blank_or_missing_name_uses_default(self):
        store = ScoreStore(self.path)
        self.run_offer(finished_game(100), FakeTerminal([""]), store)
        self.run_offer(finished_game(200), FakeTerminal([None]), store)
        self.assertEqual([e.name for e in store.board("ranked")], ["Anonymous"] * 2)

    def test_ctrl_c_at_name_prompt_skips_saving(self):
        store = ScoreStore(self.path)
        text = self.run_offer(finished_game(100), FakeTerminal([KeyboardInterrupt()]), store)
        self.assertIn("not saved", text)
        self.assertFalse(self.path.exists())

    def test_ineligible_runs_are_never_recorded(self):
        store = ScoreStore(self.path)
        term = FakeTerminal([])
        quit_game = finished_game(500)
        quit_game.stopped = True
        short_game = finished_game(500, rounds=2)
        short_game.rounds = 1
        for game in (finished_game(500, timed=False), quit_game, short_game,
                     finished_game(0)):
            self.run_offer(game, term, store)
        self.assertEqual(term.prompts, 0)
        self.assertFalse(self.path.exists())
        adaptive = AdaptiveGame(["+"])
        adaptive.rounds = 1
        self.assertFalse(run_is_ranked(adaptive))

    def test_save_failure_is_reported_not_raised(self):
        store = ScoreStore(self.path)
        with mock.patch("os.replace", side_effect=OSError("disk gone")):
            text = self.run_offer(finished_game(100), FakeTerminal(["A"]), store)
        self.assertIn("Could not save", text)

    def test_read_only_store_shows_table_without_prompt(self):
        self.write({"schema_version": 99, "entries": [entry(5).to_dict()]})
        term = FakeTerminal([])
        text = self.run_offer(finished_game(100), term, ScoreStore(self.path))
        self.assertEqual(term.prompts, 0)
        self.assertIn("newer Math Blitz", text)


if __name__ == "__main__":
    unittest.main()
