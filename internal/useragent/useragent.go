package useragent

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/sageox/ox/internal/version"
)

var (
	mu               sync.RWMutex
	agentType        string
	agentVersion     string
	orchestratorType string
	cached           string
	daemonStr        string
)

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

// SetHeaders sets User-Agent and X-Orchestrator headers on the request.
// Use this for SageOx API requests to include full telemetry context.
func SetHeaders(h http.Header) {
	h.Set("User-Agent", String())
	if ot := OrchestratorType(); ot != "" {
		h.Set("X-Orchestrator", ot)
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
	cached = ""
}
