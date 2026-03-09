package constants

// MaxInlineContextLines caps how many lines are read from files inlined
// into agent context (e.g., MEMORY.md, coworkers/agents/AGENTS.md).
// Forces teams to distill instructions down to a reasonable context size.
const MaxInlineContextLines = 200
