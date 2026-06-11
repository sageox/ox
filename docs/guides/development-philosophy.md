---
audience: human
ai_editing: prohibited
preserve_voice: true
---

# Development Philosophy

Core principles guiding ox development.

## Quality Workflow

1. **Before coding**: Review existing patterns, check conventions
2. **After implementing**: Review for over-engineering, run `make lint` and `make test`
3. **Before merging**: Tests pass, security checked, backward compatible

## Simplicity First

> The right amount of complexity is the minimum needed for the current task.

**Do**
- Write code that solves the current problem
- Add abstraction only when you have 3+ concrete uses
- Keep solutions straightforward

**Don't**
- Add features "for the future"
- Create abstractions for single-use cases
- Write defensive code for impossible scenarios

**Signs of over-engineering**
- Interfaces with only one implementation
- Config options nobody uses
- Layers of abstraction for simple operations

## Git Workflow

- Clear, descriptive commit messages using conventional commits (`feat:`, `fix:`, `docs:`)
- One logical change per commit
- `main` = stable, `mvp` = development

## Documentation

- Document "why", not "what"
- Keep README focused on getting started
- Include examples for common use cases

---

*For detailed Go conventions, logging patterns, and CLI design specs, see [ai/specs/go-conventions.md](../specs/go-conventions.md).*
