# ox MCP Server — Design Specification

## Overview

A local MCP (Model Context Protocol) server embedded in the `ox` CLI that connects Claude Chat, Claude Code cowork sessions, and other MCP clients to a team's shared knowledge and coordination layer.

It does two things:
1. **Pulls team context in** — discussions, decisions, semantic search across team knowledge
2. **Pushes coordination signals out** — murmurs that tell the team what you're working on and flag conflicts

## Problem

Today, `ox` integrates with Claude Code through shell hooks — `ox agent prime` runs on session start, injects team context, and `ox agent session` records conversations. This works when Claude Code runs inside a terminal, inside a repo that's been `ox init`-ed.

But there are environments where hooks don't work:

- **Claude Chat (Desktop)** — no shell, no hooks, no working directory
- **Claude Code cowork** — the orchestrating agent may not be in a repo
- **Cursor / Windsurf / other MCP clients** — different hook systems, if any
- **Multi-repo workflows** — agent needs context from Team A while coding in Repo B

MCP is the universal adapter. An MCP server turns `ox` from a "Claude Code plugin" into infrastructure that works everywhere.

## Targets

| Environment | Supported | Notes |
|-------------|-----------|-------|
| Claude Chat (Desktop) | **Yes** | Primary target |
| Claude Code cowork | **Yes** | Primary target |
| Claude Code in-terminal | **No** | Already served by hooks (`ox agent prime`) |
| Cursor / Windsurf / other MCP clients | **Future** | Should work if they support stdio MCP |

## Anti-Goals

- **Not replacing `ox agent prime`** — hooks remain the optimal path for Claude Code in-terminal (lower token cost, automatic). In MCP, the tool catalog IS the context delivery mechanism — the agent pulls on demand rather than getting a push at session start.
- **Not doing session recording via MCP** — MCP is request/response, wrong shape for continuous recording
- **Not exposing code search in v1** — requires a repo checkout with CodeDB index
- **Not building install automation for v1** — just documentation
- **Not a cloud API proxy** — the MCP server talks to `ox` locally and reads from daemon-synced paths

---

## Architecture

### Multi-Daemon Model

The MCP server manages multiple per-ledger daemons rather than a single global daemon. Each daemon remains 1:1 with a ledger, matching the existing daemon architecture. The MCP server acts as a router.

```
┌──────────────────────┐
│ Claude Chat / Cowork  │
│ (MCP client)          │
└──────────┬───────────┘
           │ MCP protocol (stdio)
           │
┌──────────▼───────────┐
│ ox mcp serve          │
│ (embedded Go server)  │
│                       │
│ - 3 tool handlers     │
│ - Daemon lifecycle    │
│   management          │
│ - Routes IPC to       │
│   correct daemon      │
└──┬────────────┬──────┘
   │            │
   ▼            ▼ (lazy, on demand)
┌────────┐   ┌────────┐   ┌────────┐
│ Daemon │   │ Daemon │   │ Daemon │
│ Repo A │   │ Repo B │   │ Repo C │
│(ledger │   │(ledger │   │(ledger │
│+ team  │   │+ team  │   │+ team  │
│ ctx)   │   │ ctx)   │   │ ctx)   │
└────────┘   └────────┘   └────────┘
```

### Startup Flow

```
ox mcp serve starts (by Claude Chat/cowork)
  → read auth token from ~/.config/sageox/auth.json
  → GET /api/v1/cli/repos → all repos (repo_id, team_id, name)
  → pick first repo → GET /api/v1/repos/{repo_id}/ledger-status → clone URL
  → ox daemon start --ledger=<repo_id> --endpoint=<url> --team=<team_slug>
  → bootstrap daemon syncs team contexts + ledger
  → MCP server ready to serve tools
```

**One eager daemon** is started on MCP boot (first repo from API) to bootstrap team context sync. Additional per-ledger daemons start **lazily** when a murmur or session request targets a different ledger.

### Daemon Lifecycle

- **Startup:** MCP server starts daemons via `ox daemon start --ledger=<repo_id> --endpoint=<url> --team=<team_slug>`
- **Ready detection:** Poll with IPC ping (retry with backoff until daemon responds)
- **Routing:** Each daemon has a Unix socket at `~/.cache/sageox/daemon/<workspace_id>.sock` where workspace ID = SHA256 of ledger path on disk
- **Shutdown:** Daemons are NOT stopped when MCP client disconnects. They persist and self-exit after the standard 1-hour inactivity timeout. This means faster reconnection if the user restarts Claude.

### Key Architectural Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Language | **Go** (embedded in ox) | No runtime deps, ships as single binary, direct access to internal APIs |
| MCP SDK | **`modelcontextprotocol/go-sdk`** v1.4.x | Official SDK, maintained by Google Go team + MCP org, stable v1.x API |
| Transport | **stdio** | Standard for Claude Desktop / Claude Code; no port management |
| Daemon model | **Per-ledger daemons** (1 eager + N lazy) | Keeps daemon 1:1 with ledger (no architecture change), MCP server routes |
| Team context reads | **Direct disk reads** | Daemon syncs paths, MCP server reads files; no new CLI commands needed |
| Query | **Subprocess** (`ox query --json`) | Keeps search logic centralized in one place |
| Murmur | **Daemon IPC** (existing `murmur` message type) | Already exists, fire-and-forget, fast |
| Cold start | **Return what's on disk** + sync-in-progress note | Graceful degradation, no blocking on first call |
| Logging | **Log file** (`~/.cache/sageox/mcp-server.log`) | stdio is protocol-only; logs must go elsewhere |

### Why Embedded in ox (Not Standalone)?

