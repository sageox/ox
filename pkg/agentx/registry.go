package agentx

import (
	"context"
	"fmt"
	"sync"
)

// DefaultRegistry is the global registry with all supported agents.
var DefaultRegistry = NewRegistry()

// registry implements Registry with thread-safe agent management.
type registry struct {
	mu     sync.RWMutex
	agents map[AgentType]Agent
}

// NewRegistry creates a new empty agent registry.
func NewRegistry() Registry {
	return &registry{
		agents: make(map[AgentType]Agent),
	}
}

func (r *registry) Register(agent Agent) error {
	if agent == nil {
		return fmt.Errorf("agent cannot be nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.agents[agent.Type()] = agent
	return nil
}

func (r *registry) Get(agentType AgentType) (Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	agent, ok := r.agents[agentType]
	return agent, ok
}

func (r *registry) List() []Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()

	agents := make([]Agent, 0, len(r.agents))
	for _, agent := range r.agents {
		agents = append(agents, agent)
	}
	return agents
}

func (r *registry) Detector() Detector {
	return &detector{registry: r}
}

// detector implements Detector using a registry.
type detector struct {
	registry *registry
	env      Environment
}

// NewDetector creates a detector with the default registry.
func NewDetector() Detector {
	return DefaultRegistry.Detector()
}

// NewDetectorWithEnv creates a detector with a custom environment.
func NewDetectorWithEnv(env Environment) Detector {
	reg, ok := DefaultRegistry.(*registry)
	if !ok {
		// Fallback to empty registry if type assertion fails
		reg = &registry{
			agents: make(map[AgentType]Agent),
		}
	}
	return &detector{
		registry: reg,
		env:      env,
	}
}

func (d *detector) getEnv() Environment {
	if d.env != nil {
		return d.env
	}
	return NewSystemEnvironment()
}

func (d *detector) Detect(ctx context.Context) (Agent, error) {
	return d.detectByRole(ctx, RoleAgent)
}

func (d *detector) DetectOrchestrator(ctx context.Context) (Agent, error) {
	return d.detectByRole(ctx, RoleOrchestrator)
}

// detectByRole finds the first detected agent matching the given role.
// Each agent's Detect() handles AGENT_ENV priority internally.
func (d *detector) detectByRole(ctx context.Context, role AgentRole) (Agent, error) {
	env := d.getEnv()

	for _, agent := range d.registry.List() {
		if agent.Role() != role {
			continue
		}
		detected, err := agent.Detect(ctx, env)
		if err != nil {
			continue
		}
		if detected {
			return agent, nil
		}
	}

	return nil, nil
}

// DetectAll returns all detected agents and orchestrators regardless of role.
// Unlike Detect (agents only) and DetectOrchestrator (orchestrators only),
// this does not filter by role.
func (d *detector) DetectAll(ctx context.Context) ([]Agent, error) {
	env := d.getEnv()
	var detected []Agent

	for _, agent := range d.registry.List() {
		ok, err := agent.Detect(ctx, env)
		if err != nil {
			continue
		}
		if ok {
			detected = append(detected, agent)
		}
	}

	return detected, nil
}

func (d *detector) DetectByType(ctx context.Context, agentType AgentType) (bool, error) {
	agent, ok := d.registry.Get(agentType)
	if !ok {
		return false, fmt.Errorf("agent type %s not registered", agentType)
	}

	return agent.Detect(ctx, d.getEnv())
}

// IsAgentContext returns true if running inside any coding agent.
// This is a convenience function using the default registry.
func IsAgentContext() bool {
	ctx := context.Background()
	agent, _ := NewDetector().Detect(ctx)
	return agent != nil
}

// RequireAgent returns an error message if not running in an agent context.
// Returns empty string if in agent context.
func RequireAgent(commandName string) string {
	if IsAgentContext() {
		return ""
	}
	return fmt.Sprintf("'%s' must be run from within a coding agent (Claude Code, Cursor, etc.).\n"+
		"If your agent doesn't set standard env vars, set AGENT_ENV=<agent-name> before running.", commandName)
}

// CurrentAgent returns the currently detected coding agent, or nil if none.
func CurrentAgent() Agent {
	ctx := context.Background()
	agent, _ := NewDetector().Detect(ctx)
	return agent
}

// CurrentOrchestrator returns the currently detected orchestrator, or nil if none.
func CurrentOrchestrator() Agent {
	ctx := context.Background()
	orch, _ := NewDetector().DetectOrchestrator(ctx)
	return orch
}

// OrchestratorType returns the type string of the detected orchestrator,
// or empty string if none detected.
func OrchestratorType() string {
	orch := CurrentOrchestrator()
	if orch == nil {
		return ""
	}
	return string(orch.Type())
}
