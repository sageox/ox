# Team Claude Index Format

The `index.md` file provides token-optimized descriptions for agents, commands, and skills.
This reduces context usage when injecting team customizations into Claude Code sessions.

## Purpose

When `ox agent prime` discovers team customizations, it reads descriptions to help Claude
understand what each agent/command/skill does. Full file descriptions can be verbose.
The `index.md` file provides brief, curated descriptions optimized for context efficiency.

## Supported Formats

The parser supports two markdown formats:

### List Format (Recommended)

```markdown
# Agents

- **agent-name**: Brief token-optimized description
- **other-agent**: Another description

# Commands

- **deploy**: Deploy to staging or production
- **review-pr**: Review pull request with team patterns
```

### Table Format

```markdown
# Agents

| Name | Description |
|------|-------------|
| agent-name | Brief token-optimized description |
| other-agent | Another description |
```

## Guidelines

### Keep descriptions concise

- **Target: 40-60 characters** - enough for Claude to understand when to use
- Lead with capability: "AI orchestration expert" not "Expert in AI orchestration"
- Omit obvious context: Don't repeat "for this team" or "custom"

### Good examples

```markdown
- **ai-orchestration-architect**: Design multi-agent workflows and pipelines
- **database-reviewer**: Review schema changes and migrations
- **deploy**: Deploy to staging or production environment
```

### Bad examples

```markdown
- **ai-orchestration-architect**: This is a custom agent that helps the team design AI orchestration systems including multi-agent workflows, pipelines, and complex agentic systems
- **database-reviewer**: A specialized code reviewer for our team that focuses on database-related changes
```

## Fallback Behavior

If an agent/command/skill file exists but is NOT listed in `index.md`:

1. The CLI reads the file's frontmatter `description` field
2. This may be longer/more detailed than desired for context efficiency
3. Best practice: keep `index.md` complete and up-to-date

## Directory Structure

```
<team_ledger>/coworkers/ai/claude/
├── agents/
│   ├── index.md              # Token-optimized agent descriptions
│   ├── ai-orchestration.md   # Full agent definition
│   └── database-reviewer.md
├── commands/
│   ├── index.md              # Token-optimized command descriptions
│   ├── deploy.md             # Full command definition
│   └── review-pr.md
└── skills/
    ├── index.md              # Token-optimized skill descriptions
    ├── database-design.md    # Full skill definition
    └── code-refactoring.md
```

## Implementation Reference

- Parser: `internal/claude/index.go`
- Functions: `ParseIndex()` (list format), `ParseIndexWithTable()` (table format)
- Used by: `DiscoverAgents()`, `DiscoverSkills()`, `DiscoverTeamCommands()`
