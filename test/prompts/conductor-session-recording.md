# Conductor + ox E2E Integration Test

You are running inside Conductor. Your job is to verify that ox session recording works correctly in this environment. Run each phase sequentially, collecting pass/fail results. At the end, print a report card.

**Rules:**
- Run every command shown. Do not skip phases.
- Record PASS or FAIL for each test. Include brief notes on failures.
- Do not fix issues — only diagnose and report.
- After Phase 5 stops the session, Phase 6 must start a fresh one before testing abort.

---

## Phase 1: Environment Detection

Run these commands and check the results:

```bash
# 1a. Verify we're in Conductor
echo "CONDUCTOR_WORKSPACE_PATH=$CONDUCTOR_WORKSPACE_PATH"
echo "CONDUCTOR_ROOT_PATH=$CONDUCTOR_ROOT_PATH"
# PASS if CONDUCTOR_WORKSPACE_PATH is set and non-empty

# 1b. Verify this is a git worktree (not a standalone clone)
cat .git
# PASS if output starts with "gitdir:" (file, not directory)

# 1c. Verify .sageox/ is initialized
cat .sageox/config.json | python3 -c "import sys,json; d=json.load(sys.stdin); print(f'repo_id={d.get(\"repo_id\",\"MISSING\")}')"
# PASS if repo_id is present and non-empty

# 1d. Verify ox is on PATH
ox version
# PASS if ox version prints without error
```

---

## Phase 2: Session Auto-Start

The SessionStart hook should have fired `ox agent prime` automatically when this conversation started. Verify:

```bash
# 2a. Check agent ID is set
echo "SAGEOX_AGENT_ID=$SAGEOX_AGENT_ID"
# PASS if non-empty

# 2b. Check session is recording
ox session status --json 2>/dev/null | python3 -c "
import sys,json
d=json.load(sys.stdin)
print(json.dumps({'recording': d.get('recording'), 'agent_id': d.get('agent_id'), 'entry_count': d.get('entry_count', 0), 'workspace_path': d.get('workspace_path', 'MISSING')}, indent=2))
"
# PASS if recording=true AND agent_id matches SAGEOX_AGENT_ID

# 2c. Check workspace_path points to this Conductor workspace (not the main repo)
# PASS if workspace_path == CONDUCTOR_WORKSPACE_PATH or matches $(pwd)
```

Record the `entry_count` value — you'll need it for Phase 3.

---

## Phase 3: Incremental Recording

Do a trivial action to trigger the PostToolUse hook, then check that recording data grew.

**Step 1:** Read any small file (e.g., `cat Makefile | head -5` or use the Read tool on `Makefile`).

**Step 2:** Check entry count increased:

```bash
# 3a. Check entry count after tool use
ox session status --json 2>/dev/null | python3 -c "
import sys,json
d=json.load(sys.stdin)
print(f'entry_count={d.get(\"entry_count\", 0)}')
"
# PASS if entry_count > the value recorded in Phase 2

# 3b. Verify raw.jsonl exists and has content
ox session status --json 2>/dev/null | python3 -c "
import sys,json
d=json.load(sys.stdin)
session_file = d.get('session_file', '')
print(f'session_file={session_file}')
"
# Note: session_file points to Claude's JSONL, not the ledger raw.jsonl.
# The raw.jsonl is written at session stop. During recording, data accumulates
# via the hook writing to .recording.json entry_count.
# PASS if entry_count increased from Phase 2
```

---

## Phase 4: Shared Ledger Verification

Verify the ledger is accessible from this worktree and shows sessions from multiple agents/workspaces.

```bash
# 4a. Check ledger availability
ox session list --json --all 2>/dev/null | python3 -c "
import sys,json
d=json.load(sys.stdin)
print(json.dumps({
    'ledger_available': d.get('ledger_available'),
    'total_sessions': d.get('total'),
    'repo_id': d.get('repo_id', 'MISSING')
}, indent=2))
"
# PASS if ledger_available=true

# 4b. Check for sessions from multiple agent IDs (proves shared ledger)
ox session list --json --all 2>/dev/null | python3 -c "
import sys,json
d=json.load(sys.stdin)
names = [s['name'] for s in d.get('sessions', [])]
# Session name format: TIMESTAMP-USERNAME-AGENTID
agent_ids = set()
for n in names:
    parts = n.split('-')
    if len(parts) >= 5:
        agent_ids.add(parts[-1])
print(f'unique_agent_ids={len(agent_ids)}: {sorted(agent_ids)[:10]}')
print(f'sample_sessions={names[:5]}')
"
# PASS if unique_agent_ids > 1 (sessions from different workspaces visible)
# NOTE: If this is the very first Conductor workspace, unique_agent_ids=1 is acceptable.
#       Mark as SKIP with note "only one agent has recorded so far"

# 4c. Verify ledger path resolves from worktree
ox status --json 2>/dev/null | python3 -c "
import sys,json
d=json.load(sys.stdin)
ledger = d.get('ledger', {})
print(json.dumps({'exists': ledger.get('exists'), 'path': ledger.get('path', 'MISSING')}, indent=2))
"
# PASS if exists=true and path is non-empty
```

---

## Phase 5: Full Lifecycle — Stop + Verify Artifacts

**IMPORTANT:** This phase stops the current session. After this, there is no active recording until Phase 6 starts a new one.

**Step 1:** Record the current session name for later verification:

```bash
# 5a. Capture session name before stopping
ox session status --json 2>/dev/null | python3 -c "
import sys,json; d=json.load(sys.stdin)
print(f'agent_id={d.get(\"agent_id\")}')"
```

