package useragent

import (
	"fmt"
	"os"
	"runtime"
	"sync"

	"github.com/sageox/ox/internal/version"
)

var (
	mu           sync.RWMutex
	agentType    string
	agentVersion string
	cached       string
	daemonStr    string
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

// String returns the User-Agent for CLI requests.
// With agent version: "ox/0.17.0 (claude-code/1.0.26; darwin; arm64)"
// Without version:    "ox/0.17.0 (claude-code; darwin; arm64)"
// No agent:           "ox/0.17.0 (darwin; arm64)"
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

	if at != "" {
		agentToken := at
		if av != "" {
			agentToken = at + "/" + av
		}
		cached = fmt.Sprintf("ox/%s (%s; %s; %s)", version.Version, agentToken, runtime.GOOS, runtime.GOARCH)
	} else {
		cached = fmt.Sprintf("ox/%s (%s; %s)", version.Version, runtime.GOOS, runtime.GOARCH)
	}
	return cached
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
	cached = ""
}
