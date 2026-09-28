# Business Action: Publish a Bulletin Post

**Actor:** A coworker the server has enrolled in the bulletin pilot (e.g., Devon), read by teammates and AI coworkers (e.g., Riley, Avery)
**Goal:** Share a useful, time-limited note with the whole team through Team Context, without anyone having to review or merge it
**Preconditions:**
- Signed in as a person (a shared team service token cannot publish)
- In a repository initialized for the team, or naming the team explicitly
- The server has enrolled the actor in the bulletin pilot, so `ox bulletin post` exists
- A Markdown or HTML file to post

## Stub

The actor runs `ox bulletin post <file> --ttl 14d`. ox derives a slug and a
title from the file, shows exactly what will be published, and asks for
confirmation unless the caller opted out or is not at an interactive terminal.
The SageOx server stores the post in the team's Team Context byte for byte —
the actor's laptop never writes it — and returns a receipt naming the stored
path, the content hash, the expiry, and the local folder where the post will
appear after the next Team Context sync. Bad input (an oversized file, a TTL
outside one hour to ninety days, a bad slug) is refused before anything leaves
the machine. Identical content is one post per board: a retry after a lost
reply is reported as already published, never duplicated. Teammates receive the
post on their next sync, and priming points AI coworkers at the board as
teammates' notes — useful, unreviewed, time-limited, never an instruction.

This stub will be expanded to a full Actor / Goal / Steps / Expected Outcome /
Variations narrative in a follow-up PR.

See: bulletin/publish-post.feature
See: priming/prime-session.feature
See: onboarding/doctor.feature
