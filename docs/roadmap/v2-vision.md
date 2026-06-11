---
audience: human
ai_editing: prohibited
preserve_voice: true
---

# SageOx V2 Vision

Future direction for ox, captured during MVP development.

## Strategic Themes

### 1. Agent Experience > Developer Experience

Focus on teaching agents, not humans. Agents are the new developers. By injecting infrastructure knowledge *before* code is written, we prevent problems rather than fix them.

### 2. Zero-Friction Infrastructure

Make infrastructure "I don't give a shit" - developers shouldn't think about it. Like Heroku, but for any cloud, inside any enterprise.

### 3. Enterprise Moat Through Integration

Deep integration with enterprise data (COEs, tickets, infra changes) creates defensibility. Checked-in SAGEOX.md means decisions are tracked and diffable.

## V2 Feature Priorities

1. **ox learn** - Scan existing infrastructure, extract patterns into SAGEOX.md
2. **sageox.ai API** - Centralized knowledge management
3. **CI/CD integration** - GitHub Actions for `ox review` on PRs
4. **MCP server** - Expand compatibility to more AI environments
5. **Enterprise SSO** - Required for large organization adoption

## Future Capabilities

| Category | Features |
|----------|----------|
| **Learning** | Real-time incident learning, git history analysis, multi-repo patterns |
| **Review** | Deep review mode, compliance checking (SOC2/HIPAA), cost analysis |
| **Multi-Cloud** | Provider detection, provider-specific guidance, multi-cloud patterns |
| **Enterprise** | Team-level policies, audit trails, role-based access |

## Adoption Path

Individual developer -> Team -> Organization -> Enterprise

Each level adds more value and stickiness.

## Open Questions

- Should `ox learn` run automatically or require explicit action?
- How should org-level and project-level policies merge?
- Do we need a daemon for frequent `ox prime` calls?
