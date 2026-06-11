# Acceptance Specs

BDD specifications for the `ox` CLI's developer- and coworker-facing flows.
These are natural-language specs of *what a developer or AI coworker
experiences* when they use `ox` — installing it, authenticating, onboarding a
repo, priming a session, recording sessions, enriching plans with team context,
murmuring to teammates, querying the Ledger, exploring code, inspecting
Knowledge Bubbles, and upgrading — written in the same style as the SageOx
corpus the rest of the platform uses.

These specs cover the **experience**, not implementation completeness. They are
a behavioral contract a reviewer can read end to end and recognize as "yes,
that is how ox should feel."

## Status

Content-only. There is **no runner wired** in this repo yet. These `.feature`
files have no step definitions, no executor, and no CI integration. They are
deliberately valuable on their own: a human-readable contract of ox's
user-facing behavior that survives independent of any test harness.

Wiring is **deferred until a CLI-oriented BDD runner exists** — one that can
drive the `ox` binary, a hermetic SageOx API double, and a scratch git repo,
and assert on what a coworker sees at the terminal and in-session. When that
runner ships, follow-up issues will bind these scenarios to the real command
surfaces (`cmd/ox/*.go`) and a SageOx API twin.

## Structure

```text
acceptance/
  install/                # build & install, ox on PATH, version, first run
  auth/                   # login, logout, device-code / headless login, tokens
  onboarding/             # ox init, commit .sageox/, ox status, ox doctor
  priming/                # ox agent prime at start / after compaction / clear
  session-recording/      # auto-record on prime, pause/resume, list/view, finalize
  plan-enrichment/        # enrich-while-drafting, render, the review loop, nudges
  murmur/                 # publishing WIP, whisper delivery to other coworkers
  team-context/           # ox agent team-ctx, ox query, searching team knowledge
  code-intelligence/      # ox code search, ox code insights before planning
  knowledge-bubbles/      # ox kb list / inspect / locate bubbles
  upgrade/                # ox upgrade, version-mismatch detection, doctor self-heal
  business-actions/       # named user journeys (one-paragraph stubs in v1)
  system-interactions/    # feature -> ox subcommand / SageOx API touchpoint map
  glossary.md             # ox domain terminology (canonical SageOx terms)
  personas.md             # test actor defaults
```

## Three Layers

| Layer | Files | Changes When |
|-------|-------|-------------|
| Business Rules | `*.feature` files | Developer/coworker-facing behavior changes |
| Business Actions | `business-actions/*.md` | Named user journeys change |
| System Interactions | `system-interactions/*.md` | ox subcommands or SageOx APIs change |

## Conventions

Mirrors the SageOx acceptance corpus exactly:

- `Feature:` opens with a 2-4 sentence prose description of the user-facing
  behavior, then `See also:` cross-references to business-actions and related
  features.
- `Rule:` blocks group scenarios by capability or invariant; **no
  `Background:`** blocks.
- Personas are named (`Devon`, `Avery`, `Sam`, `Riley`, `Quinn`); no
  anonymous `the user`. Each scenario name begins with the actor + action.
- Scenario language describes what the developer or AI coworker **sees and
  does** at the CLI or in-session — not ox internals. Say "the plan is
  rendered as a SageOx team-context-optimized HTML page", not the name of the
  Go function that renders it. Scenarios never reference Go function names,
  file paths, or internal structs.
- `Scenario Outline` + `Examples` for parametrized flows.

## Scope discipline

Specs cover the **developer/coworker experience**, not implementation
completeness. We deliberately do **not** exhaust every internal error code,
daemon state, or redaction edge the user only ever sees as "something went
wrong, here's how to recover." Those live in the Go unit, integration, and E2E
tests under `cmd/ox/*_test.go` and `internal/**/*_test.go`. An acceptance
scenario earns its place only when it captures a beat a coworker would notice.

## Divergences from the full SageOx corpus (by design, for v1)

- `system-interactions/` is a single `touchpoints.md` map here (feature ->
  subcommand / SageOx API), not a per-endpoint subdirectory split. A future PR
  can expand into per-command detail files.
- `business-actions/` are one-paragraph stubs, not full Actor / Goal / Steps /
  Outcome narratives. A future stakeholder-readout PR can flesh them out.
- No `skills/` subdirectory: no CLI BDD runner has been extracted yet, so there
  is nothing to install in this repo.

## Running specs

Deferred until a CLI-oriented BDD runner ships. Once it does, follow-up issues
will wire these specs to:

- The real `ox` binary built by `make build` / `make install`
- A hermetic SageOx API double (device-code OAuth, `/api/v1/cli/repos`,
  `/api/v1/kb`, team-context and ledger git remotes, search)
- A scratch git repo per scenario so onboarding, priming, and recording run
  without touching a developer's real configuration
