//go:build ledger_twin

package ledger_twin

import "time"

// Dev represents a simulated team member.
type Dev struct {
	Username string
	Email    string
	AgentID  string
}

// FileTouch represents a single file edit in a session.
type FileTouch struct {
	AbsPath  string // must contain /cmd/, /internal/, etc. for normalization
	ToolName string // Edit, Write, or MultiEdit
}

// SessionSpec defines one session to generate on disk.
type SessionSpec struct {
	Dev        Dev
	Timestamp  time.Time
	SessionID  string
	Title      string
	Summary    string // one-liner: what happened in this session
	Files      []FileTouch
	Recording  bool // if true, write .recording.json
	IsSubagent bool // if true, mark as subagent session
	ParentPID  int  // parent PID for subagent sessions
}

// Window defines a time range with expected assertions.
type Window struct {
	Name              string
	Since             time.Time
	Until             time.Time
	MinSessions       int
	MinAuthors        int
	MinConflicts      int
	MaxConflicts      int // -1 = no upper bound
	ExpectedPairs     []string
	ExpectedRecording int
}

// TwinManifest holds everything: generated sessions + validation windows.
type TwinManifest struct {
	LedgerPath string
	Sessions   []SessionSpec
	Windows    []Window
}

var (
	alice = Dev{Username: "alice", Email: "alice@team.dev", AgentID: "Ox1a2b"}
	bob   = Dev{Username: "bob", Email: "bob@team.dev", AgentID: "Ox3c4d"}
	carol = Dev{Username: "carol", Email: "carol@team.dev", AgentID: "Ox5e6f"}
	dave  = Dev{Username: "dave", Email: "dave@team.dev", AgentID: "Ox7g8h"}
	eve   = Dev{Username: "eve", Email: "eve@team.dev", AgentID: "Ox9i0j"}
	frank = Dev{Username: "frank", Email: "frank@team.dev", AgentID: "Oxk1l2"}
)

// Worktree prefixes — mimic real team directory structures.
// normalizePath strips everything before /cmd/, /internal/, /tests/, etc.
// so these all normalize to the same repo-relative paths.
var devWorktree = map[string]string{
	"alice": "/Users/alice/conductor/workspaces/ox/sprint-7/",
	"bob":   "/Users/bob/Documents/Code/sageox/ox/",
	"carol": "/home/carol/src/github.com/sageox/ox/",
	"dave":  "/home/dave/src/ox/",
	"eve":   "/Users/eve/conductor/workspaces/ox/main/",
	"frank": "/home/frank/projects/sageox/ox/",
}

// fp builds an absolute path using the dev's worktree prefix.
// Falls back to a generic prefix if dev not in map (shouldn't happen).
func fp(dev, repoRelative string) string {
	prefix, ok := devWorktree[dev]
	if !ok {
		prefix = "/home/dev/project/"
	}
	return prefix + repoRelative
}

// edit and write use a placeholder prefix; sess() rewrites them with the dev's real worktree.
func edit(path string) FileTouch  { return FileTouch{AbsPath: path, ToolName: "Edit"} }
func write(path string) FileTouch { return FileTouch{AbsPath: path, ToolName: "Write"} }

// ts creates a local time on the given March 2026 day at hour:minute.
func ts(day, hour, minute int) time.Time {
	return time.Date(2026, time.March, day, hour, minute, 0, 0, time.Local)
}

// sess builds a SessionSpec, injecting the dev's worktree prefix into each file path.
func sess(dev Dev, t time.Time, id, title string, files ...FileTouch) SessionSpec {
	resolved := make([]FileTouch, len(files))
	for i, f := range files {
		resolved[i] = FileTouch{AbsPath: fp(dev.Username, f.AbsPath), ToolName: f.ToolName}
	}
	return SessionSpec{Dev: dev, Timestamp: t, SessionID: id, Title: title, Files: resolved}
}

// withSummary adds a one-liner summary to a SessionSpec.
func withSummary(s SessionSpec, summary string) SessionSpec {
	s.Summary = summary
	return s
}

