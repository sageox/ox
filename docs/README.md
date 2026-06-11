# ox documentation

Docs are organized by **topic**, not by audience. Each directory below is a single subject area; start with the anchor docs, then browse the folder for depth.

| Topic | What lives here | Start with |
|-------|-----------------|------------|
| [`adr/`](adr/) | Architecture Decision Records — the *why* behind structural choices, numbered and dated | [`ADR-001-pure-go-no-cgo.md`](adr/ADR-001-pure-go-no-cgo.md) |
| [`specs/`](specs/) | Design specs and technical references for features and subsystems | [`ipc-architecture-overview.md`](specs/ipc-architecture-overview.md), [`go-conventions.md`](specs/go-conventions.md), [`agent-ux-principles.md`](specs/agent-ux-principles.md) |
| [`architecture/`](architecture/) | System architecture deep-dives spanning multiple components | [`session-capture-architecture.md`](architecture/session-capture-architecture.md) |
| [`guides/`](guides/) | How-to guides for working on ox: philosophy, testing, authoring adapters and skills | [`development-philosophy.md`](guides/development-philosophy.md), [`testing-philosophy.md`](guides/testing-philosophy.md) |
| [`design/`](design/) | TUI component catalog and the visual design system | [`design/README.md`](design/README.md) |
| [`security/`](security/) | **User-facing**: what ox redacts from your data before it leaves your machine | [`credential-redaction.md`](security/credential-redaction.md), [`redaction-policy.md`](security/redaction-policy.md) |
| [`reference/`](reference/) | Generated CLI command reference — **do not hand-edit** (regenerated from cobra) | [`reference/index.mdx`](reference/index.mdx) |
| [`coes/`](coes/) | Correction-of-error postmortems | [`2026-04-07-multi-node-write-conflicts.md`](coes/2026-04-07-multi-node-write-conflicts.md) |
| [`analysis/`](analysis/) | Point-in-time investigations and write-ups | [`february-2026-ipc-analysis.md`](analysis/february-2026-ipc-analysis.md) |
| [`roadmap/`](roadmap/) | Product vision and forward-looking plans | [`v2-vision.md`](roadmap/v2-vision.md) |
| [`adr-grounding/`](adr-grounding/) | Source material that grounds ADR decisions | [`project-origin-transcript.md`](adr-grounding/project-origin-transcript.md) |
| [`concepts/`](concepts/) | Conceptual primers | [`agent-dimensions.mdx`](concepts/agent-dimensions.mdx) |
| [`examples/`](examples/) | Worked examples (e.g. a sample Team Context) | [`examples/team-claude/`](examples/team-claude/) |

## Conventions

- **`reference/` is generated.** Fix command docs in `cmd/ox/*.go`, then regenerate — never edit the `.mdx` files directly.
- **`design/` is owned by its own system.** Palette and tokens flow from `sageox-design` upstream; see [`design/README.md`](design/README.md) and `.claude/rules/design.md`.
- **ADRs record decisions, not status.** Once merged, an ADR is a historical record — supersede it with a new ADR rather than rewriting it.
- **Two security homes, by audience.** `docs/security/` is user-facing — what ox redacts from your data before it leaves your machine. The repo-root [`security/`](../security/) is contributor-facing — the security-review pipeline and threat model. Public ox security docs cover only client-side, user-verifiable behavior.
