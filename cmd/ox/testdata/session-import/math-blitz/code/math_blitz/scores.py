"""Persistent high-score table; see docs/decisions/0003-high-scores.md.

Plain data in, plain data out. The engine never imports this module. The file
is a versioned JSON document replaced atomically on every save.
"""
import json
import os
import shutil
import sys
import time
from contextlib import contextmanager
from dataclasses import dataclass, field
from pathlib import Path, PurePosixPath, PureWindowsPath
from typing import Any, Callable, Dict, List, Mapping, Optional

SCHEMA_VERSION = 1   # file layout; bump with a MIGRATIONS entry
RULES_VERSION = 1    # scoring rules; scores under different rules never mix
TOP_N = 10
NAME_LIMIT = 20
DEFAULT_NAME = "Anonymous"
APP_DIR = "math-blitz"
FILE_NAME = "scores.json"
LOCK_TIMEOUT = 15.0  # seconds to wait for another game's save before giving up

# MIGRATIONS[n] converts a version-n document into a version n+1 document.
MIGRATIONS: Dict[int, Callable[[Dict[str, Any]], Dict[str, Any]]] = {}


class ScoresReadOnly(Exception):
    """The file was written by a newer game version and must not be replaced."""


@dataclass(frozen=True)
class ScoreEntry:
    mode: str
    name: str
    score: int
    best_streak: int
    date: str  # UTC ISO 8601, e.g. 2026-10-01T12:00:00Z
    difficulty: Optional[int] = None  # highest family level; adaptive boards only
    rules_version: int = RULES_VERSION
    extra: Dict[str, Any] = field(default_factory=dict, compare=False)

    def to_dict(self) -> Dict[str, Any]:
        data = dict(self.extra)
        data.update(mode=self.mode, rules_version=self.rules_version, name=self.name,
                    score=self.score, best_streak=self.best_streak, date=self.date,
                    difficulty=self.difficulty)
        return data

    @classmethod
    def from_dict(cls, data: Any) -> "ScoreEntry":
        if not isinstance(data, dict):
            raise ValueError("entry must be an object")
        known = ("mode", "rules_version", "name", "score", "best_streak", "date",
                 "difficulty")

        def integer(key, minimum, optional=False):
            value = data.get(key)
            if optional and value is None:
                return None
            if type(value) is not int or value < minimum:
                raise ValueError("bad " + key)
            return value

        for key in ("mode", "name", "date"):
            if not isinstance(data.get(key), str):
                raise ValueError("bad " + key)
        if not data["mode"]:
            raise ValueError("bad mode")
        return cls(data["mode"], data["name"], integer("score", 0),
                   integer("best_streak", 0), data["date"],
                   integer("difficulty", 0, optional=True),
                   integer("rules_version", 1),
                   {k: v for k, v in data.items() if k not in known})

    def sort_key(self):
        # Higher score, then longer streak, then the earlier achievement.
        return (-self.score, -self.best_streak, self.date)


def sanitize_name(raw: str) -> str:
    """Drop control characters (including terminal escapes); bound the length."""
    name = "".join(c for c in raw if c.isprintable()).strip()[:NAME_LIMIT].strip()
    return name or DEFAULT_NAME


def default_path(environ: Optional[Mapping[str, str]] = None,
                 platform: Optional[str] = None, home: Optional[str] = None) -> str:
    """Per-user data location by platform convention, standard library only."""
    environ = os.environ if environ is None else environ
    platform = sys.platform if platform is None else platform
    home = os.path.expanduser("~") if home is None else home
    if environ.get("MATH_BLITZ_HOME"):
        return str(Path(environ["MATH_BLITZ_HOME"]) / FILE_NAME)
    if platform == "win32":
        base = (environ.get("LOCALAPPDATA") or environ.get("APPDATA")
                or str(PureWindowsPath(home) / "AppData" / "Local"))
        return str(PureWindowsPath(base) / APP_DIR / FILE_NAME)
    if platform == "darwin":
        return str(PurePosixPath(home) / "Library" / "Application Support"
                   / APP_DIR / FILE_NAME)
    xdg = environ.get("XDG_DATA_HOME", "")
    base = xdg if xdg.startswith("/") else str(PurePosixPath(home) / ".local" / "share")
    return str(PurePosixPath(base) / APP_DIR / FILE_NAME)


def _fsync(fd: int) -> None:
    if sys.platform == "darwin":
        try:  # plain fsync does not flush the drive cache on macOS
            import fcntl
            fcntl.fcntl(fd, fcntl.F_FULLFSYNC)
            return
        except (ImportError, AttributeError, OSError):
            pass
    os.fsync(fd)


