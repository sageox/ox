---
audience: human
ai_editing: prohibited
preserve_voice: true
---

# Testing Philosophy

Our approach to testing ox.

## Core Principle

> **The goal is the RIGHT tests, not MORE tests.**

A test is valuable if it:
1. Catches a bug you'd actually ship
2. Documents expected behavior
3. Runs fast enough that you won't skip it
4. Isn't duplicated elsewhere

## Test Pyramid

```
              E2E (~5%)       ← Deploy only
           Integration (~15%) ← PR gate
         Unit Tests (~80%)    ← Every commit
```

| Tier | Speed Target | When to Run |
|------|--------------|-------------|
| Unit | < 5 seconds | Every file save, pre-commit |
| Integration | < 30 seconds | Pre-push, PR checks |
| E2E | 1-5 minutes | Pre-deploy, nightly |

## Target Ratio

~1:1 test LOC to production LOC. More than 2:1 is over-engineered.

## What to Test

| Priority | Category | Examples |
|----------|----------|----------|
| **High** | Core business logic | `ox init`, `ox doctor --fix` |
| **High** | Data corruption paths | Config files, git operations |
| **Medium** | Edge cases from bugs | Production issues |
| **Low** | Display/formatting | Manual verification OK |

## What NOT to Test

- Simple utility functions (trust the language)
- Every input permutation (use table-driven tests)
- Obvious behavior ("if file exists, return true")
- Same logic via different entry points

## Before Adding a Test

Ask:
1. Does this test already exist?
2. Am I testing behavior or implementation?
3. Can this be table-driven?
4. What tier does this belong to?
5. Will it run fast enough to use constantly?

---

*For implementation patterns, anti-patterns, and code examples, see [ai/specs/testing-implementation.md](../specs/testing-implementation.md).*
