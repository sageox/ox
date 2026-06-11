---
audience: human
ai_editing: prohibited
preserve_voice: true
---

# Skill Authoring Guide

How to write effective skills that agents can discover and use.

## The Key Insight

> Skills should be written with the agent's routing needs in mind, not just human readability.

When a user says "help me with database migrations", the agent scans skill descriptions to find matches. If your description doesn't contain the right keywords, your skill won't be activated.

## Quick Start

```yaml
---
name: my-skill
description: |
  What this skill does in one sentence.
  Use when: scenario1, scenario2, scenario3.
  Keywords: term1, term2, term3
version: "1.0.0"
author: Your Name
tags:
  - category1
  - category2
---

# My Skill

Instructions for the agent...
```

## Description Formula

**Bad** (human-only):
```yaml
description: Infrastructure expertise
```

**Good** (agent-aware):
```yaml
description: |
  Deep expertise for Python testing and quality assurance.
  Use when: writing pytest tests, configuring fixtures, mocking dependencies,
  measuring coverage, debugging test failures, or setting up CI test pipelines.
  Keywords: pytest, fixtures, mocking, coverage, unit tests, integration tests
```

The difference: keywords and explicit activation scenarios.

## Token Budget

Keep skills context-efficient:

| Component | Budget |
|-----------|--------|
| Description | < 200 tokens |
| Full SKILL.md | < 3,000 tokens |

**Tips:**
- Link to reference docs instead of embedding them
- Use progressive disclosure (summary first, details later)
- Remove redundant explanations

## Required Fields

| Field | Required | Purpose |
|-------|----------|---------|
| `name` | Yes | Identifier (lowercase, hyphens ok) |
| `description` | Yes | Agent routing signal |
| `version` | No | Semver for tracking |
| `author` | No | Attribution |
| `tags` | No | Categorization |
| `triggers` | No | Explicit activation keywords |

## Checklist

Before publishing:

- [ ] Description contains searchable keywords
- [ ] "Use when" clause lists specific scenarios
- [ ] Under 3,000 tokens total
- [ ] Name is lowercase with hyphens only
- [ ] Includes common synonyms (e.g., "task" and "issue")

## Common Mistakes

1. **Vague descriptions** - "Helps with development" tells the agent nothing
2. **Missing keywords** - If users say "database" but your description says "DB", you miss matches
3. **Too much content** - Embedding full reference docs bloats context
4. **No activation hints** - Without "Use when", agents guess when to use your skill

---

*For detailed design rationale, see [ai/specs/skill-activation-design.md](../specs/skill-activation-design.md).*