def atomic_write(path: Path, payload: bytes) -> None:
    """Replace path with payload so readers see the old or new file, never a mix."""
    directory = path.parent
    directory.mkdir(parents=True, exist_ok=True)
    tmp = directory / "{}.{}.tmp".format(path.name, os.getpid())
    try:
        with open(str(tmp), "wb") as handle:
            handle.write(payload)
            handle.flush()
            _fsync(handle.fileno())
        for attempt in range(5):
            try:
                os.replace(str(tmp), str(path))
                break
            except PermissionError:
                # Windows: antivirus or an indexer may briefly hold the target.
                if os.name != "nt" or attempt == 4:
                    raise
                time.sleep(0.05)
    except BaseException:
        try:
            tmp.unlink()
        except OSError:
            pass
        raise
    if os.name != "nt":
        try:
            fd = os.open(str(directory), os.O_RDONLY)
            try:
                _fsync(fd)
            finally:
                os.close(fd)
        except OSError:
            pass  # directory durability is best effort; the file is consistent


def _try_lock(fd: int) -> bool:
    """One non-blocking attempt at an exclusive lock on byte 0 of fd."""
    if os.name == "nt":
        import msvcrt
        try:
            os.lseek(fd, 0, os.SEEK_SET)
            msvcrt.locking(fd, msvcrt.LK_NBLCK, 1)
            return True
        except OSError:
            return False
    import fcntl
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        return True
    except OSError:  # EWOULDBLOCK / EAGAIN: someone else holds it
        return False


def _unlock(fd: int) -> None:
    if os.name == "nt":
        import msvcrt
        os.lseek(fd, 0, os.SEEK_SET)
        msvcrt.locking(fd, msvcrt.LK_UNLCK, 1)
    else:
        import fcntl
        fcntl.flock(fd, fcntl.LOCK_UN)


@contextmanager
def file_lock(lock_path: Path, timeout: float = LOCK_TIMEOUT):
    """Cross-process mutual exclusion via an OS advisory lock on a side file.

    The lock lives on a separate file, never on scores.json: that file is
    replaced on every save, and a lock held on a replaced inode excludes no one.
    The OS drops the lock if the holder crashes, so it cannot go stale. Polling
    with a deadline means a stuck holder costs a failed save, never a hung game.
    """
    lock_path.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(str(lock_path), os.O_RDWR | os.O_CREAT, 0o600)
    try:
        deadline = time.monotonic() + timeout
        while not _try_lock(fd):
            if time.monotonic() >= deadline:
                raise OSError("timed out waiting for another Math Blitz to finish saving")
            time.sleep(0.005)
        try:
            yield
        finally:
            _unlock(fd)
    finally:
        os.close(fd)


