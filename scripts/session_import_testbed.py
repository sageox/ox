#!/usr/bin/env python3
"""session_import_testbed.py - real Claude Code and Codex sessions for testing ox session import.

Usage:
  scripts/session_import_testbed.py capture <source-repo> --out <fixture-dir> --identity <text> ...
  scripts/session_import_testbed.py create <target-dir> [--fixture <fixture-dir>]

capture copies the sessions a coworker ran in <source-repo> out of this machine's
stores (~/.claude, ~/.codex, or CLAUDE_CONFIG_DIR / CODEX_HOME) into a fixture that
is safe to commit: only the fields ox reads are kept, vendor-internal data and
SageOx context blocks are dropped, tool payloads are capped, paths are templated,
and every --identity string (plus $USER, the host name and the git identity) is
replaced with a test persona. It refuses to write anything that still names you,
holds an email that is not a known placeholder, or would exceed the repo's file
size limit. Review the result before committing it.

create builds a fresh repo from a fixture: the fixture's code in one commit, and its
sessions in <target>/.git/ox-test-data, laid out like ~/.claude and ~/.codex, pointed
at the new repo and dated yesterday. Then, inside the repo:

  ox init --endpoint <url>
  ox session import --from-test-data .git/ox-test-data --dry-run

Standard library only; runs on Python 3.9.
"""

import argparse
import datetime
import json
import os
import re
import shutil
import subprocess
import sys
import time
from pathlib import Path

DEFAULT_FIXTURE = Path(__file__).resolve().parent.parent / "cmd" / "ox" / "testdata" / "session-import" / "math-blitz"

PAYLOAD_CAP = 4096            # bytes of any one tool input or output kept
FILE_CAP = 450 * 1024         # the repo rejects files over 500 KB
QUIET = 30 * 60               # a session touched more recently may still be running
REPO, HOME = "__REPO__", "__HOME__"

PERSONA_EMAIL = "devon@example.com"
PERSONA_NAME = "Devon"
PERSONA_USER = "devon"
ALLOWED_EMAIL = re.compile(r"@(example\.(com|org|net)|anthropic\.com|openai\.com|users\.noreply\.github\.com)$|^ox@sageox\.ai$|^noreply@", re.I)
EMAIL = re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}")
OX_PRIME = re.compile(r"<ox-prime\b[^>]*>.*?(?:</ox-prime>|\Z)", re.S)
PRIME_PLACEHOLDER = "<ox-prime>[removed from fixture]</ox-prime>"
PRIME_LEFT = re.compile(r"<ox-prime\b[^>]*>(?!\[removed from fixture\])")
UUID = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")

# Claude Code records and fields ox reads, plus the structural ones that keep a
# transcript shaped like the real thing. Everything else is the app's own
# bookkeeping or vendor-internal (prompt snapshots, classifier context, usage).
CLAUDE_TYPES = {"user", "assistant", "system", "summary", "custom-title"}
CLAUDE_KEYS = {"type", "uuid", "parentUuid", "isSidechain", "isMeta", "isCompactSummary", "userType", "cwd",
               "sessionId", "version", "gitBranch", "timestamp", "entrypoint", "requestId", "message",
               "subtype", "content", "summary", "customTitle", "leafUuid", "level"}
CLAUDE_MESSAGE_KEYS = {"role", "content", "id", "model", "type", "stop_reason"}
# Codex records ox reads: the session header, turn boundaries, messages and tool calls.
CODEX_EVENTS = {"task_started", "task_complete", "task_completed", "turn_aborted", "user_message", "agent_message"}
CODEX_TOOL_ITEMS = {"function_call", "custom_tool_call", "function_call_output", "custom_tool_call_output"}
CODEX_TURN_KEYS = {"cwd", "model", "approval_policy", "sandbox_policy"}
# Files an ox-initialized repo gains from ox itself; the fixture is the code before ox.
OX_MANAGED = (".sageox/", ".claude/", ".codex/", ".agents/", ".factory/", ".opencode/")
OX_MANAGED_FILES = {"AGENTS.md", "CLAUDE.md", ".gitattributes"}
JUNK = re.compile(r"(^|/)(__pycache__/|\.DS_Store$|.*\.pyc$)")


class Refused(Exception):
    pass


def cap(value):
    if isinstance(value, str):
        data = value.encode()
        if len(data) <= PAYLOAD_CAP:
            return value
        return data[:PAYLOAD_CAP].decode(errors="ignore") + "\n[… %d bytes trimmed from fixture]" % (len(data) - PAYLOAD_CAP)
    if isinstance(value, list):
        return [cap(v) for v in value]
    if isinstance(value, dict):
        return {k: cap(v) for k, v in value.items()}
    return value


