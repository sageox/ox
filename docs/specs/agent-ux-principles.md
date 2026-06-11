# Agent UX Principles

Comprehensive guide for designing CLI and API experiences optimized for AI coding agents.

## Origins and Attribution

The concept of "Agent UX" as a distinct discipline was catalyzed by Steve Yegge's December 2025 post observing that AI coding agents have tool preferences—they gravitate toward interfaces that resemble what they've seen before. This insight sparked the realization that agents, like humans, benefit from familiar patterns.

Ryan Snodgrass evolved these ideas into a comprehensive framework while building SageOx, discovering through practice that Agent UX shares deep parallels with human UX: both benefit from familiarity, both hate blocking, both need progressive disclosure.

## Overview

"Agent UX" is the discipline of designing tool interfaces that AI agents can consume efficiently and act upon effectively. Unlike human UX which optimizes for comprehension and delight, Agent UX optimizes for:

1. **Token efficiency** - Minimize context window consumption
2. **Parseability** - Structured output agents can reliably extract data from
3. **Progressive disclosure** - Just-in-time information delivery
4. **Actionable guidance** - Clear next steps, not just information

---

## Core Principles

### 1. Never Block the Event Loop

The most important principle: **don't stall the caller**.

Whether the caller is an AI agent or a human, blocking their "event loop" (context window / working memory / attention) is costly:

- **Agents**: Blocked context can't process other tasks; timeout = wasted tokens
- **Humans**: Waiting = frustration, context switching, lost flow state

```
Bad:  mytool sync           # Blocks for 30 seconds while syncing
Good: mytool sync --async   # Returns immediately, daemon handles work
Best: mytool sync           # Daemon auto-syncs, command just confirms status
```

**The Daemon Pattern:**

Long-running operations should be delegated to a background daemon:

```go
// Command returns immediately
func runSync(cmd *cobra.Command, args []string) error {
    // Tell daemon to sync (non-blocking)
    if err := daemon.RequestSync(); err != nil {
        return err
    }

    // Return status immediately
    fmt.Println("Sync queued. Run 'mytool status' to check progress.")
    return nil
}
```

**When to use a daemon:**
- Network operations (API calls, git sync)
- File watching (config changes, session recording)
- Periodic tasks (health checks, cache refresh)
- Any operation that might take > 100ms

**Why this matters for agents:**
```
Agent context: 200K tokens
Agent waiting for 30s sync: 0 tokens processed
Agent with async daemon: Continues working, daemon syncs in parallel
```

The same applies to humans - a CLI that blocks for network calls trains users to avoid using it.

### 2. Tokens Are Precious

Every token in an agent's context window competes with the developer's actual work.

```
Bad:  2000 tokens of "here's everything about your project"
Good: 500 tokens of "your team uses React + Express, call me when working on API design"
```

**Guidelines:**
- Lead with actionable instruction, not explanation
- Omit obvious context the agent already has
- Prefer references over repetition ("see above" vs repeating content)
- Use structured formats (JSON) for machine parsing

### 3. JSON First, Text Second

Agents parse JSON reliably. Free-form text requires interpretation.

```go
// Agent UX Decision: JSON is the default output format.
// Why: Text output wastes tokens and requires parsing.
// JSON is machine-readable by default.
```

**Recommended flag precedence:**
1. `--review` - Security audit mode (both human summary AND JSON)
2. `--text` - Human-readable output only
3. Default - JSON output

### 4. Progressive Disclosure

Don't dump everything upfront. Reveal information as needed.

```
mytool prime              → 500 tokens: "Your team uses React + Express, call me for API patterns"
mytool guidance api       → 750 tokens: REST/GraphQL overview, deeper triggers
mytool guidance api/rest  → 500 tokens: REST-specific patterns and conventions
```

**Taxonomy-based paths:**
- Agents request specific guidance paths on-demand
- Each response includes token estimate for context budgeting
- Suggested paths are ranked by relevance to detected context

### 5. Familiar Patterns Win

Agents prefer tools that look like tools they already know.

**Why this matters:**
- Agents are trained on millions of CLI interactions (git, npm, docker, kubectl)
- Familiar patterns require zero learning—they pattern-match instantly
- Novel interfaces require explanation tokens and risk misuse
- This applies equally to humans (muscle memory, mental models)

