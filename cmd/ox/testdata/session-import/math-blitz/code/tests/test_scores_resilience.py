"""High-score file damage, old formats, ties and the top-10 cutoff.

Every test works inside a temporary directory. A module-level guard points
MATH_BLITZ_HOME at a throwaway directory and fails the run if the user's real
scores file is created or modified.
"""
import hashlib
import json
import os
import re
import select
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from math_blitz import scores
from math_blitz.scores import ScoreEntry, ScoreStore

ROOT = Path(__file__).resolve().parent.parent
_real_path = Path(scores.default_path())          # resolved before any override
_real_before = None
_saved_env = None
_module_tmp = None


def _fingerprint(path):
    if not path.exists():
        return None
    return hashlib.sha256(path.read_bytes()).hexdigest(), path.stat().st_mtime_ns


def setUpModule():
    global _real_before, _saved_env, _module_tmp
    _real_before = _fingerprint(_real_path)
    _saved_env = os.environ.get("MATH_BLITZ_HOME")
    _module_tmp = tempfile.TemporaryDirectory()
    os.environ["MATH_BLITZ_HOME"] = _module_tmp.name


def tearDownModule():
    if _saved_env is None:
        os.environ.pop("MATH_BLITZ_HOME", None)
    else:
        os.environ["MATH_BLITZ_HOME"] = _saved_env
    _module_tmp.cleanup()
    if _fingerprint(_real_path) != _real_before:
        raise AssertionError("tests touched the real scores file " + str(_real_path))


def entry(score, streak=0, date="2026-10-01T00:00:00Z", name="P", mode="ranked", **kw):
    return ScoreEntry(mode, name, score, streak, date, **kw)


