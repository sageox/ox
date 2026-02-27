package agentx

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSupportedAgents(t *testing.T) {
	// Verify all expected agents are in the list
	expected := []AgentType{
		AgentTypeClaudeCode,
		AgentTypeCursor,
		AgentTypeWindsurf,
		AgentTypeCopilot,
		AgentTypeAider,
		AgentTypeCody,
		AgentTypeContinue,
		AgentTypeCodePuppy,
		AgentTypeKiro,
		AgentTypeOpenCode,
		AgentTypeCodex,
		AgentTypeGoose,
		AgentTypeAmp,
		AgentTypeCline,
		AgentTypeDroid,
		AgentTypeOpenClaw,
		AgentTypeConductor,
	}

	assert.Equal(t, len(expected), len(SupportedAgents), "SupportedAgents count mismatch")

	for _, e := range expected {
		found := false
		for _, s := range SupportedAgents {
			if s == e {
				found = true
				break
			}
		}
		assert.True(t, found, "expected agent %s not found in SupportedAgents", e)
	}
}

func TestAgentTypeConstants(t *testing.T) {
	// Verify agent type slugs are correct
	tests := []struct {
		agentType AgentType
		expected  string
	}{
		{AgentTypeClaudeCode, "claude"},
		{AgentTypeCursor, "cursor"},
		{AgentTypeWindsurf, "windsurf"},
		{AgentTypeCopilot, "copilot"},
		{AgentTypeAider, "aider"},
		{AgentTypeCody, "cody"},
		{AgentTypeContinue, "continue"},
		{AgentTypeCodePuppy, "code-puppy"},
		{AgentTypeKiro, "kiro"},
		{AgentTypeOpenCode, "opencode"},
		{AgentTypeCodex, "codex"},
		{AgentTypeGoose, "goose"},
		{AgentTypeAmp, "amp"},
		{AgentTypeCline, "cline"},
		{AgentTypeDroid, "droid"},
		{AgentTypeOpenClaw, "openclaw"},
		{AgentTypeConductor, "conductor"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.expected, string(tt.agentType), "AgentType %v mismatch", tt.agentType)
	}
}

func TestMockEnvironment(t *testing.T) {
	env := NewMockEnvironment(map[string]string{
		"CLAUDECODE": "1",
		"AGENT_ENV":  "claude",
	})

	assert.Equal(t, "1", env.GetEnv("CLAUDECODE"), "GetEnv failed for CLAUDECODE")
	assert.Equal(t, "claude", env.GetEnv("AGENT_ENV"), "GetEnv failed for AGENT_ENV")
	assert.Equal(t, "", env.GetEnv("NONEXISTENT"), "GetEnv should return empty for nonexistent key")

	val, ok := env.LookupEnv("CLAUDECODE")
	assert.True(t, ok, "LookupEnv should return true for CLAUDECODE")
	assert.Equal(t, "1", val, "LookupEnv value mismatch for CLAUDECODE")

	_, ok = env.LookupEnv("NONEXISTENT")
	assert.False(t, ok, "LookupEnv should return false for nonexistent key")
}

func TestSystemEnvironment(t *testing.T) {
	env := NewSystemEnvironment()

	// These should not error
	_, err := env.HomeDir()
	require.NoError(t, err, "HomeDir() should not error")

	_, err = env.ConfigDir()
	require.NoError(t, err, "ConfigDir() should not error")

	_, err = env.DataDir()
	require.NoError(t, err, "DataDir() should not error")

	_, err = env.CacheDir()
	require.NoError(t, err, "CacheDir() should not error")

	goos := env.GOOS()
	assert.NotEmpty(t, goos, "GOOS() should not be empty")
}

func TestRegistry(t *testing.T) {
	reg := NewRegistry()

	// Register a mock agent
	agent := &mockAgent{
		agentType: AgentTypeClaudeCode,
		name:      "Claude Code",
	}

	err := reg.Register(agent)
	require.NoError(t, err, "Register() should not error")

	// Get the agent back
	got, ok := reg.Get(AgentTypeClaudeCode)
	assert.True(t, ok, "Get() should return true for registered agent")
	assert.Equal(t, AgentTypeClaudeCode, got.Type(), "Get() returned wrong agent type")

	// List should include the agent
	list := reg.List()
	assert.Equal(t, 1, len(list), "List() should return 1 agent")

	// Get nonexistent
	_, ok = reg.Get(AgentTypeCursor)
	assert.False(t, ok, "Get() should return false for unregistered agent")
}

func TestDetector(t *testing.T) {
	reg := NewRegistry()

	// Register a mock agent that always detects
	agent := &mockAgent{
		agentType:    AgentTypeClaudeCode,
		name:         "Claude Code",
		detectResult: true,
	}
	reg.Register(agent)

	detector := reg.Detector()

	// Should detect the agent
	ctx := context.Background()
	detected, err := detector.Detect(ctx)
	require.NoError(t, err, "Detect() should not error")
	require.NotNil(t, detected, "Detect() should return an agent")
	assert.Equal(t, AgentTypeClaudeCode, detected.Type(), "Detect() returned wrong type")
}