**Unix Philosophy Alignment:**

```bash
# Agents instantly understand these patterns:
mytool init                # like: git init, npm init
mytool status              # like: git status, docker status
mytool doctor              # like: brew doctor, flutter doctor
mytool login / logout      # like: gh auth login, aws configure

# They'd struggle with:
mytool bootstrap-repository    # unfamiliar, verbose
mytool health-diagnostic       # not the convention
mytool authenticate-user       # nobody does this
```

**Command Naming Guidelines:**

| Pattern | Convention | Examples |
|---------|------------|----------|
| Initialize | `init` | git init, npm init, docker init |
| Status check | `status` | git status, systemctl status |
| Diagnostics | `doctor` | brew doctor, flutter doctor |
| Auth | `login`/`logout` | gh auth login, docker login |
| Version | `version` or `--version` | Nearly universal |
| Help | `help` or `--help` | Universal |
| List items | `list` or `ls` | docker ps, kubectl get |
| Create | `create` or `new` | kubectl create, rails new |
| Delete | `delete` or `rm` | kubectl delete, rm |
| Configuration | `config` | git config, npm config |

**Flag Conventions:**

```bash
# Familiar flags agents expect:
-v, --verbose      # More output
-q, --quiet        # Less output
-h, --help         # Help text
-f, --force        # Skip confirmations
-y, --yes          # Auto-confirm
-o, --output       # Output format/file
--json             # JSON output
--dry-run          # Preview without action
```

**Why Simple Unix Tools Get Adopted:**

1. **Training data density** - Billions of examples in agent training
2. **Composability** - Pipes, redirects work as expected
3. **Predictable behavior** - No surprises, no magic
4. **Self-documenting** - `--help` always works
5. **Exit codes** - 0 = success, non-zero = failure

**The Familiarity Principle:**

> Design your CLI as if agents have used a thousand similar tools before—because they have.

When in doubt, ask: "What would git/docker/kubectl do?"

### 6. State-Aware Recommendations

Both humans and agents benefit from contextual recommendations.

**For humans (help output):**
```
State: No config detected → Highlight: init (Step 1)
State: Initialized, not authenticated → Highlight: login (Step 2)
State: Fully set up → Highlight: doctor (always useful)
```

**For agents (prime output):**
```json
{
  "suggested_paths": [
    {"path": "api/rest", "relevance": 0.95, "reason": "Express routes detected"},
    {"path": "frontend/react", "relevance": 0.7, "reason": "React components present"}
  ]
}
```

---

## Output Format Patterns

### Machine-Readable Structure

Agent-facing commands should output JSON with consistent structure:

```json
{
  "session_id": "abc123",
  "status": "success",
  "content": "...",
  "metadata": {
    "tokens": 523,
    "cache_hit": true,
    "version": "1.2.0"
  },
  "suggested_paths": [],
  "warnings": []
}
```

### Human Summary Mode (`--review`)

For security audits where humans need to inspect what agents receive:

```
=== HUMAN SUMMARY ===
Session: abc123
Commands: 15 invocations
Content: API design patterns for REST endpoints

=== MACHINE OUTPUT ===
{"session_id":"abc123","content":"..."}
```

### Error Responses

Errors must be parseable AND actionable:

```json
{
  "status": "error",
  "error_code": "AUTH_EXPIRED",
  "message": "Authentication token expired",
  "action": "Run 'mytool login' to re-authenticate",
  "retry": true
}
```

---

## Agent Detection and Adaptation

### Why Detect the Agent?

Different AI coding agents have different:
- **Capabilities** - Tool use, multi-turn, streaming
- **Context limits** - 8K to 200K+ tokens
- **Output preferences** - Some parse JSON better, some prefer markdown
- **Hook systems** - Different integration patterns
- **Advanced features** - MCP servers, custom tools, memory systems

Detecting the running agent enables optimized experiences.

### Detection Signals

