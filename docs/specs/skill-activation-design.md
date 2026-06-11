---
audience: ai
ai_editing: allowed
refreshable: true
---

# Skill Activation Design

Design principles for writing skills that agents can effectively discover and route to.

## Core Insight

> Skills should be written with the agent's routing needs in mind, not just human readability. The description field is the primary activation surface.

This insight comes from analyzing [beads PR #718](https://github.com/steveyegge/beads/pull/718), which consolidated 30 slash commands into a single SKILL.md with natural language activation.

## The Routing Problem

When a user says "help me with my API design", the agent must:

1. Scan available skills
2. Match user intent to skill capabilities
3. Activate the appropriate skill

The agent's primary signal for routing is the `description` field in skill frontmatter. If the description doesn't contain routing-relevant keywords, the skill may never be activated.

## Triggers Field

Skills can optionally specify explicit activation keywords via the `triggers` field:

```yaml
---
name: code-reviewer
description: Deep expertise for code review and quality
triggers:
  - code review
  - pull request
  - PR
  - quality
  - refactor
---
```

**Purpose:** Triggers provide explicit intent-routing keywords that agents use to match user requests to skills. While the description field is the primary activation surface, triggers offer a structured, machine-readable list of activation keywords.

**When to use triggers:**
- Keywords that users commonly search for
- Synonyms and alternative phrasings
- Domain-specific terminology
- Related tool names

**Guidelines:**
- Include the primary technology name (python, react, etc.)
- Add common synonyms (DB = database)
- Include related tools in the ecosystem
- Keep to 5-10 triggers for focus

## Description as Activation Surface

### Poor Description (Human-Only)

```yaml
name: code-reviewer
description: Code quality expertise
```

Problems:
- "Code quality expertise" doesn't mention review, PR, refactor, security
- Agent searching for "code review" won't match this skill
- No indication of when to use it

### Good Description (Agent-Aware)

```yaml
name: code-reviewer
description: |
  Deep expertise for code review, quality analysis, and refactoring.
  Use when: reviewing pull requests, auditing code changes, checking for
  vulnerabilities, enforcing style guidelines, or planning refactors.
```

Improvements:
- Contains searchable keywords: review, quality, pull request, audit, vulnerabilities, style, refactor
- "Use when" clause explicitly lists activation scenarios
- Agent can confidently route code review queries here

## Writing for Agents vs Humans

| Aspect | Human Docs | Agent Docs |
|--------|------------|------------|
| Style | Concise, progressive disclosure | Explicit, keyword-rich |
| Redundancy | Avoid | Embrace (synonyms help matching) |
| Structure | Narrative flow | Scannable sections |
| "Use when" | Implicit from context | Explicit list |

### The Dual-Audience Pattern

Skills serve both humans (who read the content) and agents (who route based on metadata). Structure accordingly:

```markdown
---
name: skill-name
description: |
  [AGENT-FACING: keyword-rich, explicit triggers]
  Use when: [scenario1], [scenario2], [scenario3]
---

# Skill Name

[HUMAN-FACING: concise, progressive disclosure, crafted voice]
```

## Token Budget Awareness

Context is finite. Skills that consume excessive tokens may:
- Be deprioritized by agents
- Crowd out other useful context
- Slow down inference

### Token Guidelines

| Category | Budget |
|----------|--------|
| Description | < 200 tokens |
| Full SKILL.md | < 3,000 tokens |
| With inline docs | Avoid; link instead |

### Progressive Disclosure Tiers

```
Tier 1: Description (always loaded)     ~100 tokens
Tier 2: Full SKILL.md (on activation)   ~2,500 tokens
Tier 3: Reference docs (on-demand)      external links
```

## Examples

### Task Management Skill

```yaml
name: task-tracker
description: |
  Manage development tasks with issue tracking and progress monitoring.
  Use when: creating tasks, updating status, viewing backlogs,
  managing dependencies, or checking blocked work.
  Keywords: issue, task, ticket, backlog, sprint, blocked, dependency
```

### Code Review Skill

```yaml
name: code-reviewer
description: |
  Automated code review for quality, security, and maintainability.
  Use when: reviewing pull requests, auditing code changes,
  checking for vulnerabilities, or enforcing style guidelines.
  Keywords: review, PR, pull request, audit, security, quality, lint
```

### Backend Skill

```yaml
name: backend-expert
description: |
  Backend development expertise for Node.js, Python, and Go.
  Use when: designing APIs, optimizing databases,
  managing authentication, setting up CI/CD, or troubleshooting performance.
  Keywords: API, REST, GraphQL, database, auth, Node.js, Python, Go
```

## Validation Checklist

When writing a skill description, verify:

- [ ] Contains primary keywords users would search for
- [ ] Includes "Use when" clause with specific scenarios
- [ ] Under 200 tokens
- [ ] No jargon without explanation
- [ ] Synonyms for key concepts (e.g., "task" and "issue")

## Related

- [beads PR #718](https://github.com/steveyegge/beads/pull/718) - Natural language skill activation
- `pkg/agentx/agent.go` - Skill struct definition
- `pkg/agentx/skills/parser.go` - SKILL.md parsing
