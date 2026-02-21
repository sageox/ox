package useragent

import (
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/version"
	"github.com/stretchr/testify/assert"
)

func TestString_WithoutAgentType(t *testing.T) {
	ResetForTesting()

	ua := String()
	expected := "ox/" + version.Version + " (" + runtime.GOOS + "; " + runtime.GOARCH + ")"
	assert.Equal(t, expected, ua)
}

func TestString_WithAgentType(t *testing.T) {
	ResetForTesting()
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
	before := String()
	assert.NotContains(t, before, "claude-code")

	SetAgentType("claude-code")
	after := String()
	assert.Contains(t, after, "claude-code")
}

func TestResetForTesting(t *testing.T) {
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
