---
description: How to discover and use team-context rules and knowledge from the SageOx ox CLI
---
# Team Context — More Rules Live Outside This Repo

This repo uses SageOx. Behavioral rules and conventions that apply to your
whole team—not just this repo—live in your team's SageOx Team Context. That
repository is the source of truth. ox may place ignored, managed
`sageox-team-*` projections in `{{RULE_ROOT}}` when the active AI coworker can
preserve a rule's semantics. Never edit those projections; edit the Team
Context source instead.

## Where Team Rules Live

Find the Team Context repository path with `ox status` under `team_context`.
Its typical layout is:

    <team-context>/
      AGENTS.md                  # team-wide preamble
      MEMORY.md                  # team memory (already inlined into prime)
      agents/
        rules/
          <topic>.md             # one concern per file
          backend/postgres.md    # subdirectories supported
          frontend/react.md
        commands/                # read on demand, not slash commands
        profiles/                # AI coworker profiles
      discussions/               # archived team meetings
      memory/                    # daily/weekly/monthly summaries
      documents/                 # imported docs

## How to Discover and Read Them

Each rule has exactly one delivery path for the active AI coworker:

- Native-compatible rules projected into `{{RULE_ROOT}}` load there and are
  omitted from `ox agent prime`.
- Other `visibility: always` rules are inlined by prime.
- `visibility: indexed` rules are cataloged by prime with their name,
  description, and path, then read on demand.

`ox agent prime` also includes Team AGENTS.md / CLAUDE.md and Team MEMORY.md.
To read an indexed Team Rule, use the Read tool with the absolute path shown in
prime's `<team-rules>` block.

To search team-wide knowledge:

- `ox query "<question>"` searches discussions, sessions, and documents.
- `ox agent team-ctx` returns distilled Team Context for AI coworkers.

Run `ox guide team-rules` before authoring or promoting a rule.

## When You Write a Project-Local Rule

If a coworker adds or edits a project-local rule that looks generally
applicable—not specific to this repo's paths, services, or schemas—ask whether
to publish it under `<team-context>/agents/rules/`. Never publish silently.
Repo-specific rules stay local.

Team Rules reach every supported AI coding coworker used by teammates running
ox. A project-local native rule reaches only that tool. That asymmetry is why
durable conventions belong in Team Context.

## Why This Pointer Still Exists

Native projection is an optimization, not the catalog. This pointer covers
indexed rules, delivery fallbacks, Team Context navigation, and the authoring
workflow. ox owns continuous projection cleanup and the reserved
`sageox-team-*` namespace; the Team Context file remains canonical.
