//go:build ledger_twin

package ledger_twin

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

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

// MurmurSpec defines one murmur to generate on disk.
type MurmurSpec struct {
	Dev        Dev
	Timestamp  time.Time
	ID         string // deterministic, e.g. "Mx1001"
	Topic      string
	Importance string // critical, normal, ambient
	Content    string
	Scope      string            // ledger or team (default: ledger)
	Metadata   map[string]string // optional structured context
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
	// Murmur assertions
	MinMurmurs               int
	ExpectedTopics           []string
	ExpectedMurmurImportance string // at least one murmur must have this importance
}

// CartSpec defines one cart to generate on disk.
// Cart IDs embed the session ID for trivial correlation:
//   cart-Ox1001 → session 2026-03-20T10-00-alice-Ox1001 → murmur Mx1001
type CartSpec struct {
	ID          string
	Title       string
	Description string
	Status      string // open, in_progress, closed
	Priority    int
	IssueType   string // task, feature, bug, epic, chore
	Assignee    string
	Creator     string
	CreatedAt   time.Time
	ClosedAt    *time.Time
}

// TwinManifest holds everything: generated sessions + murmurs + carts + validation windows.
type TwinManifest struct {
	LedgerPath string
	Sessions   []SessionSpec
	Murmurs    []MurmurSpec
	Carts      []CartSpec
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

// murmur builds a MurmurSpec with sensible defaults (topic=wip, scope=ledger).
func murmur(dev Dev, t time.Time, id, content string) MurmurSpec {
	return MurmurSpec{Dev: dev, Timestamp: t, ID: id, Topic: "wip", Importance: "normal", Content: content, Scope: "ledger"}
}

// cart builds a CartSpec. ID is "cart-<sessionID>" for trivial correlation.
// Created 5 minutes before session start. Default type=task, priority=2.
func cart(dev Dev, t time.Time, sessionID, title, status string) CartSpec {
	c := CartSpec{
		ID:        "cart-" + sessionID,
		Title:     title,
		Status:    status,
		Priority:  2,
		IssueType: "task",
		Assignee:  dev.Username,
		Creator:   dev.Username,
		CreatedAt: t.Add(-5 * time.Minute),
	}
	if status == "closed" {
		closed := t.Add(30 * time.Minute)
		c.ClosedAt = &closed
	}
	return c
}

// withCartType overrides the issue type on a CartSpec.
func withCartType(c CartSpec, issueType string) CartSpec {
	c.IssueType = issueType
	return c
}

// withCartPriority overrides the priority on a CartSpec.
func withCartPriority(c CartSpec, priority int) CartSpec {
	c.Priority = priority
	return c
}

// withCartDesc adds a description to a CartSpec.
func withCartDesc(c CartSpec, desc string) CartSpec {
	c.Description = desc
	return c
}

// withImportance overrides the importance on a MurmurSpec.
func withImportance(m MurmurSpec, importance string) MurmurSpec {
	m.Importance = importance
	return m
}


// withSummary adds a one-liner summary to a SessionSpec.
func withSummary(s SessionSpec, summary string) SessionSpec {
	s.Summary = summary
	return s
}

// fcEntry is a single file change for building file-change murmur content.
type fcEntry struct {
	path       string // repo-relative
	changeType string // "created", "modified", "deleted"
}

func modified(path string) fcEntry { return fcEntry{path: path, changeType: "modified"} }
func created(path string) fcEntry  { return fcEntry{path: path, changeType: "created"} }
func deleted(path string) fcEntry  { return fcEntry{path: path, changeType: "deleted"} }

// fileChangeMurmur builds a file-change murmur matching the daemon's watchman format.
// agentID is the resolved agent ID ("daemon", single agent, or "Ox1,Ox2").
// principal is the machine owner (from auth.GetUsername).
// branch is the git branch at observation time.
func fileChangeMurmur(t time.Time, id, agentID, principal, branch string, changes ...fcEntry) MurmurSpec {
	paths := make([]string, len(changes))
	for i, c := range changes {
		paths[i] = c.path
	}
	sort.Strings(paths)

	importance := "ambient"
	if len(changes) > 10 {
		importance = "normal"
	}

	content := formatFCContent(changes, branch)

	worktree := "/Users/" + principal + "/src/ox"
	if prefix, ok := devWorktree[principal]; ok {
		worktree = strings.TrimSuffix(prefix, "/")
	}

	return MurmurSpec{
		Dev:        Dev{Username: principal, AgentID: agentID},
		Timestamp:  t,
		ID:         id,
		Topic:      "file-changes",
		Importance: importance,
		Content:    content,
		Scope:      "ledger",
		Metadata: map[string]string{
			"worktree":   worktree,
			"branch":     branch,
			"files":      strings.Join(paths, ","),
			"file_count": fmt.Sprintf("%d", len(changes)),
		},
	}
}

// formatFCContent mimics the daemon's formatFileChangeMurmur output.
// shortChangeType returns compact labels matching daemon's ChangeType.Short().
func shortChangeType(ct string) string {
	switch ct {
	case "created":
		return "new"
	case "modified":
		return "mod"
	case "deleted":
		return "del"
	case "renamed":
		return "mv"
	default:
		return ct
	}
}

func formatFCContent(changes []fcEntry, branch string) string {
	ctx := fmt.Sprintf("[%s] ", branch)
	if len(changes) <= 5 {
		sort.Slice(changes, func(i, j int) bool {
			if changes[i].changeType != changes[j].changeType {
				return changes[i].changeType < changes[j].changeType
			}
			return changes[i].path < changes[j].path
		})
		var parts []string
		var curType string
		for _, c := range changes {
			if c.changeType != curType {
				curType = c.changeType
				parts = append(parts, shortChangeType(c.changeType)+" "+c.path)
			} else {
				parts = append(parts, c.path)
			}
		}
		return ctx + strings.Join(parts, ", ")
	}
	// medium format: group by directory
	type dirSummary struct{ created, modified, deleted int }
	dirs := make(map[string]*dirSummary)
	for _, c := range changes {
		dir := c.path[:strings.LastIndex(c.path, "/")+1]
		if dir == "" {
			dir = "./"
		}
		s, ok := dirs[dir]
		if !ok {
			s = &dirSummary{}
			dirs[dir] = s
		}
		switch c.changeType {
		case "created":
			s.created++
		case "deleted":
			s.deleted++
		default:
			s.modified++
		}
	}
	dirNames := make([]string, 0, len(dirs))
	for d := range dirs {
		dirNames = append(dirNames, d)
	}
	sort.Strings(dirNames)
	var parts []string
	for _, dir := range dirNames {
		s := dirs[dir]
		var counts []string
		if s.modified > 0 {
			counts = append(counts, fmt.Sprintf("%dM", s.modified))
		}
		if s.created > 0 {
			counts = append(counts, fmt.Sprintf("%dA", s.created))
		}
		if s.deleted > 0 {
			counts = append(counts, fmt.Sprintf("%dD", s.deleted))
		}
		parts = append(parts, fmt.Sprintf("%s(%s)", dir, strings.Join(counts, ",")))
	}
	return fmt.Sprintf("%s%d files: %s", ctx, len(changes), strings.Join(parts, " "))
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
	// Murmurs: WIP status updates from AI coworkers
	// Dense, technical one-liners describing current work.
	// ═══════════════════════════════════════════════════════════════

	// Hot Zone murmurs (March 20): devs announcing overlapping work
	m.Murmurs = append(m.Murmurs,
		murmur(alice, ts(20, 10, 5), "Mx1001",
			"Extracting subcommand registration into table-driven pattern in root.go, also touching output.go helpers"),
		murmur(bob, ts(20, 10, 35), "Mx1002",
			"Adding --api-endpoint global flag to root command with validation, plumbing through auth handler"),
		murmur(dave, ts(20, 11, 5), "Mx1003",
			"CLI formatting overhaul: replacing fmt.Printf with structured formatters in output.go"),
		murmur(alice, ts(20, 14, 5), "Mx1004",
			"Auth middleware extraction in progress, pulling into standalone package with session store DI"),
		murmur(bob, ts(20, 15, 5), "Mx1005",
			"Consolidating duplicate route definitions, wiring API routes into root command init"),
	)

	// Parallel Streams murmurs (March 21): quiet, ambient — no overlap
	m.Murmurs = append(m.Murmurs,
		withImportance(murmur(alice, ts(21, 9, 5), "Mx2001",
			"Deep in auth login rewrite: replacing cookie-based with device code flow, adding token refresh on 401"), "ambient"),
		withImportance(murmur(bob, ts(21, 9, 35), "Mx2002",
			"Table-driven tests for all API handler error paths and rate-limit middleware"), "ambient"),
		withImportance(murmur(carol, ts(21, 10, 5), "Mx2003",
			"Switching daemon file watcher from polling to fsnotify, fixing sync pull race"), "ambient"),
	)

	// Pair Convergence murmurs (March 22): two pairs coordinating
	m.Murmurs = append(m.Murmurs,
		murmur(alice, ts(22, 10, 5), "Mx3001",
			"Adding per-user rate limiting to auth middleware using token bucket algorithm"),
		murmur(bob, ts(22, 10, 35), "Mx3002",
			"Instrumenting auth middleware with structured request/response logging via slog"),
		murmur(carol, ts(22, 11, 5), "Mx3003",
			"Wiring daemon health endpoint into ox status output with connection retry"),
		murmur(dave, ts(22, 11, 35), "Mx3004",
			"Adding color-coded status indicators and table layout for ox status"),
	)

	// Sprint End Rush murmurs (March 23): high density, everyone posting
	m.Murmurs = append(m.Murmurs,
		murmur(frank, ts(23, 8, 5), "Mx4001",
			"Rewrote getting started guide for new ox init flow with screenshots"),
		murmur(bob, ts(23, 8, 35), "Mx4002",
			"Updated API examples in getting-started and migrating v1 endpoints to v2 schema"),
		murmur(alice, ts(23, 9, 5), "Mx4003",
			"Implementing automatic token rotation 5min before expiry with background refresh"),
		murmur(carol, ts(23, 9, 35), "Mx4004",
			"Added SIGTERM handler that drains in-flight syncs before daemon exit"),
		murmur(dave, ts(23, 10, 5), "Mx4005",
			"Adding animated progress bars for sync operations using bubbletea"),
		withImportance(murmur(eve, ts(23, 10, 35), "Mx4006",
			"Config tests at 92% coverage including XDG fallback paths and env overrides"), "ambient"),
		murmur(alice, ts(23, 12, 5), "Mx4007",
			"Replaced generic error returns with typed AuthError wrapping for better CLI messages"),
		murmur(bob, ts(23, 16, 5), "Mx4008",
			"Migrated remaining v1 callers to v2 API, added deprecation warnings on v1 routes"),
	)

	// Active Recording murmurs (March 24): in-progress session updates
	m.Murmurs = append(m.Murmurs,
		murmur(alice, ts(24, 14, 5), "Mx5001",
			"Debugging token expiry race where refresh goroutine and request handler both read expiry field"),
		withImportance(murmur(bob, ts(24, 14, 35), "Mx5002",
			"Implementing sliding window rate limiter for API v2 endpoints, still recording"), "ambient"),
		withImportance(murmur(carol, ts(24, 15, 5), "Mx5003",
			"Adding prometheus-style metrics to daemon sync loop and IPC handlers, in progress"), "ambient"),
	)

	// Cluster Bridge murmurs (March 17): carol bridges two clusters
	m.Murmurs = append(m.Murmurs,
		murmur(alice, ts(17, 9, 5), "Mx8001",
			"Implementing background token refresh with mutex-protected expiry check"),
		murmur(bob, ts(17, 10, 5), "Mx8002",
			"Session validation middleware: checking token signature and expiry on every request"),
		murmur(dave, ts(17, 9, 35), "Mx8003",
			"Unified JSON and table output modes behind a single Renderer interface"),
		withImportance(murmur(eve, ts(17, 10, 35), "Mx8004",
			"Building golden-file test helpers for CLI output and snapshot testing"), "ambient"),
		murmur(carol, ts(17, 11, 5), "Mx8005",
			"Connecting auth state to CLI status display via daemon bridge — bridging auth/ and cli/ packages"),
	)

	// Escalation Day 1 murmurs (March 10): low density, calm
	m.Murmurs = append(m.Murmurs,
		withImportance(murmur(alice, ts(10, 9, 5), "MxA001",
			"Splitting monolithic sync pull into fetch/merge/apply stages"), "ambient"),
		withImportance(murmur(carol, ts(10, 10, 5), "MxA002",
			"Adding context-aware error wrapping to pull for transient vs permanent failure distinction"), "ambient"),
	)

	// Escalation Day 2 murmurs (March 11): medium density
	m.Murmurs = append(m.Murmurs,
		murmur(alice, ts(11, 9, 5), "MxA003",
			"Built 3-way merge resolver for session conflicts using manifest-based rules"),
		murmur(carol, ts(11, 9, 35), "MxA004",
			"Sync retry with conflict detection: auto-resolve trivial, flag others for manual review"),
		murmur(bob, ts(11, 10, 5), "MxA005",
			"Wiring sync merge into API layer so remote clients can trigger merge via REST"),
		murmur(dave, ts(11, 11, 5), "MxA006",
			"Adding ox sync --force and --dry-run flags with pull integration"),
	)

	// Escalation Day 3 murmurs (March 12): high density, critical signals
	m.Murmurs = append(m.Murmurs,
		withImportance(murmur(alice, ts(12, 9, 5), "MxA007",
			"Major sync rewrite: replacing ad-hoc sync with state-machine-driven pipeline"), "critical"),
		withImportance(murmur(bob, ts(12, 9, 20), "MxA008",
			"Sync API v2 streaming migration, SSE progress updates"), "critical"),
		murmur(carol, ts(12, 9, 35), "MxA009",
			"Connecting new sync pipeline to daemon scheduler with configurable intervals"),
		murmur(dave, ts(12, 10, 5), "MxA010",
			"Rebuilt ox sync command to show real-time state machine transitions"),
		murmur(eve, ts(12, 10, 35), "MxA011",
			"Full integration test suite for sync pipeline covering all state transitions"),
		murmur(frank, ts(12, 11, 5), "MxA012",
			"Updated sync docs to reflect state machine architecture and new resolve rules"),
		murmur(alice, ts(12, 14, 5), "MxA013",
			"Added conflict-detected and manual-review states to sync state machine"),
		murmur(bob, ts(12, 14, 35), "MxA014",
			"Wired exponential backoff into pull retry path with resolve fallback"),
	)

	// Subagent Excluded murmurs (March 19): only from main session
	m.Murmurs = append(m.Murmurs,
		murmur(alice, ts(19, 10, 5), "MxS001",
			"Planned auth handler refactor: identified 4 extract-method candidates"),
	)

	// ═══════════════════════════════════════════════════════════════
	// File-change murmurs: daemon-emitted watchman observations.
	// These fire ~10 min after the daemon detects filesystem changes,
	// separate from the agent-emitted WIP murmurs above.
	// agent_id is the resolved AI coworker when exactly one is active,
	// comma-separated when multiple, or "daemon" during idle windows.
	// ═══════════════════════════════════════════════════════════════

	// Hot Zone file-changes (March 20): each dev's daemon reports their own FS changes
	m.Murmurs = append(m.Murmurs,
		// alice's machine: her root refactor work
		fileChangeMurmur(ts(20, 10, 15), "Mf1001", "Ox1a2b", "alice", "feature/root-refactor",
			modified("cmd/ox/root.go"), modified("internal/cli/output.go"), modified("internal/auth/handler.go")),
		// bob's machine: his root command flag work
		fileChangeMurmur(ts(20, 10, 45), "Mf1001b", "Ox3c4d", "bob", "feature/root-refactor",
			modified("cmd/ox/root.go"), modified("internal/cli/output.go"), modified("internal/auth/handler.go")),
		// dave's machine: CLI formatting overhaul
		fileChangeMurmur(ts(20, 11, 15), "Mf1002", "Ox7g8h", "dave", "feature/root-refactor",
			modified("cmd/ox/root.go"), modified("internal/cli/output.go"),
			created("internal/cli/format.go")),
		// alice afternoon: auth middleware
		fileChangeMurmur(ts(20, 14, 15), "Mf1003", "Ox1a2b", "alice", "feature/auth-middleware",
			modified("internal/auth/middleware.go"), modified("internal/session/store.go")),
		// bob afternoon: API routes
		fileChangeMurmur(ts(20, 15, 15), "Mf1004", "Ox3c4d", "bob", "feature/api-cleanup",
			modified("internal/api/routes.go"), modified("cmd/ox/root.go")),
	)

	// Parallel Streams file-changes (March 21): isolated work → single agent each
	m.Murmurs = append(m.Murmurs,
		fileChangeMurmur(ts(21, 9, 15), "Mf2001", "Ox1a2b", "alice", "feature/device-code-auth",
			modified("internal/auth/login.go"), modified("internal/auth/token.go")),
		fileChangeMurmur(ts(21, 9, 45), "Mf2002", "Ox3c4d", "bob", "feature/api-tests",
			modified("internal/api/handler.go"), modified("internal/api/middleware.go")),
		fileChangeMurmur(ts(21, 10, 15), "Mf2003", "Ox5e6f", "carol", "fix/daemon-watcher",
			modified("internal/daemon/watcher.go"), modified("internal/sync/pull.go")),
	)

	// Pair Convergence file-changes (March 22): each dev on their own machine
	m.Murmurs = append(m.Murmurs,
		// alice's machine: her auth rate-limiting work
		fileChangeMurmur(ts(22, 10, 45), "Mf3001", "Ox1a2b", "alice", "feature/auth-rate-limit",
			modified("internal/auth/middleware.go"), modified("internal/auth/token.go")),
		// bob's machine: his auth logging work
		fileChangeMurmur(ts(22, 10, 50), "Mf3001b", "Ox3c4d", "bob", "feature/auth-logging",
			modified("internal/auth/middleware.go"), modified("internal/api/client.go")),
		// carol's machine: her status daemon integration
		fileChangeMurmur(ts(22, 11, 45), "Mf3002", "Ox5e6f", "carol", "feature/status-overhaul",
			modified("cmd/ox/status.go"), modified("internal/daemon/health.go")),
		// dave's machine: his status rendering work
		fileChangeMurmur(ts(22, 11, 50), "Mf3002b", "Ox7g8h", "dave", "feature/status-overhaul",
			modified("cmd/ox/status.go"), modified("internal/cli/render.go")),
	)

	// Sprint End Rush file-changes (March 23): high churn, each dev's machine
	m.Murmurs = append(m.Murmurs,
		// frank's machine: docs work
		fileChangeMurmur(ts(23, 8, 45), "Mf4001", "Oxk1l2", "frank", "docs/getting-started",
			modified("docs/getting-started.md"), modified("docs/installation.md")),
		// bob's machine: also docs + API (overlap with frank on getting-started.md)
		fileChangeMurmur(ts(23, 8, 50), "Mf4001b", "Ox3c4d", "bob", "docs/getting-started",
			modified("docs/getting-started.md"), modified("internal/api/v2.go")),
		// alice's machine: auth token rotation
		fileChangeMurmur(ts(23, 9, 45), "Mf4002", "Ox1a2b", "alice", "main",
			modified("internal/auth/token.go"), modified("internal/session/refresh.go")),
		// carol's machine: daemon graceful shutdown
		fileChangeMurmur(ts(23, 9, 50), "Mf4002b", "Ox5e6f", "carol", "main",
			modified("internal/daemon/lifecycle.go"), modified("internal/daemon/signal.go")),
		// dave's machine: 6 files triggers dir grouping format
		fileChangeMurmur(ts(23, 10, 45), "Mf4003", "Ox7g8h", "dave", "feature/cli-polish",
			modified("internal/cli/progress.go"), modified("cmd/ox/sync.go"),
			modified("internal/cli/theme.go"), modified("internal/cli/output.go")),
		// eve's machine: config tests
		fileChangeMurmur(ts(23, 10, 50), "Mf4003b", "Ox9i0j", "eve", "feature/cli-polish",
			created("tests/unit/config_test.go"), modified("internal/config/loader.go")),
		// alice afternoon: auth error types
		fileChangeMurmur(ts(23, 12, 15), "Mf4004", "Ox1a2b", "alice", "feature/auth-errors",
			modified("internal/auth/errors.go"), modified("internal/auth/handler.go")),
		// bob's machine: late push wrapping up
		fileChangeMurmur(ts(23, 16, 15), "Mf4005", "Ox3c4d", "bob", "main",
			modified("internal/api/v2.go"), modified("internal/api/migration.go"),
			modified("cmd/ox/root.go"), modified("cmd/ox/help.go")),
	)

	// Active Recording file-changes (March 24): live sessions → real agent IDs
	m.Murmurs = append(m.Murmurs,
		fileChangeMurmur(ts(24, 14, 15), "Mf5001", "Ox1a2b", "alice", "fix/token-race",
			modified("internal/auth/token.go")),
		fileChangeMurmur(ts(24, 14, 45), "Mf5002", "Ox3c4d", "bob", "feature/rate-limiter",
			modified("internal/api/ratelimit.go")),
		fileChangeMurmur(ts(24, 15, 15), "Mf5003", "Ox5e6f", "carol", "feature/daemon-metrics",
			modified("internal/daemon/metrics.go")),
	)

	// Cluster Bridge file-changes (March 17): carol bridges auth/ and cli/
	m.Murmurs = append(m.Murmurs,
		// alice's machine: auth cluster
		fileChangeMurmur(ts(17, 9, 15), "Mf8001", "Ox1a2b", "alice", "feature/token-refresh",
			modified("internal/auth/token.go"), modified("internal/auth/refresh.go")),
		// bob's machine: also auth, separate checkout
		fileChangeMurmur(ts(17, 10, 10), "Mf8001b", "Ox3c4d", "bob", "feature/token-refresh",
			modified("internal/auth/session.go"), modified("internal/auth/token.go")),
		// dave's machine: cli cluster
		fileChangeMurmur(ts(17, 10, 15), "Mf8002", "Ox7g8h", "dave", "feature/cli-output",
			modified("internal/cli/output.go"), modified("internal/cli/format.go")),
		// eve's machine: also cli
		fileChangeMurmur(ts(17, 10, 40), "Mf8002b", "Ox9i0j", "eve", "feature/cli-output",
			modified("internal/cli/output.go"), created("tests/unit/cli_test.go")),
		// carol bridges both — daemon sees writes to auth/ + cli/ + daemon/
		fileChangeMurmur(ts(17, 11, 15), "Mf8003", "Ox5e6f", "carol", "feature/auth-status-bridge",
			modified("internal/auth/token.go"), modified("internal/cli/output.go"),
			modified("internal/daemon/bridge.go")),
	)

	// Escalation Day 1 file-changes (March 10): calm, few changes
	m.Murmurs = append(m.Murmurs,
		fileChangeMurmur(ts(10, 9, 15), "MfA001", "Ox1a2b", "alice", "refactor/sync-stages",
			modified("internal/sync/pull.go"), modified("internal/sync/merge.go")),
		fileChangeMurmur(ts(10, 10, 15), "MfA002", "Ox5e6f", "carol", "fix/sync-errors",
			modified("internal/sync/pull.go")),
	)

	// Escalation Day 2 file-changes (March 11): density climbing, each dev's machine
	m.Murmurs = append(m.Murmurs,
		// alice's machine: merge resolver
		fileChangeMurmur(ts(11, 9, 45), "MfA003", "Ox1a2b", "alice", "feature/sync-merge",
			modified("internal/sync/pull.go"), modified("internal/sync/merge.go"),
			modified("internal/sync/resolve.go")),
		// carol's machine: retry with conflict detection
		fileChangeMurmur(ts(11, 9, 50), "MfA003b", "Ox5e6f", "carol", "feature/sync-merge",
			modified("internal/sync/pull.go"), modified("internal/sync/resolve.go")),
		// bob's machine: sync API wiring
		fileChangeMurmur(ts(11, 10, 15), "MfA004", "Ox3c4d", "bob", "feature/sync-cli",
			modified("internal/sync/merge.go"), modified("internal/api/sync.go")),
		// dave's machine: sync CLI flags
		fileChangeMurmur(ts(11, 11, 15), "MfA004b", "Ox7g8h", "dave", "feature/sync-cli",
			modified("cmd/ox/sync.go"), modified("internal/sync/pull.go")),
	)

	// Escalation Day 3 file-changes (March 12): everyone in sync/, each dev's machine
	m.Murmurs = append(m.Murmurs,
		// alice's machine: core sync rewrite — 4 sync/ files
		fileChangeMurmur(ts(12, 9, 45), "MfA005", "Ox1a2b", "alice", "feature/sync-rewrite",
			modified("internal/sync/pull.go"), modified("internal/sync/merge.go"),
			modified("internal/sync/resolve.go"), modified("internal/sync/state.go")),
		// bob's machine: sync API + state
		fileChangeMurmur(ts(12, 9, 50), "MfA005b", "Ox3c4d", "bob", "feature/sync-rewrite",
			modified("internal/sync/pull.go"), modified("internal/api/sync.go"),
			modified("internal/sync/state.go")),
		// carol's machine: sync daemon integration
		fileChangeMurmur(ts(12, 9, 55), "MfA005c", "Ox5e6f", "carol", "feature/sync-rewrite",
			modified("internal/sync/pull.go"), modified("internal/sync/merge.go"),
			modified("internal/daemon/sync.go")),
		// dave's machine: sync CLI
		fileChangeMurmur(ts(12, 10, 15), "MfA005d", "Ox7g8h", "dave", "feature/sync-rewrite",
			modified("cmd/ox/sync.go"), modified("internal/sync/state.go"),
			modified("internal/cli/sync.go")),
		// eve's machine: tests
		fileChangeMurmur(ts(12, 10, 45), "MfA006", "Ox9i0j", "eve", "feature/sync-rewrite",
			modified("internal/sync/pull.go"), modified("internal/sync/merge.go"),
			created("tests/integration/sync_test.go")),
		// frank's machine: docs + resolve
		fileChangeMurmur(ts(12, 11, 15), "MfA006b", "Oxk1l2", "frank", "feature/sync-rewrite",
			modified("docs/sync.md"), modified("internal/sync/resolve.go")),
		// alice's machine: afternoon state machine churn
		fileChangeMurmur(ts(12, 14, 45), "MfA007", "Ox1a2b", "alice", "feature/sync-rewrite",
			modified("internal/sync/state.go"), modified("internal/sync/resolve.go")),
		// bob's machine: afternoon backoff work
		fileChangeMurmur(ts(12, 14, 50), "MfA007b", "Ox3c4d", "bob", "feature/sync-rewrite",
			modified("internal/sync/pull.go"), modified("internal/sync/resolve.go")),
	)

	// ═══════════════════════════════════════════════════════════════
	// Carts: planned work items created before sessions start.
	// Cart IDs embed session IDs for trivial correlation:
	//   cart-Ox1001 ↔ session Ox1001 ↔ murmur Mx1001
	// ═══════════════════════════════════════════════════════════════

	// Window 1 carts (Hot Zone, March 20): all completed
	m.Carts = append(m.Carts,
		withCartDesc(cart(alice, ts(20, 10, 0), "Ox1001", "Refactor root command structure", "closed"),
			"Extract subcommand registration into table-driven pattern"),
		withCartDesc(cart(bob, ts(20, 10, 30), "Ox1002", "Add API flag to root command", "closed"),
			"Add --api-endpoint global flag with validation"),
		withCartDesc(withCartType(cart(dave, ts(20, 11, 0), "Ox1003", "CLI formatting overhaul", "closed"), "feature"),
			"Replace ad-hoc fmt.Printf with structured formatters"),
		withCartDesc(cart(alice, ts(20, 14, 0), "Ox1004", "Auth middleware extraction", "closed"),
			"Pull auth middleware into standalone package"),
		withCartDesc(cart(bob, ts(20, 15, 0), "Ox1005", "API route cleanup", "closed"),
			"Consolidate duplicate route definitions"),
	)

	// Window 2 carts (Parallel Streams, March 21): all completed
	m.Carts = append(m.Carts,
		withCartDesc(withCartType(cart(alice, ts(21, 9, 0), "Ox2001", "Auth login flow rewrite", "closed"), "feature"),
			"Replace cookie-based login with device code flow"),
		withCartDesc(cart(bob, ts(21, 9, 30), "Ox2002", "API handler tests", "closed"),
			"Table-driven tests for all API handler error paths"),
		withCartDesc(withCartType(cart(carol, ts(21, 10, 0), "Ox2003", "Daemon watcher improvements", "closed"), "bug"),
			"Switch from polling to fsnotify, fix sync pull race"),
	)

	// Window 3 carts (Pair Convergence, March 22): all completed
	m.Carts = append(m.Carts,
		withCartDesc(cart(alice, ts(22, 10, 0), "Ox3001", "Auth middleware rate limiting", "closed"),
			"Add per-user rate limiting using token bucket"),
		withCartDesc(cart(bob, ts(22, 10, 30), "Ox3002", "Auth middleware logging", "closed"),
			"Instrument with structured request/response logging"),
		withCartDesc(cart(carol, ts(22, 11, 0), "Ox3003", "Status command daemon integration", "closed"),
			"Wire daemon health endpoint into ox status"),
		withCartDesc(cart(dave, ts(22, 11, 30), "Ox3004", "Status command rendering", "closed"),
			"Add color-coded status indicators and table layout"),
		withCartDesc(cart(eve, ts(22, 12, 0), "Ox3005", "Auth integration tests", "closed"),
			"End-to-end auth flow test with mock OAuth server"),
	)

	// Window 4 carts (Sprint End Rush, March 23): all completed
	m.Carts = append(m.Carts,
		withCartDesc(cart(frank, ts(23, 8, 0), "Ox4001", "Getting started guide rewrite", "closed"),
			"Rewrite for new ox init flow with screenshots"),
		withCartDesc(cart(bob, ts(23, 8, 30), "Ox4002", "API docs and getting started updates", "closed"),
			"Update API examples and migrate v1 to v2 schema"),
		withCartDesc(withCartPriority(cart(alice, ts(23, 9, 0), "Ox4003", "Session token rotation", "closed"), 1),
			"Automatic token rotation 5min before expiry"),
		withCartDesc(cart(carol, ts(23, 9, 30), "Ox4004", "Daemon graceful shutdown", "closed"),
			"SIGTERM handler that drains in-flight syncs"),
		withCartDesc(withCartType(cart(dave, ts(23, 10, 0), "Ox4005", "CLI progress bars", "closed"), "feature"),
			"Animated progress bars for sync operations"),
		withCartDesc(cart(eve, ts(23, 10, 30), "Ox4006", "Config test coverage", "closed"),
			"92% coverage including XDG fallback and env overrides"),
		withCartDesc(cart(frank, ts(23, 11, 0), "Ox4007", "Config validation docs", "closed"),
			"Document all config keys with schema validation"),
		withCartDesc(withCartPriority(cart(alice, ts(23, 12, 0), "Ox4008", "Auth error handling", "closed"), 1),
			"Typed AuthError wrapping for better CLI messages"),
		withCartDesc(cart(carol, ts(23, 14, 0), "Ox4009", "Sync retry logic", "closed"),
			"Exponential backoff with jitter for failed git pulls"),
		withCartDesc(cart(eve, ts(23, 14, 30), "Ox4010", "Daemon test harness", "closed"),
			"Test harness for daemon lifecycle with mock FS events"),
		withCartDesc(cart(frank, ts(23, 15, 0), "Ox4011", "FAQ documentation", "closed"),
			"FAQ covering common setup issues"),
		withCartDesc(withCartPriority(cart(alice, ts(23, 16, 0), "Ox4012", "Session store optimization", "closed"), 1),
			"In-memory index for session lookups"),
		withCartDesc(cart(bob, ts(23, 16, 0), "Ox4013", "API v2 migration", "closed"),
			"Migrate remaining v1 callers to v2 API"),
		withCartDesc(cart(dave, ts(23, 17, 0), "Ox4014", "CLI help text polish", "closed"),
			"Rewrite help text with examples and contextual hints"),
		withCartDesc(cart(eve, ts(23, 18, 0), "Ox4015", "Integration test cleanup", "closed"),
			"Deduplicate test fixtures, add cleanup hooks"),
		withCartDesc(withCartType(cart(dave, ts(23, 13, 0), "Ox4016", "CLI color theme support", "closed"), "feature"),
			"Light/dark/auto theme detection and NO_COLOR support"),
	)

	// Window 5 carts (Active Recording, March 24): 1 closed, 2 in_progress
	m.Carts = append(m.Carts,
		withCartDesc(withCartType(cart(alice, ts(24, 14, 0), "Ox5001", "Token expiry investigation", "closed"), "bug"),
			"Debug token expiry race between refresh goroutine and request handler"),
		withCartDesc(cart(bob, ts(24, 14, 30), "Ox5002", "API rate limiter", "in_progress"),
			"Implement sliding window rate limiter for API v2 endpoints"),
		withCartDesc(cart(carol, ts(24, 15, 0), "Ox5003", "Daemon metrics collection", "in_progress"),
			"Add prometheus-style metrics to daemon sync loop"),
	)

	// Window 6 carts (Cluster Bridge, March 17): all completed
	m.Carts = append(m.Carts,
		withCartDesc(cart(alice, ts(17, 9, 0), "Ox8001", "Auth token refresh", "closed"),
			"Background token refresh with mutex-protected expiry check"),
		withCartDesc(cart(bob, ts(17, 10, 0), "Ox8002", "Auth session validation", "closed"),
			"Session validation middleware checking token signature and expiry"),
		withCartDesc(withCartType(cart(dave, ts(17, 9, 30), "Ox8003", "CLI output formatting", "closed"), "feature"),
			"Unified JSON and table output behind Renderer interface"),
		withCartDesc(cart(eve, ts(17, 10, 30), "Ox8004", "CLI test helpers", "closed"),
			"Golden-file test helpers for CLI output and snapshot testing"),
		withCartDesc(withCartPriority(cart(carol, ts(17, 11, 0), "Ox8005", "Auth-to-CLI status bridge", "closed"), 1),
			"Connect auth state to CLI status via daemon bridge"),
	)

	// Window 7 carts (Escalation, March 10-12)
	// Day 1: calm
	m.Carts = append(m.Carts,
		withCartDesc(cart(alice, ts(10, 9, 0), "OxA001", "Sync pull refactor", "closed"),
			"Split monolithic pull into fetch/merge/apply stages"),
		withCartDesc(withCartType(cart(carol, ts(10, 10, 0), "OxA002", "Sync pull error handling", "closed"), "bug"),
			"Context-aware error wrapping for transient vs permanent failures"),
		withCartDesc(cart(bob, ts(10, 11, 0), "OxA003", "API endpoint tests", "closed"),
			"Integration tests for all REST endpoints"),
		withCartDesc(cart(dave, ts(10, 14, 0), "OxA004", "CLI version command", "closed"),
			"Add ox version --json and build metadata output"),
	)
	// Day 2: growing
	m.Carts = append(m.Carts,
		withCartDesc(withCartPriority(cart(alice, ts(11, 9, 0), "OxA005", "Sync merge conflict resolution", "closed"), 1),
			"3-way merge resolver using manifest-based rules"),
		withCartDesc(cart(carol, ts(11, 9, 30), "OxA006", "Sync pull retry logic", "closed"),
			"Retry with conflict detection and auto-resolve"),
		withCartDesc(cart(bob, ts(11, 10, 0), "OxA007", "Sync API integration", "closed"),
			"Wire sync merge into API for remote trigger"),
		withCartDesc(cart(dave, ts(11, 11, 0), "OxA008", "Sync CLI commands", "closed"),
			"Add ox sync --force and --dry-run flags"),
		withCartDesc(cart(eve, ts(11, 14, 0), "OxA009", "Sync test fixtures", "closed"),
			"Pre-built conflict scenarios for merge testing"),
		withCartDesc(cart(frank, ts(11, 15, 0), "OxA010", "Sync documentation", "closed"),
			"Architecture docs with Mermaid diagrams"),
	)
	// Day 3: everyone converges
	m.Carts = append(m.Carts,
		withCartDesc(withCartType(withCartPriority(cart(alice, ts(12, 9, 0), "OxA011", "Sync core rewrite", "closed"), 0), "epic"),
			"Replace ad-hoc sync with state-machine-driven pipeline"),
		withCartDesc(cart(bob, ts(12, 9, 15), "OxA012", "Sync API v2", "closed"),
			"Streaming progress updates via SSE"),
		withCartDesc(cart(carol, ts(12, 9, 30), "OxA013", "Sync daemon integration", "closed"),
			"Connect new sync pipeline to daemon scheduler"),
		withCartDesc(cart(dave, ts(12, 10, 0), "OxA014", "Sync CLI overhaul", "closed"),
			"Show real-time state machine transitions"),
		withCartDesc(cart(eve, ts(12, 10, 30), "OxA015", "Sync test suite", "closed"),
			"Full integration test for all state transitions"),
		withCartDesc(cart(frank, ts(12, 11, 0), "OxA016", "Sync docs update", "closed"),
			"Update docs for state machine architecture"),
		withCartDesc(cart(alice, ts(12, 14, 0), "OxA017", "Sync state machine", "closed"),
			"Add conflict-detected and manual-review states"),
		withCartDesc(cart(bob, ts(12, 14, 30), "OxA018", "Sync retry backoff", "closed"),
			"Exponential backoff in pull retry with resolve fallback"),
		withCartDesc(cart(carol, ts(12, 15, 0), "OxA019", "Sync manifest", "closed"),
			"Manifest-driven merge rules for auto-resolve"),
		withCartDesc(withCartType(cart(dave, ts(12, 16, 0), "OxA020", "Sync progress reporting", "closed"), "feature"),
			"State machine emits progress events for CLI rendering"),
	)

	// Subagent carts (March 19): only the main session gets a cart
	m.Carts = append(m.Carts,
		withCartDesc(cart(alice, ts(19, 10, 0), "OxS001", "Auth refactor planning", "closed"),
			"Plan auth handler refactor, identify extract-method candidates"),
		withCartDesc(cart(bob, ts(19, 11, 0), "OxS004", "API handler update", "closed"),
			"Fix nil pointer in API handler for missing auth token"),
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
			MinMurmurs:    10,
			ExpectedTopics: []string{"wip", "file-changes"},
		},
		{
			Name:         "parallel_streams",
			Since:        ts(21, 9, 0),
			Until:        ts(21, 17, 0),
			MinSessions:  3,
			MinAuthors:   3,
			MinConflicts: 0,
			MaxConflicts: 0,
			MinMurmurs:   6,
			ExpectedTopics:           []string{"wip", "file-changes"},
			ExpectedMurmurImportance: "ambient",
		},
		{
			Name:          "pair_convergence",
			Since:         ts(22, 10, 0),
			Until:         ts(22, 16, 0),
			MinSessions:   5,
			MinAuthors:    4,
			MinConflicts:  2,
			MaxConflicts:  2,
			ExpectedPairs: []string{"alice|bob", "carol|dave"},
			MinMurmurs:    8,
			ExpectedTopics: []string{"wip", "file-changes"},
		},
		{
			Name:         "sprint_end_rush",
			Since:        ts(23, 8, 0),
			Until:        ts(23, 20, 0),
			MinSessions:  15,
			MinAuthors:   6,
			MinConflicts: 1,
			MaxConflicts: -1,
			MinMurmurs:   16,
			ExpectedTopics: []string{"wip", "file-changes"},
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
			MinMurmurs:        6,
			ExpectedTopics:    []string{"wip", "file-changes"},
		},
		{
			Name:         "cluster_bridge",
			Since:        ts(17, 9, 0),
			Until:        ts(17, 17, 0),
			MinSessions:  5,
			MinAuthors:   5,
			MinConflicts: 2,
			MaxConflicts: -1,
			MinMurmurs:   8,
			ExpectedTopics: []string{"wip", "file-changes"},
		},
		{
			Name:         "escalation_day1",
			Since:        ts(10, 0, 0),
			Until:        ts(10, 23, 59),
			MinSessions:  4,
			MinAuthors:   4,
			MinConflicts: 1,
			MaxConflicts: 1,
			MinMurmurs:   4,
			ExpectedTopics:           []string{"wip", "file-changes"},
			ExpectedMurmurImportance: "ambient",
		},
		{
			Name:         "escalation_day2",
			Since:        ts(11, 0, 0),
			Until:        ts(11, 23, 59),
			MinSessions:  6,
			MinAuthors:   6,
			MinConflicts: 3,
			MaxConflicts: -1,
			MinMurmurs:   8,
			ExpectedTopics: []string{"wip", "file-changes"},
		},
		{
			Name:         "escalation_day3",
			Since:        ts(12, 0, 0),
			Until:        ts(12, 23, 59),
			MinSessions:  10,
			MinAuthors:   6,
			MinConflicts: 4,
			MaxConflicts: -1,
			MinMurmurs:   16,
			ExpectedTopics:           []string{"wip", "file-changes"},
			ExpectedMurmurImportance: "critical",
		},
		{
			Name:         "subagent_excluded",
			Since:        ts(19, 10, 0),
			Until:        ts(19, 12, 0),
			MinSessions:  2,
			MinAuthors:   2,
			MinConflicts: 0,
			MaxConflicts: 0,
			MinMurmurs:   1,
			ExpectedTopics: []string{"wip"}, // no file-changes: subagent window is short, no daemon cycle
		},
	}

	return m
}