The original design doc proposed a standalone TypeScript MCP server that shells out to `ox` CLI commands. We chose to embed it in ox instead:

1. **No subprocess overhead** — direct function calls for disk reads and IPC (subprocess only for `ox query`)
2. **No runtime dependency** — users don't need Node.js/npx installed
3. **Direct access to internal APIs** — daemon IPC, config, paths, auth store
4. **Single binary distribution** — `ox` is the MCP server
5. **Version coupling** — MCP server always matches the ox version

---

## Daemon Changes

### New Startup Mode: `--ledger` Flag

**Problem:** Today the daemon is started from within a project directory. It derives ProjectRoot from CWD and LedgerPath from project config. The MCP server needs to start daemons for arbitrary ledgers without being inside a project.

**Solution:** New daemon startup flags that bypass project config:

```bash
ox daemon start --ledger=<repo_id> --endpoint=<url> --team=<team_slug>
```

| Flag | Required | Description |
|------|----------|-------------|
| `--ledger` | Yes | Repo ID (e.g., `repo_01934f5a...`). Daemon resolves ledger path: `~/.local/share/sageox/<endpoint>/ledgers/<repo_id>/` |
| `--endpoint` | Yes | SageOx API endpoint URL (normalized) |
| `--team` | Yes | Team slug (informational — for daemon's team context discovery) |

**Behavior when `--ledger` is provided:**
- Skip `findGitRoot()` — no project root needed
- Resolve ledger path from `--ledger` repo ID + `--endpoint`
- Resolve endpoint from `--endpoint` (not from project config)
- Start standard sync loop: credential refresh → team discovery → ledger sync + team context sync
- Workspace ID derived from SHA256 of resolved ledger path (consistent with socket path)

**Existing behavior (no `--ledger` flag) is unchanged.** This is a new code path, not a modification of the existing one.

**Files affected:**
- `cmd/ox/daemon.go` — add `--ledger`, `--endpoint`, `--team` flags to `daemon start`
- `internal/daemon/config.go` — `Config` struct gains optional `RepoID` and `Endpoint` fields
- `internal/daemon/daemon.go` — startup path branches on whether `--ledger` is provided
- `internal/daemon/workspace_registry.go` — `rebuildFromConfigLocked()` handles ledger-only mode

### Ledger Discovery from API

The MCP server (not the daemon) discovers available ledgers:

```
MCP server startup:
  → auth token from auth store
  → GET /api/v1/cli/repos → [{repo_id, team_id, name, created_at}, ...]
  → for each repo: GET /api/v1/repos/{repo_id}/ledger-status → clone URL + status
  → cache repo list for disambiguation (multiple ledgers/teams)
```

This uses existing API endpoints — no new server-side APIs needed.

---

## v1 Tool Set

Three tools, targeting ~1.5k tokens total for tool catalog overhead.

### Tool 1: `ox_ctx`

**Purpose:** Read your team's shared knowledge — recent discussions, distilled decisions, principles.

**Data source:** Daemon-synced team context paths on disk:
```
~/.local/share/sageox/<endpoint>/teams/<team_id>/
```

**Parameters:**
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `team` | string | No | Primary team | Team slug or ID |

**Behavior:**
- If `team` is provided, reads that team's context from the synced path
- If `team` is omitted and the user belongs to one team, uses that team
- If `team` is omitted and the user belongs to multiple teams, returns the list of available teams (agent asks the user, re-calls with chosen team)
- If team context is not yet synced (cold start), returns whatever is on disk with a note: "Team context is syncing, data may be incomplete"

**Returns:** SOUL.md content, discussion titles + dates, distilled summary, docs/ and memory/ contents.

**Prerequisites:** `ox login` (auth token), at least one daemon running and syncing team contexts.

**Implementation:** Direct disk reads from synced paths (`internal/mcp/server.go`).

### Tool 2: `ox_q`

**Purpose:** Semantic search across team knowledge — discussions, documents, session recordings.

**Data source:** Subprocess call to `ox query --json` (which hits cloud API internally).

**Parameters:**
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `query` | string | Yes | — | Natural language search text |
| `team` | string | No | Primary team | Team slug or ID to search |
| `limit` | integer | No | 5 | Max results |

**Behavior:**
- Calls `ox query "<text>" --source=team --json` as subprocess
- Same team-ambiguity pattern as `ox_ctx` (return available teams if multiple and none specified)

**Returns:** Ranked results with titles, excerpts, relevance scores, source types.

**Prerequisites:** `ox login` (auth token), network access to cloud API.

**Not included in v1:** `--source=code` (local code index) — requires repo checkout with CodeDB.

**Implementation:** Subprocess exec of `ox query --json` (`internal/mcp/server.go`).

### Tool 3: `ox_murmur`

**Purpose:** Publish and list coordination signals to/from other AI coworkers and humans.

**Data source:** Daemon IPC (`murmur` message type for publish, `whisper_history` for list).

**Parameters (publish):**
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `action` | string | No | `publish` | `publish` or `list` |
| `content` | string | Yes (publish) | — | Message to broadcast |
| `topic` | string | No | — | Topic slug: `wip`, `architecture`, `conflict`, etc. |
| `ledger` | string | No | — | Ledger/repo name to publish to |
| `importance` | string | No | `normal` | `ambient`, `normal`, or `critical` |

**Parameters (list):**
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `action` | string | Yes | — | Must be `list` |
| `ledger` | string | No | — | Filter to specific ledger; omit for all |
| `since` | string | No | `4h` | Time window: `4h`, `1d`, `3d` |

**Ledger scoping behavior:**
- If `ledger` is provided, route to that ledger's daemon (start lazily if needed)
- If `ledger` is omitted and user has one ledger, use it automatically
- If `ledger` is omitted and user has multiple ledgers, return the list of available ledgers (agent asks user, re-calls with chosen ledger)
- **List action:** defaults to showing murmurs across all active daemons when no ledger specified

**Returns (publish):** Murmur ID confirming publication.
**Returns (list):** Recent murmurs with author, content, topic, timestamp.

**Implementation:** Daemon IPC via existing `murmur` message type (`internal/mcp/server.go`).

**Future:** Team-level murmurs (not ledger-scoped) are planned but not in v1.

---

## MCP Server Implementation

### File Structure

```
cmd/ox/mcp.go                    — cobra command: ox mcp serve
internal/mcp/server.go           — MCPServer struct: tool handlers, daemon management, IPC routing
```

All tool handler logic lives in `server.go` alongside daemon management. No separate `tools/` package — keep it simple until complexity warrants splitting.

### Go MCP SDK Integration

**Dependency:** `github.com/modelcontextprotocol/go-sdk/mcp` v1.4.x

```go
// cmd/ox/mcp.go
var mcpServeCmd = &cobra.Command{
    Use:   "serve",
    Short: "Start MCP server for Claude Chat and cowork sessions",
    RunE: func(cmd *cobra.Command, args []string) error {
        srv, err := mcp.NewMCPServer(ctx)
        if err != nil {
            return err
        }
        return srv.Run(ctx) // blocks until client disconnects
    },
}
```

```go
// internal/mcp/server.go
type MCPServer struct {
    server    *mcp.Server
    repos     []RepoInfo        // cached from /api/v1/cli/repos
    daemons   map[string]*DaemonConn  // repo_id → daemon connection
    authToken string
    endpoint  string
    logFile   *os.File
}

func NewMCPServer(ctx context.Context) (*MCPServer, error) {
    // 1. Read auth token from auth store
    // 2. Discover repos via GET /api/v1/cli/repos
    // 3. Start bootstrap daemon (first repo)
    // 4. Register MCP tools
    // 5. Return server ready to Run()
}

func (s *MCPServer) Run(ctx context.Context) error {
    return s.server.Run(ctx, &mcp.StdioTransport{})
}
```

### Tool Registration

```go
mcp.AddTool(s.server, &mcp.Tool{
    Name:        "ox_ctx",
    Description: "Read your team's shared knowledge — discussions, decisions, principles",
}, s.handleTeamContext)

mcp.AddTool(s.server, &mcp.Tool{
    Name:        "ox_q",
    Description: "Search team knowledge — discussions, documents, sessions",
}, s.handleQuery)

mcp.AddTool(s.server, &mcp.Tool{
    Name:        "ox_murmur",
    Description: "Publish or list coordination signals to/from coworkers",
}, s.handleMurmur)
```

### Daemon Management

```go
// ensureDaemon starts a daemon for the given repo if not already running.
// Returns the daemon connection for IPC.
func (s *MCPServer) ensureDaemon(repoID, teamSlug string) (*DaemonConn, error) {
    if conn, ok := s.daemons[repoID]; ok {
        if conn.Ping() == nil {
            return conn, nil // already running
        }
    }

    // Resolve ledger path
    ledgerPath := paths.LedgerDir(s.endpoint, repoID)

    // Start daemon
    // ox daemon start --ledger=<repo_id> --endpoint=<url> --team=<team_slug>
    cmd := exec.Command("ox", "daemon", "start",
        "--ledger", repoID,
        "--endpoint", s.endpoint,
        "--team", teamSlug,
    )
    if err := cmd.Start(); err != nil {
        return nil, fmt.Errorf("start daemon for %s: %w", repoID, err)
    }

    // Wait for ready (poll with IPC ping, backoff)
    conn, err := waitForDaemon(ledgerPath, 30*time.Second)
    if err != nil {
        return nil, fmt.Errorf("daemon for %s not ready: %w", repoID, err)
    }

    s.daemons[repoID] = conn
    return conn, nil
}
```

### Heartbeats

The MCP server sends periodic heartbeats to each managed daemon, replicating the CLI's heartbeat pattern. Heartbeats serve two purposes: (1) keep daemons alive by resetting their 1-hour inactivity timeout, and (2) deliver fresh credentials for sync operations.

**Pattern:** Fire-and-forget via `SendOneWay()` with 50ms write deadline. All errors silently ignored — heartbeat failure must never affect MCP tool responses.

```go
// MCPServer starts a heartbeat loop per daemon on creation
func (s *MCPServer) startHeartbeatLoop(ctx context.Context, repoID string, conn *DaemonConn) {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            _ = conn.SendOneWay(daemon.Message{
                Type:        daemon.MsgTypeHeartbeat,
                WorkspaceID: conn.WorkspaceID,
                Payload: daemon.HeartbeatPayload{
                    AgentID:     "mcp-server",
                    AgentType:   "mcp",
                    CLIVersion:  version.Version,
                    Timestamp:   time.Now().UTC(),
                    Credentials: s.loadCredentials(), // git PAT + auth token
                    TeamIDs:     s.teamIDs(),
                },
            })
        }
    }
}
```

**Heartbeat payload fields** (matching CLI's `HeartbeatPayload`):
- `AgentID`: `"mcp-server"` (identifies the MCP process)
- `AgentType`: `"mcp"` (distinguishes from `"claude-code"` agents)
- `CLIVersion`: ox binary version
- `Timestamp`: current UTC time
- `Credentials`: git PAT + OAuth auth token (daemon uses these for sync auth)
- `TeamIDs`: team IDs from cached repo list

**Timing:** Every 5 minutes per daemon. The daemon's inactivity timeout is 1 hour, so 5-minute heartbeats provide ample margin. The first heartbeat fires immediately on daemon startup to deliver initial credentials.

**Credential refresh:** The MCP server reads credentials from the auth store on each heartbeat (not cached). This ensures the daemon always gets the freshest available token.

### Telemetry

The MCP server emits telemetry events using the same system as the CLI. Telemetry is fire-and-forget — never blocks tool responses.

**Three delivery paths** (matching CLI patterns):

1. **Daemon IPC** (primary, for most events): Send `telemetry` messages to the bootstrap daemon via `SendOneWay()` with 50ms timeout. The daemon batches and forwards to the telemetry endpoint.
2. **Direct API** (daemon start failures only): When a daemon fails to start, IPC isn't available — use `telemetry.Client.TrackDaemonStartFailure()` which sends directly to the cloud API. This is the same exception the CLI uses (`internal/telemetry/client.go:479`), with built-in rate limiting (exponential backoff: 1min → 2min → 4min → ... → 32min max) to prevent swarms during persistent failures.
3. **Disk queue** (pre-bootstrap fallback): If no daemon is available yet and the event isn't a daemon start failure, queue to `~/.sageox/cache/telemetry.jsonl`. Next CLI invocation flushes the queue.

**Events emitted by MCP server:**

| Event | When | Delivery path | Properties |
|-------|------|---------------|------------|
| `mcp_server_start` | `ox mcp serve` begins | Disk queue (pre-bootstrap) | `app_type=mcp-server`, endpoint, team count, repo count |
| `mcp_server_stop` | MCP client disconnects | Daemon IPC | `app_type=mcp-server`, uptime, tool_call_count |
| `mcp_tool_call` | Any tool invocation | Daemon IPC | `tool_name`, `duration_ms`, `success`, `error_code` |
| `mcp_daemon_start` | Daemon started for ledger | Daemon IPC | `repo_id`, `eager_or_lazy`, `startup_duration_ms` |
| `daemon:start_failure` | Daemon failed to start | **Direct API** | `error_type`, `error_msg` (rate-limited) |

**Opt-out:** Respects the same `DO_NOT_TRACK=1` / `SAGEOX_TELEMETRY=false` / user config as CLI.

```go
func (s *MCPServer) emitTelemetry(event string, props map[string]any) {
    props["app_type"] = "mcp-server"
    payload := telemetry.TelemetryPayload{Event: event, Props: props}

    // Try daemon IPC first (fire-and-forget)
    if conn := s.bootstrapDaemon(); conn != nil {
        _ = conn.SendOneWay(daemon.Message{
            Type:    daemon.MsgTypeTelemetry,
            Payload: marshalPayload(payload),
        })
        return
    }

    // Fallback: queue to disk
    s.telemetryClient.Queue(payload)
}

// Daemon start failures bypass IPC entirely — daemon isn't running.
// Uses CLI's existing TrackDaemonStartFailure with rate-limited direct API send.
func (s *MCPServer) trackDaemonStartFailure(errorType, errorMsg string) {
    s.telemetryClient.TrackDaemonStartFailure(errorType, errorMsg)
}
```

### Friction Events

The MCP server captures friction events when tool calls fail due to user-facing issues (e.g., bad parameters, auth failures). Uses the same `frictionax` system as the CLI.

**Events captured:**
- Invalid tool parameters (wrong type, missing required field)
- Auth-related errors surfaced to agent
- Daemon unavailable when tool requires it

**Delivery:** Fire-and-forget IPC to bootstrap daemon (`friction` message type), matching the CLI's 50ms timeout pattern.

### Observability: Issue Tracking

The MCP server monitors daemon health and surfaces issues through tool error responses. It leverages the daemon's existing `IssueTracker` system.

**MCP server-side tracking:**

The MCPServer struct maintains a lightweight issue log for its own operations:

```go
type MCPIssue struct {
    Type      string    // e.g., "daemon_start_failed", "daemon_restart", "api_unreachable"
    RepoID    string    // which ledger (empty for global)
    Message   string    // human-readable description
    Since     time.Time // when first detected
    Attempts  int       // retry count (for daemon start failures)
}
```

| Issue Type | Trigger | Surfaced How |
|------------|---------|-------------|
| `daemon_start_failed` | Daemon fails to start after all retries | Tool error response: `"Cannot reach ledger <name>. Daemon failed to start after N attempts."` |
| `daemon_restart` | Daemon found dead on health check, restarted | Logged to MCP log file. Not surfaced to agent unless tool call in flight. |
| `daemon_unhealthy` | Daemon responds but reports `NeedsHelp=true` | Include daemon issues summary in next tool response as advisory note |
| `api_unreachable` | Cloud API call fails (for query) | Tool error response with retry guidance |
| `auth_expired` | Auth token expired, refresh failed | All tools return: `"Authentication expired. Run 'ox login' to refresh."` |

**Daemon issue pass-through:** On each tool call that routes through a daemon, the MCP server checks `status.NeedsHelp` and `status.Issues`. If issues exist (e.g., `clone_failed`, `sync_backoff`), an advisory note is appended to the tool response:

```json
{
  "content": "... tool result ...",
  "_advisory": "Daemon reports: Clone failed for ledger repo. Run 'ox doctor' for details."
}
```

**Logging:** All issues are written to `~/.cache/sageox/mcp-server.log` with timestamps. The log includes daemon start/stop events, IPC errors, API failures, and retry attempts.

### Daemon Startup Robustness

The MCP server must handle daemon startup failures gracefully, with retry and exponential backoff.

#### Retry with Exponential Backoff

```go
func (s *MCPServer) startDaemonWithRetry(ctx context.Context, repoID, teamSlug string) (*DaemonConn, error) {
    maxAttempts := 3
    baseDelay := 2 * time.Second

    for attempt := 1; attempt <= maxAttempts; attempt++ {
        conn, err := s.tryStartDaemon(ctx, repoID, teamSlug)
        if err == nil {
            return conn, nil
        }

        s.logf("daemon start failed for %s (attempt %d/%d): %v", repoID, attempt, maxAttempts, err)

        // Send directly to API (not via daemon IPC — daemon is what failed)
        // Rate-limited internally by TrackDaemonStartFailure (exp backoff: 1min → 32min)
        s.trackDaemonStartFailure("mcp_start_failed", err.Error())

        if attempt < maxAttempts {
            delay := baseDelay * time.Duration(1<<(attempt-1)) // 2s, 4s
            select {
            case <-ctx.Done():
                return nil, ctx.Err()
            case <-time.After(delay):
            }
        }
    }

    // Record persistent issue
    s.setIssue(MCPIssue{
        Type:     "daemon_start_failed",
        RepoID:   repoID,
        Message:  fmt.Sprintf("Failed to start daemon after %d attempts", maxAttempts),
        Since:    time.Now(),
        Attempts: maxAttempts,
    })

    return nil, fmt.Errorf("daemon for %s failed to start after %d attempts", repoID, maxAttempts)
}
```

**Retry schedule:** 3 attempts with delays of 2s → 4s. Total worst-case: ~8s before surfacing error to agent.

**Error surfacing:** After exhausting retries, the tool returns a structured error to the agent:
```json
{
  "isError": true,
  "content": "Cannot start sync daemon for repo 'my-project'. Tried 3 times over 8 seconds. The user may need to run 'ox doctor' to diagnose."
}
```

#### Singleton Guarantee Per Ledger

The MCP server leverages the existing daemon singleton mechanisms — it does NOT implement its own locking:

1. **Pre-start cleanup:** Before starting a daemon, call `daemon.KillStaleDaemonForCurrentWorkspace()` which handles stale socket detection and PID-verified cleanup.

2. **Socket binding is the lock:** When the daemon calls `net.Listen("unix", socketPath)`, the OS guarantees only one process can bind. If another daemon is already running for this ledger, the new one fails to bind → the MCP server detects this and connects to the existing daemon instead.

3. **Registry lookup:** Before starting a new daemon, check the daemon registry (`~/.config/sageox/daemon-registry.json`) via `FindByRepoID(repoID)`. If a daemon is already registered and responds to ping, reuse it.

4. **Supersession detection:** If the MCP server starts a daemon that supersedes an existing one (e.g., from a terminal session), the existing daemon's 30-second socket check detects the PID change and exits gracefully.

```go
func (s *MCPServer) tryStartDaemon(ctx context.Context, repoID, teamSlug string) (*DaemonConn, error) {
    ledgerPath := paths.LedgerDir(s.endpoint, repoID)
    workspaceID := daemon.WorkspaceIDFromPath(ledgerPath)

    // 1. Check if daemon already running (registry + ping)
    if info := daemon.FindDaemonForRepo(repoID); info != nil {
        conn, err := daemon.NewClientWithSocket(info.SocketPath)
        if err == nil && conn.Ping() == nil {
            return &DaemonConn{Client: conn, WorkspaceID: workspaceID}, nil
        }
    }

    // 2. Clean up any stale daemon for this workspace
    daemon.KillStaleDaemonForWorkspace(workspaceID)

    // 3. Start new daemon
    cmd := exec.Command("ox", "daemon", "start",
        "--ledger", repoID,
        "--endpoint", s.endpoint,
        "--team", teamSlug,
    )
    cmd.Stdout = s.logFile
    cmd.Stderr = s.logFile
    if err := cmd.Start(); err != nil {
        return nil, fmt.Errorf("exec failed: %w", err)
    }

    // 4. Wait for readiness (poll IPC ping with backoff, 30s max)
    conn, err := waitForDaemon(workspaceID, 30*time.Second)
    if err != nil {
        return nil, fmt.Errorf("not ready after 30s: %w", err)
    }

    return conn, nil
}
```

**Restart loop protection:** The daemon's own restart loop detection (max 3 restarts in 5 minutes, exponential backoff up to 2 minutes) protects against the MCP server repeatedly trying to start a crashing daemon. If the daemon enters restart throttling, the MCP server's readiness poll will timeout, triggering the retry-with-backoff path above.

---

## Configuration & Installation

### v1: Documentation Only

No install automation. Users configure manually.

**Claude Code cowork:**
```bash
claude mcp add sageox -- ox mcp serve
```

**Claude Chat (Desktop) — macOS:**

Edit `~/Library/Application Support/Claude/claude_desktop_config.json`:
```json
{
  "mcpServers": {
    "sageox": {
      "command": "ox",
      "args": ["mcp", "serve"]
    }
  }
}
```

**Prerequisites:**
- `ox` binary in PATH (typically `$GOBIN` or `~/go/bin`)
- `ox login` completed (auth token exists)
- No `ox init` required — daemon syncs team contexts from auth store credentials

### CLI Command

```
ox mcp serve

Start the SageOx MCP server for Claude Chat and cowork sessions.

The MCP server connects AI coworkers in Claude Chat, Claude Code cowork, and
other MCP clients to your team's shared knowledge. It exposes three tools:

  ox_ctx  Read team discussions, decisions, and principles
  ox_q         Search team knowledge semantically
  ox_murmur        Publish and list coordination signals

Prerequisites: ox login (no ox init required)

Usage:
  ox mcp serve [flags]

Flags:
  -h, --help   help for serve
```

Zero required flags. Everything auto-discovered from auth store.

### Future: Install Automation

| Approach | Target | Timeline |
|----------|--------|----------|
| `ox mcp install` command | Claude Code (runs `claude mcp add`) | v1.1 |
| Desktop Extension (`.mcpb`) | Claude Chat | v2 |
| Smithery registry | All MCP clients | v2 |

---

## Token Cost Analysis

MCP tool catalogs have a fixed cost: every tool's name, description, and parameter schema is injected into the agent's context on every conversation.

| Tool | Estimated catalog cost |
|------|----------------------|
| `ox_ctx` | ~350 tokens |
| `ox_q` | ~350 tokens |
| `ox_murmur` | ~550 tokens |
| **Total** | **~1.25k tokens** |

This is well under the 2k-10k range flagged in the original design doc. Achieved by shipping 3 tools instead of 5+ and keeping descriptions concise.

The comparison isn't "MCP vs CLI hooks" — it's "MCP vs nothing." 1.4k tokens for team awareness in Claude Chat is a good trade.

---

## Error Handling

### Degraded States

| State | Tool behavior |
|-------|---------------|
| Not authenticated (`ox login` not run) | All tools return error: `"Not authenticated. Run 'ox login' first."` |
| Bootstrap daemon still syncing | `ox_ctx` returns whatever is on disk + `"Team context is syncing, data may be incomplete"` |
| Daemon crashed / not responding | MCP server attempts restart, returns error if still failing after retry |
| No repos found (new user) | All tools return error: `"No repos found. Run 'ox init' in a project first."` |
| Network down | `ox_q` fails with network error. `ox_ctx` works (disk reads). `ox_murmur` publish queued locally. |
| Multiple teams, none specified | Return list of available teams with slugs. Agent asks user and re-calls. |
| Multiple ledgers, none specified | Return list of available ledgers with names. Agent asks user and re-calls. |

---

## Testing Strategy

### Three Layers

| Layer | What | How | When |
|-------|------|-----|------|
| **Unit** | Tool handlers in isolation | Mock daemon IPC, mock file reads, verify JSON schema input/output | Every commit (`make test`) |
| **Integration** (`slow` build tag) | Real ox binary + daemon + MCP stdio | Go test acts as MCP client, calls tools over stdio pipe, verifies responses | Pre-PR (`make test-slow`) |
| **E2E** (release gate) | Real Claude Chat + cowork sessions | BDD-style manual acceptance test plan | Each release |

### Unit Tests

Each tool handler is tested independently:
- Verify parameter validation (required fields, types)
- Verify behavior with single team/ledger (auto-select)
- Verify behavior with multiple teams/ledgers (return list)
- Verify error responses (not authenticated, daemon down, team context not synced)
- Verify cold-start graceful degradation (return partial data + sync note)
- Mock daemon IPC and filesystem reads

Daemon management tests:
- `startDaemonWithRetry` retries 3 times with exponential backoff (2s, 4s)
- `startDaemonWithRetry` emits telemetry on each failure attempt
- `startDaemonWithRetry` records MCPIssue after exhausting retries
- `tryStartDaemon` reuses existing daemon found via registry lookup
- `tryStartDaemon` cleans up stale sockets before starting new daemon
- Heartbeat loop sends at 5-minute intervals with fresh credentials
- Heartbeat failure is silently ignored (never propagates to tool handlers)
- Telemetry opt-out respected (`DO_NOT_TRACK`, `SAGEOX_TELEMETRY=false`)
- Telemetry falls back to disk queue when no daemon available
- Daemon issue pass-through: `NeedsHelp` advisory appended to tool responses

### Integration Tests (`slow` tag)

End-to-end over stdio:
- Build real ox binary
- Start daemon with `--ledger` flag
- Connect as MCP client over stdin/stdout pipe
- Call each tool with real (test) data
- Verify MCP protocol compliance (JSON-RPC, tool results format)
- Verify lazy daemon startup on murmur to second ledger
- Verify singleton: starting MCP server finds and reuses terminal-started daemon
- Verify heartbeats keep daemon alive across inactivity check
- Verify telemetry events reach daemon (inspect daemon telemetry buffer)
- Verify retry backoff: simulate daemon start failure, verify 3 attempts with correct delays
- Verify stale socket cleanup: leave dead socket, verify MCP server recovers

### E2E Acceptance Tests (BDD Manual)

Run against real Claude Chat and Claude Code cowork before each release:

```gherkin
Feature: Team Context via MCP

  Scenario: Agent reads team context in Claude Chat
    Given the user has run "ox login"
    And the MCP server is configured in Claude Chat
    When the user asks "What has our team decided about error handling?"
    Then the agent calls ox_ctx
    And the response includes SOUL.md content and recent discussions

  Scenario: Agent searches team knowledge
    Given the user has run "ox login"
    When the user asks "How do we handle Stripe webhooks?"
    Then the agent calls ox_q with the search text
    And the response includes relevant discussions and session excerpts

  Scenario: Agent publishes a murmur
    Given the user has run "ox login"
    And there is one ledger available
    When the agent calls ox_murmur with content "Refactoring auth middleware"
    Then a murmur is published to the ledger
    And other coworkers see it in their whispers

  Scenario: Multiple ledgers — agent asks for clarification
    Given the user has access to 3 ledgers
    When the agent calls ox_murmur without a ledger parameter
    Then the tool returns the list of available ledgers
    And the agent asks the user which ledger to publish to

  Scenario: Multiple teams — agent asks for clarification
    Given the user belongs to 2 teams
    When the agent calls ox_ctx without a team parameter
    Then the tool returns the list of available teams
    And the agent asks the user which team to query

  Scenario: Not authenticated
    Given the user has NOT run "ox login"
    When the agent calls any sageox tool
    Then the tool returns an error with guidance to run "ox login"

  Scenario: Cold start — team context still syncing
    Given the user has run "ox login"
    And the MCP server just started (bootstrap daemon syncing)
    When the agent calls ox_ctx
    Then the tool returns partial data with a sync-in-progress note

  Scenario: Lazy daemon startup for murmur
    Given the bootstrap daemon is running for Repo A
    When the agent calls ox_murmur targeting Repo B
    Then the MCP server starts a new daemon for Repo B
    And the murmur is published after daemon is ready

  Scenario: MCP client disconnects and reconnects
    Given daemons are running from a previous MCP session
    When the user reopens Claude Chat
    Then ox mcp serve starts and finds existing daemons still running
    And tools work immediately without waiting for sync

Feature: Daemon Robustness

  Scenario: Daemon start failure with retry
    Given the user has run "ox login"
    And the bootstrap daemon fails to start (e.g., port conflict)
    When ox mcp serve retries with exponential backoff
    Then it attempts 3 times with delays of 2s, 4s
    And surfaces a clear error to the agent after exhausting retries
    And emits mcp_daemon_start_failure telemetry on each attempt

  Scenario: Singleton guarantee — reuse existing daemon
    Given a daemon is already running for Repo A (started by terminal session)
    When the MCP server tries to start a daemon for Repo A
    Then it finds the existing daemon via registry lookup
    And reuses the connection instead of starting a duplicate

  Scenario: Stale daemon cleanup
    Given a daemon socket exists but the process is dead
    When the MCP server tries to start a daemon for that ledger
    Then it cleans up the stale socket via KillStaleDaemonForWorkspace
    And starts a fresh daemon successfully

  Scenario: Daemon supersession from MCP
    Given a terminal-started daemon is running for Repo A
    When the MCP server starts a new daemon for Repo A
    Then the old daemon detects PID change in its 30s socket check
    And exits gracefully
    And the MCP server's daemon takes over

  Scenario: Daemon restart loop protection
    Given a daemon crashes repeatedly for Repo A
    When the MCP server tries to start it multiple times
    Then the daemon's restart throttle (3 in 5min) activates
    And the MCP server's readiness poll times out
    And the MCP server returns error to agent with guidance

Feature: Heartbeats and Observability

  Scenario: MCP server sends heartbeats to managed daemons
    Given the MCP server has started a daemon for Repo A
    When 5 minutes elapse
    Then a heartbeat is sent to the daemon via IPC
    And the heartbeat includes fresh credentials from auth store
    And the daemon's inactivity timeout is reset

  Scenario: Heartbeat failure is silent
    Given the MCP server is sending heartbeats
    When the daemon becomes temporarily unresponsive
    Then the heartbeat fails silently (50ms timeout)
    And tool calls continue to work normally
    And no error is surfaced to the agent

  Scenario: Daemon health issues surfaced in tool responses
    Given a daemon reports NeedsHelp=true with clone_failed issue
    When the agent calls ox_ctx
    Then the tool response includes team context data
    And an advisory note: "Daemon reports: Clone failed. Run 'ox doctor'."

Feature: Telemetry

  Scenario: MCP server emits tool call telemetry
    Given the MCP server is running
    When the agent calls ox_q
    Then an mcp_tool_call event is emitted with tool_name, duration_ms, success
    And the event is sent via daemon IPC (fire-and-forget)

  Scenario: Telemetry respects opt-out
    Given DO_NOT_TRACK=1 is set in the environment
    When the MCP server starts and tools are called
    Then no telemetry events are emitted

  Scenario: Telemetry fallback when no daemon available
    Given no daemon is running yet (pre-bootstrap)
    When the MCP server emits a telemetry event
    Then the event is queued to ~/.sageox/cache/telemetry.jsonl
    And the next CLI invocation flushes the queue
```

---

## Implementation Phases

### Phase 1: Daemon `--ledger` Startup Mode

**Goal:** Daemon can start with just a repo ID and endpoint, without a project directory.

**Changes:**
- `cmd/ox/daemon.go` — add `--ledger`, `--endpoint`, `--team` flags
- `internal/daemon/config.go` — `Config` gains `RepoID`, `Endpoint` fields
- `internal/daemon/daemon.go` — branch startup on `--ledger` presence
- `internal/daemon/workspace_registry.go` — `rebuildFromConfigLocked()` handles ledger-only mode

**Tests:** Unit tests for ledger-only startup path; integration test verifying daemon starts and syncs without a project root.

### Phase 2: `ox mcp serve` Command + Tool Handlers

**Goal:** Working MCP server with 3 tools over stdio.

**Changes:**
- `go get github.com/modelcontextprotocol/go-sdk@v1.4.x`
- `cmd/ox/mcp.go` — cobra command for `ox mcp serve`
- `internal/mcp/server.go` — MCPServer struct with tool handlers, daemon management, IPC routing
- Auto-discover repos via API on startup
- Bootstrap daemon for first repo
- Lazy daemon startup for murmur targeting other repos

**Tests:** Unit tests for each tool handler; integration tests over stdio pipe.

### Phase 3: BDD Acceptance Tests + Documentation

**Goal:** Manual test plan verified, docs written.

**Changes:**
- BDD acceptance test scenarios (as above)
- Setup docs for Claude Chat and Claude Code cowork
- `ox mcp serve --help` with clear usage
- Logging to `~/.cache/sageox/mcp-server.log`

---

## Open Questions (Resolved)

| Question | Decision | Rationale |
|----------|----------|-----------|
| Global daemon vs per-ledger? | **Per-ledger daemons** (1 eager + N lazy) | Keeps daemon 1:1 with ledger, no architecture change |
| How to start daemons without a project? | **`ox daemon start --ledger=<repo_id> --endpoint=<url> --team=<team_slug>`** | Explicit flags, daemon resolves paths internally |
| Socket path for ledger-only daemons? | **SHA256 of ledger path on disk** | Matches existing convention (path-based workspace IDs) |
| MCP replaces prime? | **No** — MCP tools ARE the pull-based equivalent | Agent pulls context on demand via tools |
| Team context on cold start? | **Return what's on disk + sync note** | Graceful degradation, no blocking |
| Query implementation? | **Subprocess `ox query --json`** | Keeps search logic centralized |
| Murmur implementation? | **Daemon IPC** (existing `murmur` message type) | Already exists, fast, fire-and-forget |
| `ox mcp serve` flags? | **Zero required flags** | Auto-discover from auth store |
| Daemon cleanup on disconnect? | **Let daemons run** (inactivity timeout) | Faster reconnection |
| Logging? | **Log file** `~/.cache/sageox/mcp-server.log` | stdio is protocol-only |

## FAQ

### Why multiple daemon processes instead of a single MCP daemon?

The existing daemon is tightly coupled to a single ledger — it derives its workspace ID, socket path, sync schedule, and GC lifecycle from one ledger path. Rather than redesigning the daemon to manage N ledgers (a significant refactor touching `WorkspaceRegistry`, `SyncScheduler`, socket multiplexing, and GC), we keep each daemon 1:1 with a ledger and let the MCP server act as a router.

This is a deliberate scope-reduction choice for v1. The daemon's architecture is well-tested and stable. Introducing multi-ledger support would create new concurrency and lifecycle edge cases (GC on ledger A while syncing ledger B, socket identity with multiple workspaces, heartbeat routing). By reusing the existing daemon unchanged, we get MCP support with minimal regression risk.

If the per-daemon overhead becomes a concern (e.g., user has 20+ repos), a future version can consolidate to a multi-workspace daemon — but we'd rather prove the MCP value proposition first with the simpler model.

### Is stdio transport secure enough?

Yes, for v1. Here's why:

**What stdio provides:** The MCP client (Claude Desktop, Claude Code) spawns `ox mcp serve` as a child process. Communication happens over the child's stdin/stdout — Unix pipes that are only accessible to the parent process and the child. There's no network socket, no port to scan, no ambient authority. An attacker would need local code execution as the same user to intercept the pipe, at which point they already have access to everything the MCP server can read.

**What stdio doesn't provide:** No authentication of the MCP client. Any process that can exec `ox mcp serve` and read its stdio gets full access to the user's team context and murmur capabilities. This is acceptable because the MCP server runs with the user's own credentials — it's equivalent to running `ox query` in a terminal. The threat model is the same as any CLI tool.

**When we'd need more:** If we ever expose the MCP server over a network transport (SSE, WebSocket), we'd need TLS + authentication. That's a v2 concern and out of scope for stdio.

### Why can't the MCP server just be a long-running daemon that Claude spins up?

This is tempting — Claude already knows how to connect to MCP servers as persistent processes, and a daemon model would avoid per-conversation startup costs. But it doesn't work for several reasons:

1. **MCP transport model mismatch.** MCP clients (Claude Desktop, Claude Code) communicate with MCP servers over **stdio** — the client spawns the server process and talks over stdin/stdout. The client manages the process lifecycle. A pre-existing daemon would need a network transport (SSE/WebSocket), but Claude Desktop's MCP client doesn't support connecting to an already-running server — it expects to spawn the process itself.

2. **Credential freshness.** The MCP server needs fresh auth tokens for API calls and credential delivery via heartbeats. A long-running daemon would need its own token refresh loop, duplicating logic that already exists in the per-ledger daemons and the auth store. By running as a spawned process, the MCP server reads credentials from the auth store on startup — always fresh.

3. **Process lifecycle clarity.** If the MCP server were a daemon, who starts it? Who stops it? What happens when the user logs out and back in? With the stdio model, Claude owns the lifecycle — start on conversation open, stop on close. Clean and predictable. A daemon would need its own inactivity timeout, health monitoring, and crash recovery — all of which already exist in the per-ledger daemons we reuse.

4. **ox already has daemon infrastructure.** Rather than building a second daemon system for MCP, we reuse the existing per-ledger daemons for the heavy lifting (sync, credentials, murmurs) and keep the MCP server as a thin stdio translation layer that routes to them. This separation means the MCP server is stateless and disposable — if it crashes, Claude restarts it and it reconnects to the still-running daemons.

---

## Remaining Open Questions

1. **Team-level murmurs:** Timeline for murmurs that are team-scoped rather than ledger-scoped? This would simplify the MCP murmur tool significantly (no ledger disambiguation needed).

2. **Desktop Extension packaging:** Should we package ox as a `.mcpb` Desktop Extension in a future release for one-click Claude Chat install?

3. **Multi-endpoint:** If the user has tokens for multiple endpoints (prod + staging), the bootstrap daemon uses the first. Should there be explicit endpoint selection?

4. **Rate limiting / caching:** Should the MCP server cache team context reads and query results to reduce repeated calls within a conversation?

---

## References

- [MCP Specification](https://spec.modelcontextprotocol.io/)
- [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk)
- [IPC Architecture](ipc-architecture.md)
- [Daemon State Principles](daemon-state-principles.md)
- [Agent UX Principles](agent-ux-principles.md)