class TempCase(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.dir = Path(tmp.name) / "data"
        self.path = self.dir / "scores.json"

    def write_bytes(self, data):
        self.dir.mkdir(parents=True, exist_ok=True)
        self.path.write_bytes(data)

    def copies(self):
        return sorted(self.dir.glob("scores.corrupt-*.json"))


class SafetyGuardTests(unittest.TestCase):
    def test_default_path_is_redirected_to_a_temporary_directory(self):
        path = Path(scores.default_path())
        self.assertEqual(path.parent, Path(_module_tmp.name))
        self.assertNotEqual(path, _real_path)


class CorruptFileTests(TempCase):
    def test_every_truncation_of_a_valid_file_is_kept_and_not_fatal(self):
        good = ScoreStore(self.path)
        good.add(entry(300, 5, name="Ann"))
        good.add(entry(200, 2, name="Bob"))
        full = self.path.read_bytes()
        for cut in range(0, len(full)):
            with self.subTest(cut=cut):
                for old in self.dir.glob("scores*"):
                    old.unlink()
                self.write_bytes(full[:cut])
                store = ScoreStore(self.path)          # must not raise
                self.assertEqual(store.entries, [])
                kept = self.copies()
                self.assertEqual(len(kept), 1)
                self.assertEqual(kept[0].read_bytes(), full[:cut])  # byte-identical
                self.assertIn(kept[0].name, store.notice)
                self.assertFalse(self.path.exists())

    def test_game_keeps_working_after_corruption_and_old_copy_survives(self):
        garbage = b'{"schema_version": 1, "entries": [{"mode": "ranked", "sco'
        self.write_bytes(garbage)
        store = ScoreStore(self.path)
        rank = store.add(entry(100, name="New"))
        self.assertEqual(rank, 1)
        self.assertEqual([e.name for e in ScoreStore(self.path).board("ranked")], ["New"])
        self.assertEqual([p.read_bytes() for p in self.copies()], [garbage])

    def test_repeated_corruption_never_overwrites_an_earlier_copy(self):
        payloads = [b"{broken one", b"{broken two", b"{broken three"]
        for payload in payloads:
            self.write_bytes(payload)
            ScoreStore(self.path)       # quarantines; same-second stamps must not collide
        self.assertEqual(sorted(p.read_bytes() for p in self.copies()), sorted(payloads))

    def test_other_kinds_of_damage_are_kept_too(self):
        cases = {
            "binary": b"\x00\xff\xfe\x80garbage",
            "wrong top level": b"[1, 2, 3]",
            "entries not a list": b'{"schema_version": 1, "entries": {}}',
            "one bad entry spoils the file": json.dumps({
                "schema_version": 1,
                "entries": [entry(5).to_dict(), {"mode": "ranked", "score": -1}]}).encode(),
            "string schema version": b'{"schema_version": "1", "entries": []}',
        }
        for label, data in cases.items():
            with self.subTest(label):
                for old in self.dir.glob("scores*") if self.dir.exists() else []:
                    old.unlink()
                self.write_bytes(data)
                store = ScoreStore(self.path)
                self.assertEqual(store.entries, [])
                self.assertEqual([p.read_bytes() for p in self.copies()], [data])

    def test_unreadable_file_that_is_a_directory_surfaces_oserror_for_the_runner(self):
        self.path.mkdir(parents=True)
        with self.assertRaises(OSError):
            ScoreStore(self.path)

    def test_empty_directory_and_missing_file_start_cleanly(self):
        store = ScoreStore(self.dir / "does" / "not" / "exist" / "scores.json")
        self.assertEqual(store.entries, [])
        self.assertIsNone(store.notice)
        store.add(entry(1))                       # creates the directory tree
        self.assertEqual(len(ScoreStore(store.path).entries), 1)


@unittest.skipUnless(os.name == "posix", "pseudo-terminal needed")
class GameStartsWithDamagedFileTests(TempCase):
    """Run the real program in a pseudo-terminal against a damaged file."""

    def play(self, home, name):
        import pty
        env = dict(os.environ, MATH_BLITZ_HOME=str(home))
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(str(ROOT))
            os.execve(sys.executable, [sys.executable, "math_game.py", "--plain"], env)
        out, answered, named = "", 0, False
        try:
            while True:
                if not select.select([fd], [], [], 10)[0]:
                    self.fail("game stalled:\n" + out)
                try:
                    data = os.read(fd, 4096)
                except OSError:
                    break
                if not data:
                    break
                out += data.decode(errors="replace")
                match = re.search(r"What is (\d+) ([-+*/]) (\d+)\? $", out)
                if match and answered < 20:
                    a, op, b = match.groups()
                    result = eval(a + ("//" if op == "/" else op) + b)
                    os.write(fd, "{}\r".format(result).encode())
                    answered += 1
                    out += "\n"
                elif "Enter your name:" in out and not named:
                    os.write(fd, (name + "\r").encode())
                    named = True
                    out += "\n"
        finally:
            os.close(fd)
            os.waitpid(pid, 0)
        return out

    def test_full_game_runs_over_a_truncated_file_and_keeps_a_copy(self):
        good = ScoreStore(self.path)
        good.add(entry(500, 4, name="Old"))
        damaged = self.path.read_bytes()[:40]
        self.write_bytes(damaged)
        output = self.play(self.dir, "Zoe")
        self.assertIn("kept as scores.corrupt-", output)
        self.assertIn("New high score", output)
        self.assertEqual([p.read_bytes() for p in self.copies()], [damaged])
        board = ScoreStore(self.path).board("ranked")
        self.assertEqual([e.name for e in board], ["Zoe"])
        self.assertEqual(board[0].best_streak, 20)

    def test_quit_run_still_starts_and_preserves_the_damaged_file(self):
        damaged = b"not json at all"
        self.write_bytes(damaged)
        result = subprocess.run(
            [sys.executable, "math_game.py"], input="q\n", universal_newlines=True,
            cwd=str(ROOT), env=dict(os.environ, MATH_BLITZ_HOME=str(self.dir)),
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=30)
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn("Final score", result.stdout)
        self.assertNotIn("Traceback", result.stdout)
        self.assertEqual([p.read_bytes() for p in self.copies()], [damaged])


class OlderFormatTests(TempCase):
    V1 = {"schema_version": 1, "entries": [
        {"mode": "ranked", "rules_version": 1, "name": "Ann", "score": 400,
         "best_streak": 7, "date": "2026-09-30T10:00:00Z", "difficulty": None},
        {"mode": "ranked", "rules_version": 1, "name": "Bob", "score": 300,
         "best_streak": 3, "date": "2026-09-29T10:00:00Z", "difficulty": None}]}

    def test_old_file_loads_without_being_modified_and_migrates_on_save(self):
        raw = json.dumps(self.V1).encode()
        self.write_bytes(raw)
        migrations = {1: lambda d: dict(d, schema_version=2)}
        with mock.patch.object(scores, "SCHEMA_VERSION", 2), \
                mock.patch.object(scores, "MIGRATIONS", migrations):
            store = ScoreStore(self.path)
            self.assertEqual([e.name for e in store.board("ranked")], ["Ann", "Bob"])
            self.assertEqual(self.path.read_bytes(), raw)           # load is read-only
            self.assertEqual(self.copies(), [])                      # and not "corrupt"
            store.add(entry(350, name="Cy"))
            self.assertEqual((self.dir / "scores.json.v1.bak").read_bytes(), raw)
            saved = json.loads(self.path.read_text())
            self.assertEqual(saved["schema_version"], 2)
            self.assertEqual([e["name"] for e in saved["entries"]], ["Ann", "Cy", "Bob"])
            store.add(entry(10, name="Di"))                          # second save
            self.assertEqual((self.dir / "scores.json.v1.bak").read_bytes(), raw)

    def test_chained_migrations_run_in_order(self):
        self.write_bytes(json.dumps(self.V1).encode())
        seen = []

        def step(n):
            def run(doc):
                seen.append(n)
                return dict(doc, schema_version=n + 1)
            return run
        with mock.patch.object(scores, "SCHEMA_VERSION", 4), \
                mock.patch.object(scores, "MIGRATIONS", {1: step(1), 2: step(2), 3: step(3)}):
            store = ScoreStore(self.path)
            store.add(entry(1))
            self.assertEqual(seen[:3], [1, 2, 3])
            self.assertTrue((self.dir / "scores.json.v1.bak").exists())
            self.assertEqual(json.loads(self.path.read_text())["schema_version"], 4)

    def test_missing_migration_is_a_bug_not_corruption(self):
        raw = json.dumps(self.V1).encode()
        self.write_bytes(raw)
        with mock.patch.object(scores, "SCHEMA_VERSION", 2), \
                mock.patch.object(scores, "MIGRATIONS", {}):
            with self.assertRaises(RuntimeError):
                ScoreStore(self.path)
        self.assertEqual(self.path.read_bytes(), raw)
        self.assertEqual(self.copies(), [])

    def test_migration_that_produces_bad_data_keeps_the_original(self):
        raw = json.dumps(self.V1).encode()
        self.write_bytes(raw)
        with mock.patch.object(scores, "SCHEMA_VERSION", 2), \
                mock.patch.object(scores, "MIGRATIONS",
                                  {1: lambda d: dict(d, schema_version=2, entries="oops")}):
            store = ScoreStore(self.path)
        self.assertEqual(store.entries, [])
        self.assertEqual([p.read_bytes() for p in self.copies()], [raw])

    def test_future_file_is_untouched_by_add_and_save(self):
        doc = dict(self.V1, schema_version=7, brand_new_field=True)
        raw = json.dumps(doc).encode()
        self.write_bytes(raw)
        store = ScoreStore(self.path)
        self.assertTrue(store.read_only)
        self.assertEqual(len(store.board("ranked")), 2)
        for action in (lambda: store.add(entry(9)), store.save):
            with self.assertRaises(scores.ScoresReadOnly):
                action()
        self.assertEqual(self.path.read_bytes(), raw)
        self.assertEqual({p.name for p in self.dir.iterdir()},
                         {"scores.json", "scores.json.lock"})


class TieTests(TempCase):
    def test_tie_order_is_score_then_streak_then_earlier_date_then_insertion(self):
        store = ScoreStore(self.path)
        store.add(entry(100, 1, "2026-10-01T00:00:03Z", name="d"))
        store.add(entry(100, 1, "2026-10-01T00:00:01Z", name="b"))
        store.add(entry(100, 2, "2026-10-01T00:00:09Z", name="a"))
        store.add(entry(100, 1, "2026-10-01T00:00:01Z", name="c"))  # exact tie with b
        store.add(entry(200, 0, "2026-10-01T00:00:09Z", name="top"))
        self.assertEqual([e.name for e in store.board("ranked")],
                         ["top", "a", "b", "c", "d"])

    def test_rank_returned_matches_position_for_tied_scores(self):
        store = ScoreStore(self.path)
        ranks = [store.add(entry(100, 3, "2026-10-01T00:00:0%dZ" % i)) for i in range(4)]
        self.assertEqual(ranks, [1, 2, 3, 4])      # later equal results rank lower

    def test_tie_on_streak_does_not_displace_earlier_equal_result(self):
        store = ScoreStore(self.path)
        store.add(entry(100, 3, "2026-10-01T00:00:00Z", name="first"))
        store.add(entry(100, 3, "2026-10-02T00:00:00Z", name="second"))
        self.assertEqual([e.name for e in store.board("ranked")], ["first", "second"])

    def test_equal_everything_survives_a_save_and_reload_in_order(self):
        store = ScoreStore(self.path)
        for name in "abc":
            store.add(entry(50, 1, name=name))
        self.assertEqual([e.name for e in ScoreStore(self.path).board("ranked")],
                         ["a", "b", "c"])


class CutoffTests(TempCase):
    def full_board(self, base=100, step=10):
        store = ScoreStore(self.path)
        for i in range(10):
            store.add(entry(base + i * step, name="p%d" % i))
        return store

    def test_nine_entries_accept_any_positive_score(self):
        store = ScoreStore(self.path)
        for i in range(9):
            store.add(entry(1000 + i))
        self.assertEqual(store.projected_rank("ranked", 1, 0), 10)
        self.assertEqual(store.add(entry(1)), 10)
        self.assertEqual(len(store.board("ranked")), 10)

    def test_exactly_ten_are_kept_and_the_lowest_is_dropped(self):
        store = self.full_board()
        self.assertEqual(len(store.board("ranked")), 10)
        self.assertEqual(store.add(entry(150, name="in")), 6)       # ties rank below p5
        names = [e.name for e in ScoreStore(self.path).board("ranked")]
        self.assertEqual(len(names), 10)
        self.assertIn("in", names)
        self.assertNotIn("p0", names)

    def test_boundary_scores(self):
        store = self.full_board()                  # scores 100..190, lowest is 100
        self.assertIsNone(store.projected_rank("ranked", 99, 0))
        self.assertIsNone(store.projected_rank("ranked", 100, 0))   # tie: incumbent stays
        self.assertEqual(store.projected_rank("ranked", 100, 1), 10)  # better streak wins
        self.assertEqual(store.projected_rank("ranked", 101, 0), 10)
        self.assertEqual(store.projected_rank("ranked", 191, 0), 1)

    def test_qualifying_pushes_out_exactly_the_lowest(self):
        store = self.full_board()
        self.assertEqual(store.add(entry(105, name="newcomer")), 10)
        board = store.board("ranked")
        self.assertEqual(len(board), 10)
        self.assertIn("newcomer", [e.name for e in board])
        self.assertNotIn("p0", [e.name for e in board])             # the old 100
        self.assertEqual(min(e.score for e in board), 105)

    def test_non_qualifying_result_changes_nothing(self):
        store = self.full_board()
        before = self.path.read_bytes()
        self.assertIsNone(store.add(entry(5, name="low")))
        board = ScoreStore(self.path).board("ranked")
        self.assertEqual(len(board), 10)
        self.assertNotIn("low", [e.name for e in board])
        self.assertEqual(len(ScoreStore(self.path).entries), 10)
        self.assertEqual(json.loads(before)["entries"],
                         json.loads(self.path.read_text())["entries"])

    def test_file_never_holds_more_than_ten_per_board(self):
        store = ScoreStore(self.path)
        for i in range(50):
            store.add(entry(i + 1, name="r%d" % i))
            store.add(entry(i + 1, mode="adaptive", difficulty=i % 33, name="a%d" % i))
        data = json.loads(self.path.read_text())
        by_mode = {}
        for item in data["entries"]:
            by_mode.setdefault(item["mode"], []).append(item["score"])
        self.assertEqual({m: len(v) for m, v in by_mode.items()},
                         {"ranked": 10, "adaptive": 10})
        self.assertEqual(sorted(by_mode["ranked"]), list(range(41, 51)))

    def test_one_board_filling_up_does_not_evict_another_board(self):
        store = ScoreStore(self.path)
        store.add(entry(1, mode="adaptive", difficulty=2))
        for i in range(30):
            store.add(entry(100 + i))
        self.assertEqual(len(store.board("adaptive")), 1)
        self.assertEqual(len(store.board("ranked")), 10)

    def test_hand_edited_file_with_too_many_entries_is_trimmed_on_next_add(self):
        doc = {"schema_version": 1, "entries": [
            entry(i + 1, name="e%d" % i).to_dict() for i in range(15)]}
        self.write_bytes(json.dumps(doc).encode())
        store = ScoreStore(self.path)
        self.assertEqual(len(store.board("ranked")), 10)            # display is capped
        self.assertIsNone(store.projected_rank("ranked", 6, 0))     # ties the 10th
        self.assertEqual(store.projected_rank("ranked", 7, 0), 10)
        store.add(entry(100, name="big"))
        saved = json.loads(self.path.read_text())["entries"]
        self.assertEqual(len(saved), 10)
        self.assertEqual(saved[0]["name"], "big")


WRITER = """
import os, sys, time
from math_blitz.scores import ScoreEntry, ScoreStore
path, worker, count, go, same_board = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4], sys.argv[5] == "1"
while not os.path.exists(go):
    time.sleep(0.0005)
for i in range(count):
    # Separate boards keep every entry visible (nothing is trimmed away).
    mode = "ranked" if same_board else "w{}-{}".format(worker, i)
    ScoreStore(path).add(ScoreEntry(mode, "w%d" % worker, worker * 1000 + i + 1, i,
                                    "2026-10-01T00:00:00Z"))
"""


class ConcurrentWriterTests(TempCase):
    """Separate processes, like two terminals each finishing games at once."""
    WORKERS, PER_WORKER = 4, 12

    def run_writers(self, same_board):
        self.dir.mkdir(parents=True, exist_ok=True)
        go = self.dir.parent / "go"
        env = dict(os.environ, PYTHONPATH=str(ROOT))
        procs = [subprocess.Popen(
            [sys.executable, "-c", WRITER, str(self.path), str(w), str(self.PER_WORKER),
             str(go), "1" if same_board else "0"],
            env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            universal_newlines=True) for w in range(self.WORKERS)]
        go.write_text("go")
        for proc in procs:
            out, _ = proc.communicate(timeout=120)
            self.assertEqual(proc.returncode, 0, out)

    def test_no_score_is_lost_when_writers_run_in_parallel(self):
        self.run_writers(same_board=False)
        saved = ScoreStore(self.path).entries
        self.assertEqual(len(saved), self.WORKERS * self.PER_WORKER,
                         "lost updates: only {} of {} scores survived".format(
                             len(saved), self.WORKERS * self.PER_WORKER))

    def test_shared_board_top_ten_is_exact_under_contention(self):
        self.run_writers(same_board=True)
        every = sorted((w * 1000 + i + 1 for w in range(self.WORKERS)
                        for i in range(self.PER_WORKER)), reverse=True)
        got = [e.score for e in ScoreStore(self.path).board("ranked")]
        self.assertEqual(got, every[:10])

    def test_concurrent_loads_of_a_corrupt_file_never_discard_a_good_one(self):
        # One process quarantines the damaged file and writes a good one; a second,
        # having read the damaged bytes earlier, must not move the good file aside.
        self.write_bytes(b"{truncated")
        self.run_writers(same_board=False)
        self.assertEqual(len(ScoreStore(self.path).entries),
                         self.WORKERS * self.PER_WORKER)
        self.assertEqual([p.read_bytes() for p in self.copies()], [b"{truncated"])


HOLDER = """
import sys, time
from pathlib import Path
from math_blitz.scores import file_lock
with file_lock(Path(sys.argv[1])):
    print("locked", flush=True)
    time.sleep(60)
"""


class QuarantineRaceTests(TempCase):
    def test_a_game_saving_during_quarantine_is_not_itself_quarantined(self):
        """Forced interleaving: game B has read a damaged file and is about to move
        it aside when game A finishes a run and saves. Unlocked, B would then move
        A's brand-new good file aside too, silently losing A's score."""
        import threading
        damaged = b"{truncated"
        self.write_bytes(damaged)
        real = ScoreStore._quarantine
        result = {}

        def racing_quarantine(store):
            def other_game():
                result["rank"] = ScoreStore(self.path).add(entry(777, name="A"))
            thread = threading.Thread(target=other_game)
            thread.start()
            thread.join(0.3)          # locked: A must still be waiting for B
            result["thread"] = thread
            real(store)

        with mock.patch.object(ScoreStore, "_quarantine", racing_quarantine):
            ScoreStore(self.path)     # game B starts and finds the damaged file
        result["thread"].join(30)
        self.assertEqual(result.get("rank"), 1)
        self.assertEqual([e.name for e in ScoreStore(self.path).board("ranked")], ["A"])
        self.assertEqual([p.read_bytes() for p in self.copies()], [damaged])


class LockTests(TempCase):
    def test_lock_excludes_a_second_holder_and_releases_on_exit(self):
        lock = self.dir / "x.lock"
        with scores.file_lock(lock):
            with self.assertRaises(OSError):
                with scores.file_lock(lock, timeout=0.05):
                    self.fail("second holder got the lock")
        with scores.file_lock(lock, timeout=0.05):   # free again
            pass

    def test_lock_is_released_when_the_holder_raises(self):
        lock = self.dir / "x.lock"
        with self.assertRaises(ZeroDivisionError):
            with scores.file_lock(lock):
                1 / 0
        with scores.file_lock(lock, timeout=0.05):
            pass

    def test_lock_held_by_a_killed_process_does_not_go_stale(self):
        self.dir.mkdir(parents=True)
        lock = self.dir / "x.lock"
        proc = subprocess.Popen([sys.executable, "-c", HOLDER, str(lock)],
                                env=dict(os.environ, PYTHONPATH=str(ROOT)),
                                stdout=subprocess.PIPE, universal_newlines=True)
        try:
            self.assertEqual(proc.stdout.readline().strip(), "locked")
            with self.assertRaises(OSError):
                with scores.file_lock(lock, timeout=0.05):
                    pass
        finally:
            proc.kill()
            proc.wait()
            proc.stdout.close()
        with scores.file_lock(lock, timeout=2):      # crash released it
            pass

    def test_save_fails_cleanly_instead_of_hanging_when_lock_is_stuck(self):
        store = ScoreStore(self.path)
        store.add(entry(5))
        before = self.path.read_bytes()
        real = scores.file_lock
        with scores.file_lock(store.lock_path):
            with mock.patch.object(scores, "file_lock",
                                   lambda p: real(p, timeout=0.05)):
                with self.assertRaises(OSError):
                    store.add(entry(9))
        self.assertEqual(self.path.read_bytes(), before)
        self.assertEqual(len(ScoreStore(self.path).entries), 1)


if __name__ == "__main__":
    unittest.main()