class ScoreStore:
    def __init__(self, path):
        self.path = Path(path)
        self.lock_path = self.path.with_name(self.path.name + ".lock")
        self.entries: List[ScoreEntry] = []
        self.read_only = False
        self.notice: Optional[str] = None
        self._extra: Dict[str, Any] = {}
        self._migrated_from: Optional[int] = None
        self.load()

    def load(self) -> None:
        """Read the file. Locked, because a damaged file is moved aside here and
        that must never race with another game's save of a freshly written one."""
        self._reset()
        if not self.path.parent.exists():
            return
        with file_lock(self.lock_path):
            self._load_locked()

    def _reset(self) -> None:
        self.entries, self.read_only, self._extra = [], False, {}
        self._migrated_from = None

    def _load_locked(self) -> None:
        self._reset()
        try:
            raw = self.path.read_bytes()
        except FileNotFoundError:
            return
        try:
            data = json.loads(raw.decode("utf-8"))
            if not isinstance(data, dict):
                raise ValueError("document must be an object")
            version = data.get("schema_version")
            if type(version) is not int or version < 1:
                raise ValueError("bad schema_version")
            if version > SCHEMA_VERSION:
                self._load_newer(data, version)
                return
            original = version
            while version < SCHEMA_VERSION:
                if version not in MIGRATIONS:
                    raise RuntimeError("no migration from schema {}".format(version))
                data = MIGRATIONS[version](data)
                version += 1
            if not isinstance(data.get("entries"), list):
                raise ValueError("entries must be a list")
            entries = [ScoreEntry.from_dict(item) for item in data["entries"]]
        except ValueError:  # includes JSON and Unicode decode errors
            self._quarantine()
            return
        self.entries = entries
        self._extra = {k: v for k, v in data.items()
                       if k not in ("schema_version", "entries")}
        if original < SCHEMA_VERSION:
            self._migrated_from = original

    def _load_newer(self, data: Dict[str, Any], version: int) -> None:
        self.read_only = True
        self.notice = ("High scores were saved by a newer Math Blitz (format {}); "
                       "showing them read-only.".format(version))
        for item in data.get("entries", []) if isinstance(data.get("entries"), list) else []:
            try:
                self.entries.append(ScoreEntry.from_dict(item))
            except ValueError:
                pass

    def _quarantine(self) -> None:
        stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
        target = self.path.with_name("scores.corrupt-{}.json".format(stamp))
        n = 0
        while target.exists():
            n += 1
            target = self.path.with_name("scores.corrupt-{}-{}.json".format(stamp, n))
        os.replace(str(self.path), str(target))
        self.notice = "High-score file was unreadable; kept as {}.".format(target.name)

    def board(self, mode: str, rules_version: int = RULES_VERSION) -> List[ScoreEntry]:
        return sorted((e for e in self.entries
                       if e.mode == mode and e.rules_version == rules_version),
                      key=ScoreEntry.sort_key)[:TOP_N]

    def projected_rank(self, mode: str, score: int, best_streak: int,
                       rules_version: int = RULES_VERSION) -> Optional[int]:
        """1-based rank a new result would take, or None if it misses the table.

        Ties rank below existing entries, since the new result is later.
        """
        if score <= 0:
            return None
        ahead = sum(1 for e in self.board(mode, rules_version)
                    if (e.score, e.best_streak) >= (score, best_streak))
        return ahead + 1 if ahead < TOP_N else None

    def add(self, entry: ScoreEntry) -> Optional[int]:
        """Insert, trim the board to TOP_N, save; return the 1-based rank or None.

        Safe against other games running at the same time: the reload, the
        merge, and the replace all happen under one cross-process lock.
        """
        self.path.parent.mkdir(parents=True, exist_ok=True)
        # The whole read-modify-write holds the lock. Atomic replacement keeps
        # readers from seeing a torn file; only this lock stops two writers from
        # both starting from the same old contents and one erasing the other.
        with file_lock(self.lock_path):
            self._load_locked()
            return self._insert_locked(entry)

    def _insert_locked(self, entry: ScoreEntry) -> Optional[int]:
        if self.read_only:
            raise ScoresReadOnly(self.notice)
        others = [e for e in self.entries
                  if (e.mode, e.rules_version) != (entry.mode, entry.rules_version)]
        board = sorted([e for e in self.entries
                        if (e.mode, e.rules_version) == (entry.mode, entry.rules_version)]
                       + [entry], key=ScoreEntry.sort_key)
        kept = board[:TOP_N]
        self.entries = others + kept
        self._write_locked()
        return next((i for i, e in enumerate(kept, 1) if e is entry), None)

    def save(self) -> None:
        """Write the in-memory table as is. Prefer add(), which re-reads first."""
        self.path.parent.mkdir(parents=True, exist_ok=True)
        with file_lock(self.lock_path):
            self._write_locked()

    def _write_locked(self) -> None:
        if self.read_only:
            raise ScoresReadOnly(self.notice)
        data = dict(self._extra)
        data["schema_version"] = SCHEMA_VERSION
        data["entries"] = [e.to_dict() for e in self.entries]
        if self._migrated_from is not None and self.path.exists():
            backup = self.path.with_name(
                "{}.v{}.bak".format(self.path.name, self._migrated_from))
            if not backup.exists():
                shutil.copy2(str(self.path), str(backup))
        atomic_write(self.path, json.dumps(data, indent=2).encode("utf-8"))
        self._migrated_from = None


def format_board(entries: List[ScoreEntry], highlight: Optional[int] = None) -> str:
    """Render a board; highlight is a 1-based rank to mark."""
    show_level = any(e.difficulty is not None for e in entries)
    header = "{:>2}  {:<{w}}  {:>6}  {:>6}  {:<10}".format(
        "#", "Name", "Score", "Streak", "Date", w=NAME_LIMIT)
    if show_level:
        header += "  Level"
    lines = [header]
    for rank, e in enumerate(entries, 1):
        line = "{:>2}  {:<{w}}  {:>6}  {:>6}  {:<10}".format(
            rank, e.name, e.score, e.best_streak, e.date[:10], w=NAME_LIMIT)
        if show_level:
            line += "  {:>5}".format("-" if e.difficulty is None else e.difficulty)
        if rank == highlight:
            line += "  <-- you"
        lines.append(line)
    return "\n".join(lines)