def clean_claude(record):
    if record.get("type") not in CLAUDE_TYPES:
        return None
    record = {k: v for k, v in record.items() if k in CLAUDE_KEYS}
    message = record.get("message")
    if isinstance(message, dict):
        message = {k: v for k, v in message.items() if k in CLAUDE_MESSAGE_KEYS}
        if isinstance(message.get("content"), list):
            parts = []
            for part in message["content"]:
                if isinstance(part, dict):
                    if part.get("type") in ("thinking", "redacted_thinking"):
                        continue
                    if part.get("type") == "tool_use":
                        part = dict(part, input=cap(part.get("input")))
                    elif part.get("type") == "tool_result":
                        part = dict(part, content=cap(part.get("content")))
                parts.append(part)
            message["content"] = parts
        record["message"] = message
    return record


def clean_codex(record):
    kind = record.get("type")
    payload = record.get("payload") if isinstance(record.get("payload"), dict) else {}
    item = payload.get("type")
    if kind == "session_meta":
        payload["base_instructions"] = {"text": "[removed from fixture]"}
        return record
    if kind == "turn_context":
        record["payload"] = {k: v for k, v in payload.items() if k in CODEX_TURN_KEYS}
        return record
    if kind == "response_item":
        if item == "message" and payload.get("role") in ("user", "assistant"):
            return record
        if item in CODEX_TOOL_ITEMS:
            for key in ("input", "arguments", "output"):
                if key in payload:
                    payload[key] = cap(payload[key])
            return record
        return None
    if kind == "event_msg" and item in CODEX_EVENTS:
        return record
    return None


def boundary(path):
    """Matches path only where it ends a path component."""
    return re.compile(re.escape(path) + r"(?=[/\"\\\s:'),]|$)")


def word(text):
    """Matches text as a whole word, ignoring case: a host named "Mac" must not touch "macOS".

    Serialized JSON writes a newline as backslash-n, so "\\ndreal" must still count as
    a word start; an escape sequence before the word is a boundary too."""
    start = r"(?:(?<=\\[nrt])|(?<![A-Za-z0-9_]))"
    return re.compile(start + re.escape(text) + r"(?![A-Za-z0-9_])", re.I)


def strip_prime(value):
    """Blanks every <ox-prime> block in a record's strings, before any capping can
    cut a block's closing tag off. A block whose closing tag never came is blanked
    to the end of its string."""
    if isinstance(value, str):
        return OX_PRIME.sub(PRIME_PLACEHOLDER, value)
    if isinstance(value, list):
        return [strip_prime(v) for v in value]
    if isinstance(value, dict):
        return {k: strip_prime(v) for k, v in value.items()}
    return value


def identity_map(source, extra):
    """Real identity strings, longest first, each with its persona."""
    found = {}

    def add(text, persona):
        text = (text or "").strip()
        if len(text) >= 3 and text not in found:
            found[text] = persona

    for text in extra:
        add(text, PERSONA_EMAIL if "@" in text else PERSONA_NAME)
    for key in ("user.email", "user.name"):
        out = subprocess.run(["git", "-C", str(source), "config", key], capture_output=True, text=True)
        add(out.stdout, PERSONA_EMAIL if key == "user.email" else PERSONA_NAME)
    # A host name is often a common word ("Mac"); it reaches transcripts inside
    # emails, which the email gate catches, so it is only replaced when passed.
    add(os.environ.get("USER", ""), PERSONA_USER)
    return sorted(found.items(), key=lambda kv: -len(kv[0]))


def scrub(line, replacements):
    for pattern, replacement in replacements:
        line = pattern.sub(replacement, line)
    return line


def in_repo(cwd, roots):
    return isinstance(cwd, str) and any(cwd == r or cwd.startswith(r + "/") for r in roots)


def claude_sessions(projects, roots):
    for path in sorted(projects.glob("*/*.jsonl")):
        if not UUID.match(path.stem):
            continue
        with open(path, encoding="utf-8") as f:
            for line in f:
                try:
                    if in_repo(json.loads(line).get("cwd"), roots):
                        yield path
                        break
                except (ValueError, AttributeError):
                    continue


def codex_sessions(home, roots):
    for sub in ("sessions", "archived_sessions"):
        for path in sorted((home / sub).rglob("rollout-*.jsonl")):
            with open(path, encoding="utf-8") as f:
                first = f.readline()
            try:
                meta = json.loads(first)
            except ValueError:
                continue
            if meta.get("type") == "session_meta" and in_repo((meta.get("payload") or {}).get("cwd"), roots):
                yield path