func TestDetectOrchestrator(t *testing.T) {
	reg := NewRegistry()

	agent := &mockAgent{
		agentType:    AgentTypeClaudeCode,
		name:         "Claude Code",
		detectResult: true,
	}
	orch := &mockAgent{
		agentType:    AgentTypeConductor,
		name:         "Conductor",
		role:         RoleOrchestrator,
		detectResult: true,
	}
	reg.Register(agent)
	reg.Register(orch)

	ctx := context.Background()
	d := reg.Detector()

	// Detect() should return only the coding agent, not the orchestrator
	detected, err := d.Detect(ctx)
	require.NoError(t, err)
	require.NotNil(t, detected)
	assert.Equal(t, AgentTypeClaudeCode, detected.Type())

	// DetectOrchestrator() should return only the orchestrator
	detectedOrch, err := d.DetectOrchestrator(ctx)
	require.NoError(t, err)
	require.NotNil(t, detectedOrch)
	assert.Equal(t, AgentTypeConductor, detectedOrch.Type())
}

func TestDetectOrchestrator_NoneRegistered(t *testing.T) {
	reg := NewRegistry()

	agent := &mockAgent{
		agentType:    AgentTypeClaudeCode,
		name:         "Claude Code",
		detectResult: true,
	}
	reg.Register(agent)

	ctx := context.Background()
	d := reg.Detector()

	// no orchestrators registered, should return nil
	detectedOrch, err := d.DetectOrchestrator(ctx)
	require.NoError(t, err)
	assert.Nil(t, detectedOrch)
}

func TestDetect_SkipsOrchestrators(t *testing.T) {
	reg := NewRegistry()

	// register only an orchestrator
	orch := &mockAgent{
		agentType:    AgentTypeOpenClaw,
		name:         "OpenClaw",
		role:         RoleOrchestrator,
		detectResult: true,
	}
	reg.Register(orch)

	ctx := context.Background()
	d := reg.Detector()

	// Detect() should return nil since only orchestrators are registered
	detected, err := d.Detect(ctx)
	require.NoError(t, err)
	assert.Nil(t, detected)
}

func TestRequireAgent_NoAgent(t *testing.T) {
	// Save and restore DefaultRegistry
	orig := DefaultRegistry
	defer func() { DefaultRegistry = orig }()

	DefaultRegistry = NewRegistry()
	// no agents registered — RequireAgent should return error message
	msg := RequireAgent("ox session start")
	assert.NotEmpty(t, msg, "RequireAgent should return error when no agent detected")
	assert.Contains(t, msg, "ox session start")
	assert.Contains(t, msg, "coding agent")
}

func TestRequireAgent_WithAgent(t *testing.T) {
	orig := DefaultRegistry
	defer func() { DefaultRegistry = orig }()

	DefaultRegistry = NewRegistry()
	DefaultRegistry.Register(&mockAgent{
		agentType:    AgentTypeClaudeCode,
		name:         "Claude Code",
		detectResult: true,
	})

	msg := RequireAgent("ox session start")
	assert.Empty(t, msg, "RequireAgent should return empty when agent is detected")
}

func TestRegister_Nil(t *testing.T) {
	reg := NewRegistry()
	err := reg.Register(nil)
	assert.Error(t, err, "Register(nil) should error")
}

// mockAgent is a test implementation of Agent
type mockAgent struct {
	agentType    AgentType
	name         string
	url          string
	role         AgentRole
	detectResult bool
	capabilities Capabilities
}

func (a *mockAgent) Type() AgentType { return a.agentType }
func (a *mockAgent) Name() string    { return a.name }
func (a *mockAgent) URL() string     { return a.url }
func (a *mockAgent) Role() AgentRole {
	if a.role != "" {
		return a.role
	}
	return RoleAgent
}
func (a *mockAgent) Detect(ctx context.Context, env Environment) (bool, error) {
	return a.detectResult, nil
}
func (a *mockAgent) UserConfigPath(env Environment) (string, error)                 { return "/mock/user/config", nil }
func (a *mockAgent) ProjectConfigPath() string                                      { return ".mock" }
func (a *mockAgent) ContextFiles() []string                                         { return []string{"MOCK.md"} }
func (a *mockAgent) SupportsXDGConfig() bool                                        { return true }
func (a *mockAgent) Capabilities() Capabilities                                     { return a.capabilities }
func (a *mockAgent) HookManager() HookManager                                       { return nil }
func (a *mockAgent) CommandManager() CommandManager                                 { return nil }
func (a *mockAgent) IsInstalled(ctx context.Context, env Environment) (bool, error) { return true, nil }
func (a *mockAgent) DetectVersion(_ context.Context, _ Environment) string          { return "" }
