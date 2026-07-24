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

## The Thin/Thick Convention (one rule, three names)

"Keep ox-* skills thin" is not three policies — it is one rule with three names. **Thin-relay == cross-agent-conformance == staleness-safety.** They describe the same boundary in the [two-layer skill-injection model](../adr/ADR-023-skill-injection-two-layer-model.md): the deterministic behavioral floor lives in **Layer 1** (the live `ox` CLI's JSON/text output, piped into agent context via the prime marker) and reaches every adapter; **Layer 2** is the agent-specific shell (Claude skills/commands, Codex hooks, Droid rules) and is strictly additive.

### Governing rule

> The deterministic floor lives in Layer 1 (CLI output). Agent-specific mechanisms are strictly additive — never load-bearing.

### Why the three names are one boundary

- **Thin-relay → cross-agent conformance.** Any behavioral guidance authored only in a Claude command/skill body is, by construction, invisible to Codex (installs hooks only, `cmd/ox-adapter-codex/main.go`) and Droid (installs hooks + rules, no commands, `cmd/ox-adapter-droid/main.go`). Those adapters install no command files, so a thin-relay violation **IS** a cross-agent gap: the Codex/Droid user silently loses behavior the Claude user gets.
- **Layer 1 → staleness safety.** Layer 1 is always the live binary; it cannot drift from the code. Installed Layer-2 command files are copied from `extensions/claude/commands/*.md` (embedded via `extensions/claude/embed.go` `CommandFS`) and stamped with an `ox-hash`+version marker (`oxStampPrefix` in `cmd/ox-adapter-claude-code/commands.go`). A copied body **can** go stale; the binary's `guidance` **cannot**. Keeping floor behavior in Layer 1 is what the staleness stamp protects.

### Author decision checklist

For each piece of content you are about to author, route it by the first matching rule:

- **(a) Is this floor / behavioral guidance** — use-when triggers, post-command branching, error handling, consult cues? → It belongs in **Layer 1**: the ox subcommand's JSON `guidance` field, NOT the skill body.
- **(b) Is it behavioral AND backed by a single ox subcommand?** → **Thin relay only.** The body is a pointer to `ox <subcommand> --json` plus a one-line description. No behavioral copy in the body.
- **(c) Is it agent-side orchestration NOT backed by a single ox subcommand** — a Claude-specific rendering spec or a multi-step flow? → **Thickness is ALLOWED, and only here.** See the sanctioned examples below.
- **(d) Could a Codex or Droid user need this?** → Then it **MUST** reach Layer 1, regardless of (a)–(c). If it only lives in a Claude body, it is trapped floor behavior and must be moved.

### Content kind → layer → mechanism

| Content kind | Layer | Mechanism |
|--------------|-------|-----------|
| Use-when / activation triggers (behavioral) | Layer 1 | ox subcommand JSON `guidance` |
| Post-command branching, error handling | Layer 1 | ox subcommand JSON `guidance` |
| Consult-first cues, intent floor | Layer 1 | prime-marker reminder + `guidance` |
| Pointer to a single ox subcommand | Layer 2 (thin) | skill body relays `ox <subcommand> --json` |
| Claude rendering spec / multi-step flow not backed by one subcommand | Layer 2 (thick, gated) | skill body carries the orchestration |

### The allowed-thickness gate

A skill body may be thick **only when ALL of the following hold**:

1. The content is **not backed by a single ox subcommand** (no relay would capture it).
2. It is **Claude-specific orchestration or a rendering spec** — agent shell ergonomics, not portable behavior.
3. It **traps no floor behavior** — nothing a Codex/Droid user would also need lives only here.

Two skills are the sanctioned thick examples:

- `extensions/claude/skills/ox-plan/SKILL.md` — carries the **judgment-badge authoring flow**: reasoning the `ox plan enrich` context bundle into cited, section-anchored badges, plus authoring the plan markdown the renderer draws. (The HTML itself is produced by `ox plan render` in the binary — that part is NOT skill content; the skill must not hand-author HTML.)
- `extensions/claude/skills/ox-session-review/SKILL.md` — carries the **audit + regeneration flow**, which is not backed by a single ox subcommand.

Everything else is thin. If you are unsure whether content clears the gate, it does not: route it to Layer 1.

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
