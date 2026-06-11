# SageOx Demo - One agent session, every wow moment.
#
# A single continuous coding-agent session that walks through:
#   - automatic session recording (on prime)
#   - "what's the team up to?" — live in-flight work from coworkers' murmurs,
#     before anyone has committed, merged, or opened a PR
#   - discussion + session recall across agents, machines, and teammates (query)
#   - a WIP murmur out + a teammate's murmur arriving live, surfaced in plain
#     language; the agent adapts
#   - plan enrichment from team context (collisions, prior art, expert routes)
#
# Shows the magic, not the internals — no raw <system-reminder> blocks.
# Mock Claude/cloud legs live in demo/mock/ for reproducible recording.
#
# Record:  vhs demo/demo.tape
# Output:  demo/demo.gif

Output demo/demo.gif

Set Shell "bash"
Set FontSize 14
Set Width 1200
Set Height 940
Set Theme "Catppuccin Mocha"
Set Padding 20
Set Framerate 24
Set TypingSpeed 40ms

Hide
Type "export PS1='$ '"
Enter
Type@0 "export PATH=$(pwd)/demo/mock:$PATH"
Enter
Type "clear"
Enter
Show
Sleep 500ms

Type "claude"
Enter
Sleep 2500ms

# Beat 1 — what's the team doing right now (pre-PR, pre-commit)
Type "What's the team working on right now?"
Enter
Sleep 7s

# Beat 2 — recall prior work across agents, machines, teammates
Type "Didn't someone get the rate limiter working a few days ago?"
Enter
Sleep 7s

# Beat 3 — build it; murmur out, inbound murmur, enriched plan
Type "Great — add it to the auth endpoints."
Enter
Sleep 8s

# Long hold so the final plan-enrichment screen is fully readable
Sleep 7s
