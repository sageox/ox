<!-- doc-audience: human -->
# AI Coworker Compatibility

ox works with multiple AI coding agents. Support depth varies by agent — here's what works where.

## Support Tiers

| Tier | What It Means |
|------|---------------|
| **Gold** | Full feature parity. Real-time session recording, hooks, whispers, anti-entropy recovery. Tested in CI. |
| **Silver** | Core features work. Hooks fire, context primes correctly, basic session recording. Tested weekly. |
| **Bronze** | Context injection via AGENTS.md works. Session recording via manual start/stop. Limited or no hooks. |

## Feature Availability

| Feature | Claude Code | Codex | Gemini | Amp | Pi | OpenCode |
|---------|:-:|:-:|:-:|:-:|:-:|:-:|
| Context Prime | Gold | Silver | Silver | Bronze | Bronze | Bronze |
| Session Recording | Gold | Silver | Silver | — | — | — |
| Whispers | Gold | — | Silver | — | — | — |
| Hooks | Gold | Silver | Silver | — | — | — |
| MCP | Gold | — | — | — | Bronze | — |
| Anti-Entropy | Gold | — | — | — | — | — |

## Quick Start by Agent

### Claude Code (Gold)
```bash
ox init        # sets up everything automatically
ox doctor      # verifies hooks + context injection
```
No extra setup needed — Claude Code is the reference implementation.

### Codex
```bash
ox init
ox integrate install --codex
codex features enable codex_hooks  # enable hook support in Codex
```

### Gemini CLI
```bash
ox init
ox integrate install --gemini
```

### Amp
```bash
ox init
ox integrate install --amp
```
Amp support is context-injection only (AGENTS.md marker). No native hooks yet.

### Pi
```bash
ox init
ox integrate install --pi
```
Pi reads AGENTS.md, CLAUDE.md, and SYSTEM.md natively. MCP server support available.

### OpenCode
```bash
ox init
ox integrate install --opencode
```
OpenCode has 27+ plugin events but ox currently uses only `session.created`.

## Known Limitations

- **Amp**: No native hook events for session lifecycle. Context works via AGENTS.md marker only.
- **OpenCode**: Sessions stored in SQLite, not files. Real-time tail watching requires SQLite adapter (not yet implemented).
- **Pi**: Extension-based architecture, not shell hooks. MCP works but hook lifecycle events aren't available.
- **code_puppy**: Single plugin event. Minimal integration surface.

## Version Requirements

All agents should be at their latest version for best compatibility. Run `ox doctor` to check for version issues.

## Detailed Matrix

For test-level compatibility data including version history, generate the full matrix:

```bash
make compat-matrix   # produces docs/compatibility.html
open docs/compatibility.html
```