// BuildManifest returns the full scenario definition.
func BuildManifest() *TwinManifest {
	m := &TwinManifest{}

	// ═══════════════════════════════════════════════════════════════
	// Window 1: Hot Zone (March 20, 10:00-18:00)
	// 3-way overlap on root.go + output.go, 2-way on handler.go
	// ═══════════════════════════════════════════════════════════════
	m.Sessions = append(m.Sessions,
		withSummary(sess(alice, ts(20, 10, 0), "Ox1001", "Refactor root command structure",
			edit("cmd/ox/root.go"), edit("internal/cli/output.go"), edit("internal/auth/handler.go")),
			"Extracted subcommand registration into a table-driven pattern and unified output helpers across all commands"),
		withSummary(sess(bob, ts(20, 10, 30), "Ox1002", "Add API flag to root command",
			edit("cmd/ox/root.go"), edit("internal/cli/output.go"), edit("internal/auth/handler.go")),
			"Added --api-endpoint global flag to root command with validation and plumbed it through auth handler"),
		withSummary(sess(dave, ts(20, 11, 0), "Ox1003", "CLI formatting overhaul",
			edit("cmd/ox/root.go"), edit("internal/cli/output.go"), write("internal/cli/format.go")),
			"Replaced ad-hoc fmt.Printf calls with structured table/list formatters and added color support"),
		withSummary(sess(alice, ts(20, 14, 0), "Ox1004", "Auth middleware extraction",
			edit("internal/auth/middleware.go"), edit("internal/session/store.go")),
			"Pulled auth middleware into standalone package with session store dependency injection"),
		withSummary(sess(bob, ts(20, 15, 0), "Ox1005", "API route cleanup",
			edit("internal/api/routes.go"), edit("cmd/ox/root.go")),
			"Consolidated duplicate route definitions and wired API routes into root command init"),
	)

	// ═══════════════════════════════════════════════════════════════
	// Window 2: Parallel Streams (March 21, 09:00-17:00)
	// Everyone in isolated areas — zero conflicts
	// ═══════════════════════════════════════════════════════════════
	m.Sessions = append(m.Sessions,
		withSummary(sess(alice, ts(21, 9, 0), "Ox2001", "Auth login flow rewrite",
			edit("internal/auth/login.go"), edit("internal/auth/token.go")),
			"Replaced cookie-based login with device code flow and added token refresh on 401"),
		withSummary(sess(bob, ts(21, 9, 30), "Ox2002", "API handler tests",
			edit("internal/api/handler.go"), edit("internal/api/middleware.go")),
			"Added table-driven tests for all API handler error paths and rate-limit middleware"),
		withSummary(sess(carol, ts(21, 10, 0), "Ox2003", "Daemon watcher improvements",
			edit("internal/daemon/watcher.go"), edit("internal/sync/pull.go")),
			"Switched file watcher from polling to fsnotify and fixed sync pull race on large repos"),
	)

	// ═══════════════════════════════════════════════════════════════
	// Window 3: Pair Convergence (March 22, 10:00-16:00)
	// alice+bob on middleware.go, carol+dave on status.go, eve alone
	// ═══════════════════════════════════════════════════════════════
	m.Sessions = append(m.Sessions,
		withSummary(sess(alice, ts(22, 10, 0), "Ox3001", "Auth middleware rate limiting",
			edit("internal/auth/middleware.go"), edit("internal/auth/token.go")),
			"Added per-user rate limiting to auth middleware using token bucket algorithm"),
		withSummary(sess(bob, ts(22, 10, 30), "Ox3002", "Auth middleware logging",
			edit("internal/auth/middleware.go"), edit("internal/api/client.go")),
			"Instrumented auth middleware with structured request/response logging via slog"),
		withSummary(sess(carol, ts(22, 11, 0), "Ox3003", "Status command daemon integration",
			edit("cmd/ox/status.go"), edit("internal/daemon/health.go")),
			"Wired daemon health endpoint into ox status output with connection retry"),
		withSummary(sess(dave, ts(22, 11, 30), "Ox3004", "Status command rendering",
			edit("cmd/ox/status.go"), edit("internal/cli/render.go")),
			"Added color-coded status indicators and table layout for ox status output"),
		withSummary(sess(eve, ts(22, 12, 0), "Ox3005", "Auth integration tests",
			write("tests/integration/auth_test.go")),
			"Created end-to-end auth flow test with mock OAuth server and token validation"),
	)

	// ═══════════════════════════════════════════════════════════════
	// Window 4: Sprint End Rush (March 23, 08:00-20:00)
	// 16 sessions, all 6 devs, frank+bob overlap on docs
	// ═══════════════════════════════════════════════════════════════
	m.Sessions = append(m.Sessions,
		withSummary(sess(frank, ts(23, 8, 0), "Ox4001", "Getting started guide rewrite",
			write("docs/getting-started.md"), edit("docs/installation.md")),
			"Rewrote getting started guide for new ox init flow with screenshots"),
		withSummary(sess(bob, ts(23, 8, 30), "Ox4002", "API docs and getting started updates",
			edit("docs/getting-started.md"), edit("internal/api/v2.go")),
			"Updated API examples in getting started and migrated v1 endpoints to v2 schema"),
		withSummary(sess(alice, ts(23, 9, 0), "Ox4003", "Session token rotation",
			edit("internal/auth/token.go"), edit("internal/session/refresh.go")),
			"Implemented automatic token rotation 5 minutes before expiry with background refresh"),
		withSummary(sess(carol, ts(23, 9, 30), "Ox4004", "Daemon graceful shutdown",
			edit("internal/daemon/lifecycle.go"), edit("internal/daemon/signal.go")),
			"Added SIGTERM handler that drains in-flight syncs before daemon exit"),
		withSummary(sess(dave, ts(23, 10, 0), "Ox4005", "CLI progress bars",
			edit("internal/cli/progress.go"), edit("cmd/ox/sync.go")),
			"Added animated progress bars for sync operations using bubbletea"),
		withSummary(sess(eve, ts(23, 10, 30), "Ox4006", "Config test coverage",
			write("tests/unit/config_test.go"), edit("internal/config/loader.go")),
			"Achieved 92% coverage on config loader including XDG fallback paths and env overrides"),
		withSummary(sess(frank, ts(23, 11, 0), "Ox4007", "Config validation docs",
			edit("docs/configuration.md"), edit("internal/config/validate.go")),
			"Documented all config keys with examples and added schema validation on load"),
		withSummary(sess(alice, ts(23, 12, 0), "Ox4008", "Auth error handling",
			edit("internal/auth/errors.go"), edit("internal/auth/handler.go")),
			"Replaced generic error returns with typed AuthError wrapping for better CLI messages"),
		withSummary(sess(carol, ts(23, 14, 0), "Ox4009", "Sync retry logic",
			edit("internal/sync/retry.go"), edit("internal/sync/pull.go")),
			"Added exponential backoff with jitter for failed git pulls, max 3 retries"),
		withSummary(sess(eve, ts(23, 14, 30), "Ox4010", "Daemon test harness",
			write("tests/unit/daemon_test.go"), edit("internal/daemon/watcher.go")),
			"Built test harness for daemon lifecycle tests with mock filesystem events"),
		withSummary(sess(frank, ts(23, 15, 0), "Ox4011", "FAQ documentation",
			write("docs/faq.md")),
			"Created FAQ covering common setup issues: auth failures, proxy config, WSL paths"),
		withSummary(sess(alice, ts(23, 16, 0), "Ox4012", "Session store optimization",
			edit("internal/session/store.go"), edit("internal/session/index.go")),
			"Added in-memory index for session lookups, reduced ListSessionsSince from 200ms to 8ms"),
		withSummary(sess(bob, ts(23, 16, 0), "Ox4013", "API v2 migration",
			edit("internal/api/v2.go"), edit("internal/api/migration.go")),
			"Migrated remaining v1 callers to v2 API and added deprecation warnings on v1 routes"),
		withSummary(sess(dave, ts(23, 17, 0), "Ox4014", "CLI help text polish",
			edit("cmd/ox/root.go"), edit("cmd/ox/help.go")),
			"Rewrote help text for all top-level commands with examples and contextual hints"),
		withSummary(sess(eve, ts(23, 18, 0), "Ox4015", "Integration test cleanup",
			edit("tests/integration/auth_test.go"), edit("tests/integration/sync_test.go")),
			"Deduplicated test fixtures and added cleanup hooks to prevent leaked temp dirs"),
		withSummary(sess(dave, ts(23, 13, 0), "Ox4016", "CLI color theme support",
			edit("internal/cli/theme.go"), edit("internal/cli/output.go")),
			"Added light/dark/auto theme detection and NO_COLOR env var support"),
	)

	// ═══════════════════════════════════════════════════════════════
	// Window 5: Active Recording (March 24, 14:00+)
	// 1 completed + 2 actively recording
	// ═══════════════════════════════════════════════════════════════
	m.Sessions = append(m.Sessions,
		withSummary(sess(alice, ts(24, 14, 0), "Ox5001", "Token expiry investigation",
			edit("internal/auth/token.go")),
			"Debugged token expiry race where refresh goroutine and request handler both read expiry field"),
		SessionSpec{Dev: bob, Timestamp: ts(24, 14, 30), SessionID: "Ox5002",
			Title: "API rate limiter", Summary: "Implementing sliding window rate limiter for API v2 endpoints",
			Recording: true, Files: []FileTouch{{AbsPath: fp("bob", "internal/api/ratelimit.go"), ToolName: "Edit"}}},
		SessionSpec{Dev: carol, Timestamp: ts(24, 15, 0), SessionID: "Ox5003",
			Title: "Daemon metrics collection", Summary: "Adding prometheus-style metrics to daemon sync loop and IPC handlers",
			Recording: true, Files: []FileTouch{{AbsPath: fp("carol", "internal/daemon/metrics.go"), ToolName: "Edit"}}},
	)

	// ═══════════════════════════════════════════════════════════════
	// Window 6: Cluster Bridge (March 17, 09:00-17:00)
	// Two clusters: {alice,bob} on auth/ and {dave,eve} on cli/.
	// carol is the only dev touching both clusters.
	// ═══════════════════════════════════════════════════════════════
	m.Sessions = append(m.Sessions,
		withSummary(sess(alice, ts(17, 9, 0), "Ox8001", "Auth token refresh",
			edit("internal/auth/token.go"), edit("internal/auth/refresh.go")),
			"Implemented background token refresh with mutex-protected expiry check"),
		withSummary(sess(bob, ts(17, 10, 0), "Ox8002", "Auth session validation",
			edit("internal/auth/session.go"), edit("internal/auth/token.go")),
			"Added session validation middleware that checks token signature and expiry on every request"),
		withSummary(sess(dave, ts(17, 9, 30), "Ox8003", "CLI output formatting",
			edit("internal/cli/output.go"), edit("internal/cli/format.go")),
			"Unified JSON and table output modes behind a single Renderer interface"),
		withSummary(sess(eve, ts(17, 10, 30), "Ox8004", "CLI test helpers",
			edit("internal/cli/output.go"), write("tests/unit/cli_test.go")),
			"Created golden-file test helpers for CLI output and snapshot testing"),
		withSummary(sess(carol, ts(17, 11, 0), "Ox8005", "Auth-to-CLI status bridge",
			edit("internal/auth/token.go"), edit("internal/cli/output.go"),
			edit("internal/daemon/bridge.go")),
			"Connected auth state to CLI status display via daemon bridge so ox status shows login state"),
	)

	// ═══════════════════════════════════════════════════════════════
	// Window 7: Escalation (March 10-12, full days)
	// Day 1: 1 conflict. Day 2: 3 conflicts. Day 3: 4+ conflicts.
	// Conflict density increasing = escalation pattern.
	// ═══════════════════════════════════════════════════════════════

	// Day 1 (March 10): 4 sessions, 1 conflict (sync/pull.go: alice+carol)
	m.Sessions = append(m.Sessions,
		withSummary(sess(alice, ts(10, 9, 0), "OxA001", "Sync pull refactor",
			edit("internal/sync/pull.go"), edit("internal/sync/merge.go")),
			"Split monolithic pull function into fetch/merge/apply stages"),
		withSummary(sess(carol, ts(10, 10, 0), "OxA002", "Sync pull error handling",
			edit("internal/sync/pull.go")),
			"Added context-aware error wrapping to pull so daemon can distinguish transient vs permanent failures"),
		withSummary(sess(bob, ts(10, 11, 0), "OxA003", "API endpoint tests",
			edit("internal/api/endpoints.go")),
			"Added integration tests for all REST endpoints with httptest server"),
		withSummary(sess(dave, ts(10, 14, 0), "OxA004", "CLI version command",
			edit("cmd/ox/version.go")),
			"Added ox version --json and build metadata output"),
	)

	// Day 2 (March 11): 6 sessions, 3 conflicts
	m.Sessions = append(m.Sessions,
		withSummary(sess(alice, ts(11, 9, 0), "OxA005", "Sync merge conflict resolution",
			edit("internal/sync/pull.go"), edit("internal/sync/merge.go"), edit("internal/sync/resolve.go")),
			"Built 3-way merge resolver for session conflicts using manifest-based rules"),
		withSummary(sess(carol, ts(11, 9, 30), "OxA006", "Sync pull retry logic",
			edit("internal/sync/pull.go"), edit("internal/sync/resolve.go")),
			"Added retry with conflict detection — auto-resolve trivial conflicts, flag others for manual review"),
		withSummary(sess(bob, ts(11, 10, 0), "OxA007", "Sync API integration",
			edit("internal/sync/merge.go"), edit("internal/api/sync.go")),
			"Wired sync merge into API layer so remote clients can trigger merge via REST"),
		withSummary(sess(dave, ts(11, 11, 0), "OxA008", "Sync CLI commands",
			edit("cmd/ox/sync.go"), edit("internal/sync/pull.go")),
			"Added ox sync --force and ox sync --dry-run flags with pull integration"),
		withSummary(sess(eve, ts(11, 14, 0), "OxA009", "Sync test fixtures",
			write("tests/unit/sync_test.go")),
			"Created sync test fixtures with pre-built conflict scenarios for merge testing"),
		withSummary(sess(frank, ts(11, 15, 0), "OxA010", "Sync documentation",
			write("docs/sync.md")),
			"Documented sync architecture with Mermaid diagrams showing pull/merge/push flow"),
	)

	// Day 3 (March 12): 10 sessions, everyone converges on sync/
	m.Sessions = append(m.Sessions,
		withSummary(sess(alice, ts(12, 9, 0), "OxA011", "Sync core rewrite",
			edit("internal/sync/pull.go"), edit("internal/sync/merge.go"),
			edit("internal/sync/resolve.go"), edit("internal/sync/state.go")),
			"Major rewrite: replaced ad-hoc sync with state-machine-driven pipeline"),
		withSummary(sess(bob, ts(12, 9, 15), "OxA012", "Sync API v2",
			edit("internal/sync/pull.go"), edit("internal/api/sync.go"),
			edit("internal/sync/state.go")),
			"Migrated sync API to v2 with streaming progress updates via SSE"),
		withSummary(sess(carol, ts(12, 9, 30), "OxA013", "Sync daemon integration",
			edit("internal/sync/pull.go"), edit("internal/sync/merge.go"),
			edit("internal/daemon/sync.go")),
			"Connected new sync pipeline to daemon scheduler with configurable intervals"),
		withSummary(sess(dave, ts(12, 10, 0), "OxA014", "Sync CLI overhaul",
			edit("cmd/ox/sync.go"), edit("internal/sync/state.go"),
			edit("internal/cli/sync.go")),
			"Rebuilt ox sync command to show real-time state machine transitions"),
		withSummary(sess(eve, ts(12, 10, 30), "OxA015", "Sync test suite",
			edit("internal/sync/pull.go"), edit("internal/sync/merge.go"),
			write("tests/integration/sync_test.go")),
			"Full integration test suite for sync pipeline covering all state transitions"),
		withSummary(sess(frank, ts(12, 11, 0), "OxA016", "Sync docs update",
			edit("docs/sync.md"), edit("internal/sync/resolve.go")),
			"Updated sync docs to reflect state machine architecture and new resolve rules"),
		withSummary(sess(alice, ts(12, 14, 0), "OxA017", "Sync state machine",
			edit("internal/sync/state.go"), edit("internal/sync/resolve.go")),
			"Added conflict-detected and manual-review states to sync state machine"),
		withSummary(sess(bob, ts(12, 14, 30), "OxA018", "Sync retry backoff",
			edit("internal/sync/pull.go"), edit("internal/sync/resolve.go")),
			"Wired exponential backoff into pull retry path with resolve fallback"),
		withSummary(sess(carol, ts(12, 15, 0), "OxA019", "Sync manifest",
			edit("internal/sync/state.go"), edit("internal/sync/merge.go")),
			"Added manifest-driven merge rules so sync knows which files can auto-resolve"),
		withSummary(sess(dave, ts(12, 16, 0), "OxA020", "Sync progress reporting",
			edit("internal/sync/state.go"), edit("internal/cli/sync.go")),
			"State machine emits progress events that CLI renders as animated status line"),
	)

	// ═══════════════════════════════════════════════════════════════
	// Subagent sessions (March 19)
	// Test --include-subagents filtering
	// ═══════════════════════════════════════════════════════════════
	m.Sessions = append(m.Sessions,
		withSummary(sess(alice, ts(19, 10, 0), "OxS001", "Auth refactor planning",
			edit("internal/auth/handler.go")),
			"Planned auth handler refactor: identified 4 extract-method candidates"),
		SessionSpec{
			Dev: alice, Timestamp: ts(19, 10, 15), SessionID: "OxS002",
			Title: "Subagent: explore auth patterns", Summary: "Searched codebase for auth middleware patterns to inform refactor",
			IsSubagent: true, ParentPID: 12345,
			Files: []FileTouch{{AbsPath: fp("alice", "internal/auth/patterns.go"), ToolName: "Edit"}},
		},
		SessionSpec{
			Dev: alice, Timestamp: ts(19, 10, 30), SessionID: "OxS003",
			Title: "Subagent: test generation", Summary: "Generated table-driven tests for auth handler extract candidates",
			IsSubagent: true, ParentPID: 12345,
			Files: []FileTouch{{AbsPath: fp("alice", "tests/unit/auth_test.go"), ToolName: "Write"}},
		},
		withSummary(sess(bob, ts(19, 11, 0), "OxS004", "API handler update",
			edit("internal/api/handler.go")),
			"Fixed nil pointer in API handler when auth token is missing from request context"),
	)

	// ═══════════════════════════════════════════════════════════════
	// Windows (assertion targets)
	// ═══════════════════════════════════════════════════════════════
	m.Windows = []Window{
		{
			Name:          "hot_zone",
			Since:         ts(20, 10, 0),
			Until:         ts(20, 18, 0),
			MinSessions:   5,
			MinAuthors:    3,
			MinConflicts:  3,
			MaxConflicts:  -1,
			ExpectedPairs: []string{"alice|bob", "alice|dave", "bob|dave"},
		},
		{
			Name:         "parallel_streams",
			Since:        ts(21, 9, 0),
			Until:        ts(21, 17, 0),
			MinSessions:  3,
			MinAuthors:   3,
			MinConflicts: 0,
			MaxConflicts: 0,
		},
		{
			Name:          "pair_convergence",
			Since:         ts(22, 10, 0),
			Until:         ts(22, 16, 0),
			MinSessions:   5,
			MinAuthors:    5,
			MinConflicts:  2,
			MaxConflicts:  2,
			ExpectedPairs: []string{"alice|bob", "carol|dave"},
		},
		{
			Name:         "sprint_end_rush",
			Since:        ts(23, 8, 0),
			Until:        ts(23, 20, 0),
			MinSessions:  15,
			MinAuthors:   6,
			MinConflicts: 1,
			MaxConflicts: -1,
		},
		{
			Name:              "active_recording",
			Since:             ts(24, 14, 0),
			Until:             time.Time{},
			MinSessions:       3,
			MinAuthors:        3,
			MinConflicts:      0,
			MaxConflicts:      -1,
			ExpectedRecording: 2,
		},
		{
			Name:         "cluster_bridge",
			Since:        ts(17, 9, 0),
			Until:        ts(17, 17, 0),
			MinSessions:  5,
			MinAuthors:   5,
			MinConflicts: 2,
			MaxConflicts: -1,
		},
		{
			Name:         "escalation_day1",
			Since:        ts(10, 0, 0),
			Until:        ts(10, 23, 59),
			MinSessions:  4,
			MinAuthors:   4,
			MinConflicts: 1,
			MaxConflicts: 1,
		},
		{
			Name:         "escalation_day2",
			Since:        ts(11, 0, 0),
			Until:        ts(11, 23, 59),
			MinSessions:  6,
			MinAuthors:   6,
			MinConflicts: 3,
			MaxConflicts: -1,
		},
		{
			Name:         "escalation_day3",
			Since:        ts(12, 0, 0),
			Until:        ts(12, 23, 59),
			MinSessions:  10,
			MinAuthors:   6,
			MinConflicts: 4,
			MaxConflicts: -1,
		},
		{
			Name:         "subagent_excluded",
			Since:        ts(19, 10, 0),
			Until:        ts(19, 12, 0),
			MinSessions:  2,
			MinAuthors:   2,
			MinConflicts: 0,
			MaxConflicts: 0,
		},
	}

	return m
}