def native_roots():
    home = Path.home()
    claude = Path(os.environ["CLAUDE_CONFIG_DIR"]) / "projects" if os.environ.get("CLAUDE_CONFIG_DIR") else home / ".claude" / "projects"
    codex = Path(os.environ["CODEX_HOME"]) if os.environ.get("CODEX_HOME") else home / ".codex"
    return claude, codex


def gate(name, text, identities):
    """Refuses a cleaned session that still names its author or is too large."""
    for real, _ in identities:
        if word(real).search(text):
            raise Refused("%s still contains %r; pass it to --identity or fix the cleaning rules" % (name, real))
    # "\n@decorator" in transcribed code is an escaped newline, not an address.
    stray = sorted({m.group(0) for m in EMAIL.finditer(text)
                    if text[m.start() - 1:m.start()] != "\\" and not ALLOWED_EMAIL.search(m.group(0))})
    if stray:
        raise Refused("%s contains email addresses %s; pass them to --identity" % (name, ", ".join(stray)))
    if PRIME_LEFT.search(text):
        raise Refused("%s still holds SageOx context from an <ox-prime> block" % name)
    if REPO not in text:
        raise Refused("%s never mentions the repo; it would not be in scope after create" % name)
    if len(text.encode()) > FILE_CAP:
        raise Refused("%s is %d KB after cleaning, over the %d KB limit" % (name, len(text.encode()) // 1024, FILE_CAP // 1024))


def capture(args):
    source = Path(args.source).resolve()
    if subprocess.run(["git", "-C", str(source), "rev-parse"], capture_output=True).returncode != 0:
        raise Refused("%s is not a git repository" % source)
    out = Path(args.out)
    if out.exists() and any(out.iterdir()):
        raise Refused("%s is not empty" % out)
    home = str(Path.home())
    roots = {str(source), os.path.realpath(source)}
    identities = identity_map(source, args.identity)
    replacements = [(boundary(r), REPO) for r in sorted(roots, key=len, reverse=True)]
    replacements += [(boundary(h), HOME) for h in sorted({home, os.path.realpath(home)}, key=len, reverse=True)]
    replacements += [(word(real), persona) for real, persona in identities]

    claude_dir, codex_home = native_roots()
    found = [("claude", p, clean_claude) for p in claude_sessions(claude_dir, roots)]
    found += [("codex", p, clean_codex) for p in codex_sessions(codex_home, roots)]
    if not found:
        raise Refused("no Claude Code or Codex sessions ran in %s" % source)
    now = time.time()
    active = [str(p) for _, p, _ in found if now - p.stat().st_mtime < QUIET]
    if active and not args.allow_active:
        raise Refused("still active (changed in the last 30 minutes): %s; finish them or pass --allow-active" % ", ".join(active))

    cleaned = []
    for agent, path, clean in found:
        lines = []
        with open(path, encoding="utf-8") as f:
            for raw in f:
                if not raw.strip():
                    continue
                record = clean(strip_prime(json.loads(raw)))
                if record is not None:
                    lines.append(scrub(json.dumps(record, ensure_ascii=False, separators=(",", ":")), replacements))
        text = "\n".join(lines) + "\n"
        name = "%s/%s" % (agent, path.name)
        gate(name, text, identities)
        cleaned.append((agent, path.name, text, len(lines)))

    for agent, filename, text, _ in cleaned:
        dest = out / agent / filename
        dest.parent.mkdir(parents=True, exist_ok=True)
        dest.write_text(text, encoding="utf-8")
    copy_code(source, out / "code")
    write_provenance(out, source, cleaned)
    print("Captured %d sessions into %s. Review them and PROVENANCE.md before committing." % (len(cleaned), out))
    return 0


def copy_code(source, dest):
    listed = subprocess.run(["git", "-C", str(source), "ls-files", "-z", "--cached", "--others", "--exclude-standard"],
                            capture_output=True, check=True).stdout.decode().split("\0")
    for rel in sorted(filter(None, listed)):
        if rel.startswith(OX_MANAGED) or rel in OX_MANAGED_FILES or JUNK.search(rel):
            continue
        src = source / rel
        if not src.is_file():
            continue
        # A nested .gitignore or .gitattributes would apply to the fixture inside the ox repo.
        parts = rel.split("/")
        parts[-1] = "dot-" + parts[-1][1:] if parts[-1] in (".gitignore", ".gitattributes") else parts[-1]
        target = dest.joinpath(*parts)
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(src, target)


def write_provenance(out, source, cleaned):
    rows = "\n".join("| %s | `%s` | %d | %d KB |" % (a, n, c, len(t.encode()) // 1024) for a, n, t, c in cleaned)
    out.joinpath("PROVENANCE.md").write_text("""# Provenance

Captured %s from a scratch repo (`%s`) with `scripts/session_import_testbed.py capture`.
These are real Claude Code and Codex Desktop sessions, cleaned to be safe to publish:

- **Kept:** only the records and fields `ox session import` reads, plus structural fields
  (ids, parents, timestamps) that keep each file shaped like the real thing.
- **Dropped:** model reasoning, vendor system prompts and prompt snapshots, safety-classifier
  context, token usage, app UI events and attachments.
- **Blanked:** `<ox-prime>` blocks (SageOx context the session loaded).
- **Capped:** tool inputs and outputs over %d bytes.
- **Templated:** the repo path is `%s` and the home directory `%s`; `create` fills them in.
- **Replaced:** the author's name, emails, user name and host name with the persona
  "%s" (`%s`).

| Agent | File | Records | Size |
|---|---|---|---|
%s
""" % (datetime.date.today().isoformat(), source.name, PAYLOAD_CAP, REPO, HOME, PERSONA_NAME, PERSONA_EMAIL, rows), encoding="utf-8")


def slug(path):
    return re.sub(r"[^A-Za-z0-9]", "-", path)


def create(args):
    fixture = Path(args.fixture).resolve()
    target = Path(args.target).expanduser()
    if target.exists():
        raise Refused("%s already exists" % target)
    if not (fixture / "code").is_dir():
        raise Refused("%s is not a fixture (no code/ directory)" % fixture)
    if re.search(r"[\s\"'\\]", str(target.resolve().parent / target.name)):
        raise Refused("pick a target path without spaces, quotes or backslashes")
    target.mkdir(parents=True)
    repo = str(target.resolve())
    for src in sorted((fixture / "code").rglob("*")):
        if src.is_file():
            parts = list(src.relative_to(fixture / "code").parts)
            if parts[-1] in ("dot-gitignore", "dot-gitattributes"):
                parts[-1] = "." + parts[-1][4:]
            dest = Path(repo).joinpath(*parts)
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(src, dest)
    git = ["git", "-C", repo, "-c", "user.name=ox testbed", "-c", "user.email=testbed@example.com",
           "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"]
    subprocess.run(git + ["init", "-q", "-b", "main"], check=True)
    subprocess.run(git + ["add", "-A"], check=True)
    subprocess.run(git + ["commit", "-q", "-m", "Math Blitz (test repo from %s)" % fixture.name], check=True)

    data = Path(repo) / ".git" / "ox-test-data"
    home = os.path.realpath(Path.home())
    yesterday = time.time() - 24 * 3600
    seeded = 0
    for agent in ("claude", "codex"):
        for src in sorted((fixture / agent).glob("*.jsonl")) if (fixture / agent).is_dir() else []:
            if agent == "claude":
                dest = data / "claude" / "projects" / slug(repo) / src.name
            else:
                stamp = re.match(r"rollout-(\d{4})-(\d{2})-(\d{2})T", src.name)
                if not stamp:
                    raise Refused("unexpected Codex file name %s" % src.name)
                dest = data / "codex" / "sessions" / stamp.group(1) / stamp.group(2) / stamp.group(3) / src.name
            text = src.read_text(encoding="utf-8").replace(REPO, repo).replace(HOME, home)
            dest.parent.mkdir(parents=True, exist_ok=True)
            dest.write_text(text, encoding="utf-8")
            os.utime(dest, (yesterday, yesterday))  # past the import's 30-minute quiet period
            seeded += 1
    print("Created %s with %d sessions in %s. Next:" % (repo, seeded, data))
    print("  cd %s" % repo)
    print("  ox init --endpoint <url>")
    print("  ox session import --from-test-data .git/ox-test-data --dry-run")
    return 0


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    sub = parser.add_subparsers(dest="command", required=True)
    cap_cmd = sub.add_parser("capture", help="clean a repo's sessions into a fixture")
    cap_cmd.add_argument("source")
    cap_cmd.add_argument("--out", required=True)
    cap_cmd.add_argument("--identity", action="append", default=[], help="a name, email or handle to replace (repeatable)")
    cap_cmd.add_argument("--allow-active", action="store_true")
    new_cmd = sub.add_parser("create", help="build a fresh repo seeded with a fixture's sessions")
    new_cmd.add_argument("target")
    new_cmd.add_argument("--fixture", default=str(DEFAULT_FIXTURE))
    args = parser.parse_args(argv)
    try:
        return capture(args) if args.command == "capture" else create(args)
    except Refused as err:
        print("refused: %s" % err, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
