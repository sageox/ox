package useragent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/sageox/ox/internal/observability"
	"github.com/sageox/ox/internal/version"
)

var (
	mu               sync.RWMutex
	agentType        string
	agentVersion     string
	orchestratorType string
	repoID           string
	daemonMode       bool
	cached           string
	daemonStr        string
)

// HeaderRepoID carries the SageOx repo ID of the project this invocation is
// working in.
//
// The server records CLI usage in cli_activity, but it can only derive a repo
// from routes whose URL embeds one — of the tracked CLI routes, only
// GET /api/v1/cli/repos/{repo_id} does. The repo list, doctor context,
// friction, and init all record NULL, so per-repo activity analytics see a
// small and unrepresentative slice of real usage. The CLI knows the answer on
// every invocation, so it puts it on the wire and the server falls back to it
// when the path has no repo.
const HeaderRepoID = "X-Sageox-Repo-Id"

// repoIDRe bounds what may be emitted in HeaderRepoID. The value comes from
// .sageox/config.json, which a user can hand-edit, so it is validated rather
// than trusted: reject anything with control characters, whitespace, or
// separators that could split a header. Mirrors the character-class posture
// the server applies to User-Agent metadata fields.
var repoIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func init() {
	daemonStr = fmt.Sprintf("ox-daemon/%s (%s; %s)", version.Version, runtime.GOOS, runtime.GOARCH)
}

// SetAgentType records the detected coding agent environment (e.g. "claude-code", "cursor").
// Thread-safe. First write wins; subsequent calls are no-ops.
func SetAgentType(at string) {
	if at == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if agentType != "" {
		return
	}
	agentType = at
	cached = ""
}

// SetAgentVersion records the coding agent version (e.g. "1.0.26").
// Thread-safe. First write wins; subsequent calls are no-ops.
// Must be called after SetAgentType. Ignored if agent type is not set.
func SetAgentVersion(ver string) {
	if ver == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if agentVersion != "" || agentType == "" {
		return
	}
	agentVersion = ver
	cached = ""
}

