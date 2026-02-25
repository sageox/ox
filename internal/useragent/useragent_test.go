package useragent

import (
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/version"
	"github.com/stretchr/testify/assert"
)

// clearEnv clears all useragent-relevant env vars for a clean test.
func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AGENT_ENV", "")
	t.Setenv("AGENT_VERSION", "")
	t.Setenv("ORCHESTRATOR_ENV", "")
}

func TestString_WithoutAgentType(t *testing.T) {
	ResetForTesting()
	clearEnv(t)

	ua := String()
	expected := "ox/" + version.Version + " (" + runtime.GOOS + "; " + runtime.GOARCH + ")"
	assert.Equal(t, expected, ua)
}

func TestString_WithAgentType(t *testing.T) {
	ResetForTesting()
	clearEnv(t)
	SetAgentType("claude-code")

	ua := String()
	expected := "ox/" + version.Version + " (claude-code; " + runtime.GOOS + "; " + runtime.GOARCH + ")"
	assert.Equal(t, expected, ua)
}

func TestSetAgentType_FirstWriteWins(t *testing.T) {
	ResetForTesting()
	SetAgentType("claude-code")
	SetAgentType("cursor")

	ua := String()
	assert.Contains(t, ua, "claude-code")
	assert.NotContains(t, ua, "cursor")
}

func TestSetAgentType_EmptyIgnored(t *testing.T) {
	ResetForTesting()
	SetAgentType("")
	SetAgentType("claude-code")

	ua := String()
	assert.Contains(t, ua, "claude-code")
}

func TestDaemonString(t *testing.T) {
	expected := "ox-daemon/" + version.Version + " (" + runtime.GOOS + "; " + runtime.GOARCH + ")"
	assert.Equal(t, expected, DaemonString())
}

func TestString_CachesResult(t *testing.T) {
	ResetForTesting()
	a := String()
	b := String()
	assert.Equal(t, a, b)
}

func TestString_InvalidatesCacheOnSetAgentType(t *testing.T) {
	ResetForTesting()
	clearEnv(t)
	before := String()
	assert.NotContains(t, before, "claude-code")

	SetAgentType("claude-code")
	after := String()
	assert.Contains(t, after, "claude-code")
}

func TestResetForTesting(t *testing.T) {
	clearEnv(t)
	SetAgentType("claude-code")
	ResetForTesting()

	ua := String()
	assert.NotContains(t, ua, "claude-code")
}

func TestString_ContainsVersion(t *testing.T) {
	ResetForTesting()
	ua := String()
	assert.True(t, strings.HasPrefix(ua, "ox/"+version.Version))
}

func TestString_WithAgentVersion(t *testing.T) {
	ResetForTesting()
	SetAgentType("claude-code")
	SetAgentVersion("1.0.26")

	ua := String()
	expected := "ox/" + version.Version + " (claude-code/1.0.26; " + runtime.GOOS + "; " + runtime.GOARCH + ")"
	assert.Equal(t, expected, ua)
}

func TestSetAgentVersion_WithoutAgentType(t *testing.T) {
	ResetForTesting()
	clearEnv(t)
	SetAgentVersion("1.0.26")

	ua := String()
	assert.NotContains(t, ua, "1.0.26", "version should be ignored without agent type")
}

func TestSetAgentVersion_FirstWriteWins(t *testing.T) {
	ResetForTesting()
	SetAgentType("claude-code")
	SetAgentVersion("1.0.26")
	SetAgentVersion("2.0.0")

	ua := String()
	assert.Contains(t, ua, "claude-code/1.0.26")
	assert.NotContains(t, ua, "2.0.0")
}

func TestSetAgentVersion_EmptyIgnored(t *testing.T) {
	ResetForTesting()
	SetAgentType("claude-code")
	SetAgentVersion("")
	SetAgentVersion("1.0.26")

	ua := String()
	assert.Contains(t, ua, "claude-code/1.0.26")
}

func TestSetAgentVersion_InvalidatesCacheOnSet(t *testing.T) {
	ResetForTesting()
	SetAgentType("claude-code")
	before := String()
	assert.NotContains(t, before, "/1.0.26")

	SetAgentVersion("1.0.26")
	after := String()
	assert.Contains(t, after, "claude-code/1.0.26")
}

