<!-- ox-hash: 6bdc2bd3da8e ver: 0.14.1 -->
<!-- Keep this file thin. Behavioral guidance (disclosure ladder, id forms,
     pinning, errors) belongs in the ox CLI JSON output (guidance field) and
     `ox guide conversations`, not here.
     Skills are agent-specific wrappers; ox serves all agents (Codex, etc.). -->
Read recorded team conversations locally: list, summaries, distillation topics, and transcript slices.

## Steps

Run `ox conversation $ARGUMENTS` (or `ox conversation list` if no arguments
are given), then present the results. Follow the JSON `guidance` field to
descend the disclosure ladder (`show` → `topics` → `topic` → `transcript`);
a full `sageox://` citation URI (quoted) works as an id and retrieves the
cited transcript slice. Full workflow: `ox guide conversations`.
