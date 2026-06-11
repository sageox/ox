# Shared Test Personas

These personas carry sensible defaults so that scenarios only need to mention
details relevant to the business rule being tested. When a persona appears in a
scenario, all attributes below are assumed unless explicitly overridden.

These personas are CLI- and session-flavored — anchored in how a developer or
an AI coworker interacts with `ox` at the terminal and inside a coding session,
not a web-app role.

---

## Devon the Onboarding Developer

**Role:** A developer bringing a repo onto SageOx for the first time, and the
default "someone installed ox and ran a command" actor for setup flows.

**Default State:**
- Has a verified SageOx account
- Email: devon@example.com
- Full name: Devon Park
- Works in a git repository on their own machine
- Is a member of "Acme Engineering" (the default team)
- Comfortable at a terminal; installs ox via `make build && make install` or a
  package manager, and runs `ox login`, `ox init`, `ox status`, `ox doctor`
- Wants the setup to be obvious and self-healing, not a manual checklist

**Relevant For:** Install, login/logout, `ox init`, committing `.sageox/`,
`ox status`, `ox doctor`, upgrade, version-mismatch detection.

---

## Avery the AI Coworker

**Role:** An AI coding agent running inside a session in Devon's repo. The
default "the agent is mid-session" actor. Avery primes at session start, drafts
plans, enriches them with Team Context, records the session, and murmurs to
teammates.

**Default State:**
- Runs in a repo that is already initialized for "Acme Engineering"
- Has an agent ID issued by `ox agent prime`
- Primes at session start, after compaction, and after a context clear
- Drafts implementation plans in plan mode, then presents them to Devon
- Calls `ox plan enrich` while drafting, `ox code search` / `ox code insights`
  before planning, and `ox query` / `ox agent team-ctx` to pull team knowledge
- Publishes murmurs at the start of significant work and after architectural
  decisions

**Relevant For:** Priming, session recording lifecycle, plan enrichment and the
review loop, murmuring, team-context queries, code intelligence, Knowledge
Bubbles.

---

## Sam the Team Admin

**Role:** Sets up and maintains the team's SageOx presence: pairs the repo's
endpoint, manages Team Context and expert coworkers, owns the upgrade cadence.

**Default State:**
- Has a verified SageOx account with admin rights for "Acme Engineering"
- Email: sam@example.com
- Full name: Sam Rivera
- Decides which endpoint the team's repos point at
- Curates Team Context: conventions, decisions, and expert coworkers the team
  shares
- Owns the "everyone should be on the same ox version" rollout

**Relevant For:** Endpoint selection at login, Team Context curation, expert
coworker management, upgrade cadence, doctor-driven fleet consistency.

---

## Riley the Teammate

**Role:** Another coworker on "Acme Engineering" — human or AI — who *consumes*
the context Avery and Devon produce. Riley hears murmurs as whispers, recalls
prior sessions, and reads enriched plans.

**Default State:**
- Is a member of "Acme Engineering"
- Email: riley@example.com
- Full name: Riley Kim
- Works in the same repo (or a sibling repo on the same team) as Avery
- Receives murmurs published by other coworkers as whispers in-session
- Recalls prior sessions and decisions via `ox query` and priming

**Relevant For:** Whisper delivery, cross-session recall, reading shared plans
and Team Context, the consuming side of murmurs and the Ledger.

---

## Quinn the Offline Developer

**Role:** A developer working with flaky or absent network — on a plane, behind
a captive portal, in a devcontainer that hasn't propagated credentials yet.
Exists to keep scenarios honest about ox degrading gracefully.

**Default State:**
- Has a SageOx account but an unreliable or absent connection
- Email: quinn@example.com
- Full name: Quinn Alvarez
- Expects local-only operations (recording capture, plan enrichment) to keep
  working offline and to sync when the network returns
- Does not want a network blip to surface as a scary error

**Relevant For:** Offline plan enrichment, headless login fallbacks, murmurs
queuing durably when the daemon is unreachable, doctor's reassurance that
deferred sync is safe.

---

## Acme Engineering

The default team used in scenarios. Has Team Context (conventions, decisions,
expert coworkers), an active Ledger of prior sessions and plans, and members
Devon, Avery, Sam, Riley, and Quinn.
