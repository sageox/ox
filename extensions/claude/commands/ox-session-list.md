<!-- Keep this file thin. Behavioral guidance (use-when, common-issues, errors)
     belongs in the ox CLI JSON output (guidance field), not here.
     Skills are agent-specific wrappers; ox serves all agents (Codex, etc.). -->
List recent sessions from the project ledger and offer to view one.

## Steps

Run `$ARGUMENTS` (or `ox session list --limit 5` if no arguments are given),
then present the results and ask which session to view. Follow the JSON
`guidance` field to view a session (`ox session view <name>`) and to hydrate
dehydrated entries first.