// SetRepoID records the SageOx repo ID of the project this invocation is
// working in, to be sent on SageOx API requests as HeaderRepoID.
// Thread-safe. First write wins; subsequent calls are no-ops.
//
// Values that fail repoIDRe are dropped rather than emitted, so a hand-edited
// or corrupt .sageox/config.json can never produce a malformed request.
//
// Callers MUST NOT set this in a process that serves more than one workspace.
// The daemon syncs every repo on the machine from a single process, so a
// process-global repo ID there would attribute all of its traffic to whichever
// repo it happened to start in — replacing missing data with wrong data.
func SetRepoID(id string) {
	if id == "" || !repoIDRe.MatchString(id) {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if repoID != "" {
		return
	}
	repoID = id
}

// RepoID returns the repo ID recorded by SetRepoID, or "" when unset.
func RepoID() string {
	mu.RLock()
	defer mu.RUnlock()
	return repoID
}

// SetDaemonMode marks this process as the SageOx daemon, so every request
// built through NewRequest / SetHeaders identifies as unattended background
// traffic rather than an interactive CLI invocation.
//
// This is process-global on purpose. The server excludes daemon traffic from
// human CLI activity metrics by matching the "ox-daemon/" User-Agent prefix,
// which makes the UA load-bearing: any daemon request that slips through with
// the interactive UA is silently counted as a human running ox. Marking the
// process once, at the point where it becomes the daemon, means no client
// type, no method, and no future call site has to remember to opt in.
//
// Safe because the daemon's work only ever runs in the daemon process:
// NewSyncScheduler is constructed solely by daemon.New, which is called solely
// by the `ox daemon start --foreground` entry point. No CLI command runs the
// scheduler in-process, so this can never mislabel human traffic.
func SetDaemonMode() {
	mu.Lock()
	defer mu.Unlock()
	daemonMode = true
	cached = ""
}

// IsDaemonMode reports whether this process has been marked as the daemon.
func IsDaemonMode() bool {
	mu.RLock()
	defer mu.RUnlock()
	return daemonMode
}

// SetOrchestratorType records the detected orchestrator (e.g. "conductor", "openclaw").
// Thread-safe. First write wins; subsequent calls are no-ops.
func SetOrchestratorType(ot string) {
	if ot == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if orchestratorType != "" {
		return
	}
	orchestratorType = ot
	cached = ""
}

// String returns the User-Agent for CLI requests.
// With agent:    "ox/0.17.0 (claude-code/1.0.26; darwin; arm64)"
// Without ver:   "ox/0.17.0 (claude-code; darwin; arm64)"
// No agent:      "ox/0.17.0 (darwin; arm64)"
// Daemon mode:   "ox-daemon/0.17.0 (darwin; arm64)"
func String() string {
	mu.RLock()
	if cached != "" {
		s := cached
		mu.RUnlock()
		return s
	}
	mu.RUnlock()

	mu.Lock()
	defer mu.Unlock()
	if cached != "" {
		return cached
	}

	// In the daemon, every request is background traffic regardless of which
	// coding agent happened to spawn the process, so the agent tokens below
	// are deliberately not applied.
	if daemonMode {
		cached = daemonStr
		return cached
	}

	// fall back to environment variables set by ox agent prime
	at := agentType
	if at == "" {
		at = os.Getenv("AGENT_ENV")
	}
	av := agentVersion
	if av == "" && at != "" {
		av = os.Getenv("AGENT_VERSION")
	}

	// build parenthesized token list
	var tokens []string
	if at != "" {
		agentToken := at
		if av != "" {
			agentToken = at + "/" + av
		}
		tokens = append(tokens, agentToken)
	}
	tokens = append(tokens, runtime.GOOS, runtime.GOARCH)

	cached = fmt.Sprintf("ox/%s (%s)", version.Version, strings.Join(tokens, "; "))
	return cached
}

// OrchestratorType returns the detected orchestrator type (e.g. "conductor").
// Returns empty string if no orchestrator is detected.
func OrchestratorType() string {
	mu.RLock()
	defer mu.RUnlock()
	ot := orchestratorType
	if ot == "" {
		ot = os.Getenv("ORCHESTRATOR_ENV")
	}
	return ot
}

// NewRequest creates an HTTP request with standard ox headers (User-Agent, X-Orchestrator).
// Prefer this over http.NewRequestWithContext + SetHeaders to ensure headers are never forgotten.
func NewRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	SetHeaders(req.Header)
	return req, nil
}

// randomTraceparent generates a random W3C traceparent as a fallback when
// OTel tracing is not initialized. Prefer observability.TraceParent() which
// shares the trace ID across all requests in a command.
func randomTraceparent() string {
	var buf [24]byte // 16 (trace ID) + 8 (span ID)
	_, _ = rand.Read(buf[:])
	return "00-" + hex.EncodeToString(buf[:16]) + "-" + hex.EncodeToString(buf[16:]) + "-01"
}

// SetHeaders sets User-Agent, X-Orchestrator, X-Sageox-Repo-Id, and traceparent
// headers on the request. Use this for SageOx API requests to include full
// telemetry context.
//
// Every call site is a SageOx-owned endpoint (internal/api, internal/auth,
// internal/doctorapi, internal/telemetry) — LFS and git transport do not go
// through here — so the repo ID is never disclosed to a third party.
//
// If OTel tracing is active, the traceparent comes from the current command's
// root span so all requests within one command share the same trace ID.
// Falls back to a random traceparent when tracing is not initialized.
func SetHeaders(h http.Header) {
	h.Set("User-Agent", String())
	if ot := OrchestratorType(); ot != "" {
		h.Set("X-Orchestrator", ot)
	}
	if rid := RepoID(); rid != "" {
		h.Set(HeaderRepoID, rid)
	}
	if h.Get("traceparent") == "" {
		tp := observability.TraceParent()
		if tp == "" {
			tp = randomTraceparent()
		}
		h.Set("traceparent", tp)
		slog.Debug("http request traceparent", "traceparent", tp)
	}
}

// DaemonString returns the User-Agent for daemon requests.
// Format: "ox-daemon/0.17.0 (darwin; arm64)"
func DaemonString() string {
	return daemonStr
}

// ResetForTesting clears cached state. Test use only.
func ResetForTesting() {
	mu.Lock()
	defer mu.Unlock()
	agentType = ""
	agentVersion = ""
	orchestratorType = ""
	repoID = ""
	daemonMode = false
	cached = ""
}
