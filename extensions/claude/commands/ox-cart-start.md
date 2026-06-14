<!-- Keep this file thin. Portable behavioral guidance (the naming intent)
     lives in the ox CLI JSON output (guidance field), not here — ox serves
     all agents (Codex, Droid). Only the host-specific /rename mechanism is a
     legitimate Layer-2 note below. -->
Claim a cart and start working on it.

## Post-Command

Follow the `guidance` field in the JSON output.

Layer-2 (Claude Code only): apply the name from `guidance` to this session via
`/rename` with a kebab-case name derived from the cart title (e.g. cart title
"Auth middleware rate limiting" → `/rename auth-rate-limiting`).

$ox carts start $ARGUMENTS --json