```go
type AgentEnvironment struct {
    Agent       string   // "claude-code", "cursor", "copilot", "cody", etc.
    Version     string   // Agent version if detectable
    Model       string   // Underlying model if known
    ContextSize int      // Max context window
    Capabilities []string // ["tool_use", "streaming", "mcp", etc.]
}

func DetectAgent() AgentEnvironment {
    // Environment variables
    if os.Getenv("CLAUDE_CODE") != "" {
        return AgentEnvironment{Agent: "claude-code", ...}
    }
    if os.Getenv("CURSOR_SESSION") != "" {
        return AgentEnvironment{Agent: "cursor", ...}
    }

    // Process ancestry
    if parentIsClaudeCode() {
        return AgentEnvironment{Agent: "claude-code", ...}
    }

    // Hook context (passed during prime)
    if hookContext := getHookContext(); hookContext != nil {
        return hookContext.AgentEnv
    }

    return AgentEnvironment{Agent: "unknown"}
}
```

### Detection Methods

| Method | Reliability | Example |
|--------|-------------|---------|
| Environment variable | High | `CLAUDE_CODE=1`, `CURSOR_SESSION=xyz` |
| Process ancestry | Medium | Parent process name check |
| Hook invocation context | High | Agent passes identity during hook |
| User-agent header | Medium | API calls include agent identifier |
| Explicit flag | Highest | `--agent=claude-code` |

### Output Dialect Adaptation

Different agents may prefer different output structures:

```go
type OutputDialect struct {
    Format          string // "json", "markdown", "yaml"
    Verbosity       string // "minimal", "standard", "verbose"
    IncludeExamples bool
    IncludeRationale bool
    MaxTokens       int
}

var agentDialects = map[string]OutputDialect{
    "claude-code": {
        Format:          "json",
        Verbosity:       "minimal",
        IncludeExamples: false,  // Claude reasons well without
        IncludeRationale: false, // Save tokens
        MaxTokens:       600,
    },
    "cursor": {
        Format:          "markdown",
        Verbosity:       "standard",
        IncludeExamples: true,   // Helps with inline suggestions
        IncludeRationale: false,
        MaxTokens:       800,
    },
    "copilot": {
        Format:          "markdown",
        Verbosity:       "minimal",
        IncludeExamples: true,
        IncludeRationale: false,
        MaxTokens:       400,   // Tighter context
    },
    "unknown": {
        Format:          "json",   // Safest default
        Verbosity:       "standard",
        IncludeExamples: true,
        IncludeRationale: true,  // Help unknown agents understand
        MaxTokens:       1000,
    },
}
```

### Capability-Based Feature Exposure

Only expose features the agent can use:

```go
type AgentCapabilities struct {
    ToolUse       bool // Can invoke tools/functions
    MultiTurn     bool // Maintains conversation state
    Streaming     bool // Supports streaming responses
    MCP           bool // Model Context Protocol support
    FileEdit      bool // Can edit files directly
    WebFetch      bool // Can fetch URLs
    BackgroundOps bool // Can run background tasks
}

func GetAvailableCommands(caps AgentCapabilities) []Command {
    commands := []Command{primeCmd, guidanceCmd} // Always available

    if caps.ToolUse {
        commands = append(commands, sessionCmd)
    }
    if caps.MultiTurn {
        commands = append(commands, learnCmd) // Requires back-and-forth
    }
    if caps.MCP {
        commands = append(commands, mcpServerCmd) // Expose MCP integration
    }

    return commands
}
```

### Agent-Specific Optimizations

#### Claude Code

```go
// Claude Code has excellent JSON parsing and tool use
// Optimize for minimal tokens, structured output
func primeForClaudeCode() PrimeOutput {
    return PrimeOutput{
        Format: "json",
        Content: minimalGuidance(),
        Hooks: claudeCodeHooks(),
        MCPServers: suggestMCPServers(), // Claude Code supports MCP
    }
}
```

#### Cursor

```go
// Cursor works well with inline markdown suggestions
// Include more examples, format for readability
func primeForCursor() PrimeOutput {
    return PrimeOutput{
        Format: "markdown",
        Content: guidanceWithExamples(),
        InlineHints: true, // Cursor uses inline suggestions
    }
}
```

#### Generic/Unknown

```go
// Unknown agent: be more explicit, include rationale
// Don't assume capabilities
func primeForUnknown() PrimeOutput {
    return PrimeOutput{
        Format: "json", // Most parseable
        Content: verboseGuidanceWithRationale(),
        Fallbacks: humanReadableFallbacks(),
    }
}
```

