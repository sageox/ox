---
name: ox-skill-manager
description: >-
  Manage SageOx Agent Skill bundles for this repository. Use when a user asks
  which ox skills are available, wants to install or repair Attest playbooks,
  or needs to understand how local coding agents receive team workflows.
---

## Manage bundled skills

1. Treat a file as managed only when `.sageox/skills.lock.json` records its
   installed digest. Inline ox stamps are migration evidence, not ownership.
2. `ox init` and `ox doctor --fix` install and reconcile the default `core`
   bundle for the project's selected native skill targets.
3. Install the opt-in Attest bundle with `ox attest install`. Using another
   `ox attest` command also attempts to install that bundle without making
   Attest unavailable when installation cannot run.
4. Canonical skill content lives in `extensions/skills/`. Claude Code receives
   a managed `.claude/skills/` copy; standard-compatible clients receive a
   managed `.agents/skills/` copy.
5. For a missing, stale, partially installed, or retired ox-managed file, run
   `ox doctor --fix`. It preserves user-owned additions and edits, and
   reconciles only project-selected targets and bundles.

Do not claim a team-synchronized skill has been installed unless the local ox
CLI reports a successful reconciliation. Team Context synchronization requires
an explicit managed bundle manifest and must never delete user-owned content.