func TestString_FallsBackToEnvVars(t *testing.T) {
	ResetForTesting()

	// simulate env vars set by a prior ox agent prime invocation
	t.Setenv("AGENT_ENV", "cursor")
	t.Setenv("AGENT_VERSION", "0.44.11")

	ua := String()
	assert.Contains(t, ua, "cursor/0.44.11")
}

func TestString_ExplicitSetTakesPrecedenceOverEnv(t *testing.T) {
	ResetForTesting()

	t.Setenv("AGENT_ENV", "cursor")
	t.Setenv("AGENT_VERSION", "0.44.11")

	SetAgentType("claude-code")
	SetAgentVersion("1.0.26")

	ua := String()
	assert.Contains(t, ua, "claude-code/1.0.26")
	assert.NotContains(t, ua, "cursor")
}

func TestString_EnvAgentTypeOnly(t *testing.T) {
	ResetForTesting()
	clearEnv(t)
	t.Setenv("AGENT_ENV", "aider")

	ua := String()
	assert.Contains(t, ua, "aider;")
	assert.NotContains(t, ua, "aider/")
}

func TestSetAgentType_ConcurrentSafe(t *testing.T) {
	ResetForTesting()

	var wg sync.WaitGroup
	agents := []string{"claude-code", "cursor", "copilot", "cline", "windsurf"}
	for _, a := range agents {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			SetAgentType(name)
		}(a)
	}
	wg.Wait()

	ua := String()
	// exactly one agent type should be present
	count := 0
	for _, a := range agents {
		if strings.Contains(ua, a) {
			count++
		}
	}
	assert.Equal(t, 1, count, "exactly one agent type should win: %s", ua)
}

func TestString_OrchestratorNotInUserAgent(t *testing.T) {
	ResetForTesting()
	clearEnv(t)
	SetAgentType("claude-code")
	SetOrchestratorType("conductor")

	ua := String()
	expected := "ox/" + version.Version + " (claude-code; " + runtime.GOOS + "; " + runtime.GOARCH + ")"
	assert.Equal(t, expected, ua)
	assert.NotContains(t, ua, "conductor")
}

func TestSetOrchestratorType_FirstWriteWins(t *testing.T) {
	ResetForTesting()
	SetOrchestratorType("conductor")
	SetOrchestratorType("openclaw")

	assert.Equal(t, "conductor", OrchestratorType())
}

func TestSetOrchestratorType_EmptyIgnored(t *testing.T) {
	ResetForTesting()
	clearEnv(t)
	SetOrchestratorType("")
	SetOrchestratorType("conductor")

	assert.Equal(t, "conductor", OrchestratorType())
}

func TestOrchestratorType_FallsBackToEnv(t *testing.T) {
	ResetForTesting()
	clearEnv(t)
	t.Setenv("ORCHESTRATOR_ENV", "conductor")

	assert.Equal(t, "conductor", OrchestratorType())
}

func TestOrchestratorType_ExplicitTakesPrecedenceOverEnv(t *testing.T) {
	ResetForTesting()
	clearEnv(t)
	t.Setenv("ORCHESTRATOR_ENV", "openclaw")
	SetOrchestratorType("conductor")

	assert.Equal(t, "conductor", OrchestratorType())
}

func TestSetHeaders_WithOrchestrator(t *testing.T) {
	ResetForTesting()
	clearEnv(t)
	SetAgentType("claude-code")
	SetAgentVersion("1.0.26")
	SetOrchestratorType("conductor")

	h := http.Header{}
	SetHeaders(h)

	assert.Equal(t, "ox/"+version.Version+" (claude-code/1.0.26; "+runtime.GOOS+"; "+runtime.GOARCH+")", h.Get("User-Agent"))
	assert.Equal(t, "conductor", h.Get("X-Orchestrator"))
}

func TestSetHeaders_WithoutOrchestrator(t *testing.T) {
	ResetForTesting()
	clearEnv(t)
	SetAgentType("claude-code")

	h := http.Header{}
	SetHeaders(h)

	assert.NotEmpty(t, h.Get("User-Agent"))
	assert.Empty(t, h.Get("X-Orchestrator"))
}