**Step 2:** Stop the session using the ox-session-stop skill. Run: `/ox-session-stop`

After the skill completes, verify the output:
- PASS if `success: true` in the output
- Note the `ledger_session_dir` path from the output

**Step 3:** Verify artifacts exist:

```bash
# 5b. Check artifacts in the ledger session directory
# Use the ledger_session_dir from the stop output, or find it:
LEDGER_BASE=$(ox status --json 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin).get('ledger',{}).get('path',''))")
echo "Ledger base: $LEDGER_BASE"

# Find the most recently modified session directory
LATEST_SESSION=$(ls -td "$LEDGER_BASE/sessions/"*/ 2>/dev/null | head -1)
echo "Latest session dir: $LATEST_SESSION"

if [ -n "$LATEST_SESSION" ]; then
    echo "--- Artifacts ---"
    for f in raw.jsonl events.jsonl summary.md session.html session.md meta.json; do
        if [ -f "$LATEST_SESSION/$f" ]; then
            SIZE=$(wc -c < "$LATEST_SESSION/$f" | tr -d ' ')
            echo "  $f: ${SIZE} bytes"
        else
            echo "  $f: MISSING"
        fi
    done
    # PASS if raw.jsonl and events.jsonl exist and are non-empty
    # summary.md, session.html, session.md may be LFS pointers (small) — that's OK

    # Check .recording.json was cleaned up
    if [ -f "$LATEST_SESSION/.recording.json" ]; then
        echo "  .recording.json: STILL EXISTS (should be deleted after stop)"
    else
        echo "  .recording.json: cleaned up (good)"
    fi
fi

# 5c. Verify session appears in list
ox session list --json 2>/dev/null | python3 -c "
import sys,json
d=json.load(sys.stdin)
for s in d.get('sessions', [])[:5]:
    print(f'{s[\"name\"]}: status={s.get(\"status\",\"unknown\")}')
"
# PASS if the stopped session shows status "uploaded" or "local"
```

---

## Phase 6: Abort Path (Destructive)

Start a fresh session, create some recording data, then abort it. Verify the session is completely discarded.

**Step 1:** Start a new session:

```bash
# 6a. Prime a new agent (starts fresh session)
AGENT_ENV=claude-code ox agent prime --json 2>/dev/null | python3 -c "
import sys,json; d=json.load(sys.stdin)
print(json.dumps({
    'agent_id': d.get('agent_id'),
    'session_recording': d.get('session', {}).get('recording'),
}, indent=2))
"
# PASS if agent_id is set and session_recording=true
```

**Step 2:** Read a file to generate at least one recording entry (use the Read tool on any file).

**Step 3:** Capture the new session name, then abort:

```bash
# 6b. Record the session name before abort
NEW_SESSION=$(ox session status --json 2>/dev/null | python3 -c "
import sys,json; d=json.load(sys.stdin)
aid=d.get('agent_id','unknown')
print(aid)
")
echo "New agent_id to abort: $NEW_SESSION"
```

**Step 4:** Abort the session using the ox-session-abort skill. Run: `/ox-session-abort`

**Step 5:** Verify the session was discarded:

```bash
# 6c. Verify session is gone
ox session status --json 2>/dev/null | python3 -c "
import sys,json; d=json.load(sys.stdin)
print(f'recording={d.get(\"recording\", False)}')
"
# PASS if recording=false (no active session)

# 6d. Verify aborted session does NOT appear in session list
ox session list --json 2>/dev/null | python3 -c "
import sys,json
d=json.load(sys.stdin)
names = [s['name'] for s in d.get('sessions', [])]
print(f'total_sessions={len(names)}')
# The aborted session should not appear
print('Recent sessions:')
for n in names[:5]:
    print(f'  {n}')
"
# PASS if the aborted session name does not appear in the list
```

---

## Report Card

After completing all phases, print a summary table exactly like this (fill in results):

```
## Conductor + ox E2E Report Card

| Phase | Test | Result | Notes |
|-------|------|--------|-------|
| 1a | CONDUCTOR_WORKSPACE_PATH set | PASS/FAIL | |
| 1b | Git worktree detected | PASS/FAIL | |
| 1c | .sageox/ initialized with repo_id | PASS/FAIL | |
| 1d | ox CLI available | PASS/FAIL | |
| 2a | SAGEOX_AGENT_ID set | PASS/FAIL | |
| 2b | Session recording active | PASS/FAIL | |
| 2c | workspace_path matches Conductor workspace | PASS/FAIL | |
| 3a | entry_count increased after tool use | PASS/FAIL | |
| 4a | Ledger available from worktree | PASS/FAIL | |
| 4b | Sessions from multiple agents visible | PASS/FAIL/SKIP | |
| 4c | Ledger path resolves correctly | PASS/FAIL | |
| 5a | Session stop succeeded | PASS/FAIL | |
| 5b | Artifacts exist (raw.jsonl, events.jsonl) | PASS/FAIL | |
| 5c | Stopped session in list | PASS/FAIL | |
| 6a | New session started after stop | PASS/FAIL | |
| 6b | Session abort succeeded | PASS/FAIL | |
| 6c | No active recording after abort | PASS/FAIL | |
| 6d | Aborted session not in list | PASS/FAIL | |

**Overall: X/18 PASS, Y/18 FAIL, Z/18 SKIP**

### Failures (if any)
<Detail each failure with the command output that shows the problem>
```