### MCP Server Integration

For agents supporting Model Context Protocol:

```go
type MCPServerSuggestion struct {
    Name        string   `json:"name"`
    Description string   `json:"description"`
    Tools       []string `json:"tools"`
    InstallCmd  string   `json:"install_cmd"`
    Relevance   float64  `json:"relevance"`
}

// Suggest MCP servers based on detected project context
func SuggestMCPServers(project DetectedProject) []MCPServerSuggestion {
    var suggestions []MCPServerSuggestion

    if project.HasDatabase {
        suggestions = append(suggestions, MCPServerSuggestion{
            Name:        "database-mcp",
            Description: "Database schema and query tools",
            Tools:       []string{"db_query", "db_schema", "db_migrate"},
            Relevance:   0.95,
        })
    }

    return suggestions
}
```

### Graceful Degradation

When agent is unknown or capabilities limited:

```go
func AdaptOutput(output PrimeOutput, agent AgentEnvironment) PrimeOutput {
    // Unknown agent: add more context
    if agent.Agent == "unknown" {
        output.IncludeRationale = true
        output.IncludeExamples = true
        output.AddFallbackInstructions = true
    }

    // Limited context: compress
    if agent.ContextSize < 32000 {
        output = compressOutput(output, agent.ContextSize * 0.1) // Use max 10%
    }

    // No tool use: provide copy-paste commands
    if !hasCapability(agent, "tool_use") {
        output.CommandsAsCopyPaste = true
    }

    return output
}
```

### Agent Registration

Agents can self-identify during initialization:

```bash
# Agent explicitly identifies itself
mytool prime --agent=claude-code --agent-version=1.0.25 --capabilities=tool_use,mcp,streaming
```

```json
// Or via JSON input
{
  "agent": "cursor",
  "version": "0.45.0",
  "capabilities": ["tool_use", "file_edit"],
  "context_size": 128000,
  "model": "claude-3.5-sonnet"
}
```

---

## Model Tier Guidance

Different models for different tasks:

| Tier | Model | Use Case |
|------|-------|----------|
| Fast | Haiku | Templating, formatting, simple transforms |
| Balanced | Sonnet | General tasks, code generation |
| Reasoning | Opus | Expert analysis, complex decisions |

---

## Anti-Patterns

### Don't Do This

1. **Blocking on network/IO operations**
   - Bad: `mytool sync` blocks for 30 seconds
   - Good: `mytool sync` queues work, daemon handles it async
   - Why: Blocks agent context, wastes tokens waiting

2. **Dumping all context upfront**
   - Bad: "Here's 5000 tokens about your project"
   - Good: "Call `mytool guidance <path>` when needed"

3. **Unstructured error messages**
   - Bad: `Error: something went wrong`
   - Good: `{"error_code": "AUTH_EXPIRED", "action": "run mytool login"}`

4. **Requiring human intervention**
   - Bad: "Press Y to continue"
   - Good: `--yes` flag for non-interactive mode

5. **Inconsistent output formats**
   - Bad: Sometimes JSON, sometimes text
   - Good: JSON default, explicit `--text` for human mode

6. **Polluting global namespace**
   - Bad: `--agent-specific-flag` in global help
   - Good: Flag scoped to relevant subcommand

7. **Inventing novel command names**
   - Bad: `mytool bootstrap-repository`
   - Good: `mytool init`

---

## Implementation Checklist

When adding new agent-facing features:

- [ ] Non-blocking (delegate long operations to daemon)
- [ ] Familiar naming (follows Unix/git/docker conventions)
- [ ] Default output is JSON
- [ ] Includes token count in metadata
- [ ] Supports `--text` for human debugging
- [ ] Supports `--review` for security audit
- [ ] Errors include actionable `action` field
- [ ] Non-interactive mode via `--yes` or similar
- [ ] Cached where appropriate
- [ ] Token budget documented
- [ ] Tested for fresh install invariant (no warnings on clean state)

---

## See Also

For project-specific implementations of these principles:
- [ox Agent UX Implementation](agent-ux-ox-implementation.md) - SageOx-specific implementation details
