---
name: monitor-pr
description: >
  Watch an open pull request for new CI failures and review comments and
  drive it to a clean state. Uses the Monitor tool to stream PR status
  changes in real time so reactions are immediate instead of polled.
  Handles CodeRabbit nitpicks with judgment (do not blanket-skip) and
  treats "out of date" comments as still-relevant until proven otherwise.
  Replies to and resolves each thread as it's addressed. Triggers:
  "monitor the PR", "watch the PR for comments", "keep an eye on the PR",
  "address PR feedback as it lands", /monitor-pr.
---

# monitor-pr

Drives a pull request to green by **streaming** its state through the
`Monitor` tool and reacting the moment a check fails or a new review
comment lands. Designed for CodeRabbit-reviewed PRs but works for human
reviewers too.

## Architecture: use the `Monitor` tool, not a sleep loop

**This is load-bearing — do not substitute a Bash `sleep` loop.** A loop
in a single `Bash` call blocks the agent until it exits, so the agent
can't react to events concurrently and misses interleaved work.
`Monitor` streams each stdout line as a notification into the
conversation, so the agent keeps full agency between events.

Start exactly one monitor at the top of the task with:

- `persistent: true` — PR reviews can take hours; don't let a timeout
  kill the watch mid-review.
- `description` — specific, e.g. `"PR #493 state changes"`, because it
  appears in every notification.
- The polling script below as `command`.

Stop the monitor with `TaskStop` only when the exit condition is met or
the user cancels.

## The polling script

Resolve PR metadata first (once, before starting the monitor):

```bash
gh pr view --json number,url,headRepository,headRepositoryOwner,baseRepository
```

Then start the `Monitor` with this command (substitute `PR`, `OWNER`,
`REPO`). It emits **one line only when state changes** — a quiet PR
produces zero events, an eventful PR produces one event per transition.

```bash
PR=<number>; OWNER=<owner>; REPO=<repo>
last=""
while true; do
  checks=$(gh pr checks "$PR" --json bucket 2>/dev/null || echo '[]')
  failing=$(jq '[.[] | select(.bucket=="fail" or .bucket=="cancel")] | length' <<<"$checks")
  pending=$(jq '[.[] | select(.bucket=="pending")] | length' <<<"$checks")
  unresolved=$(gh api graphql --paginate -f query='
    query($o:String!,$r:String!,$n:Int!,$endCursor:String){
      repository(owner:$o,name:$r){
        pullRequest(number:$n){
          reviewThreads(first:100, after:$endCursor){
            nodes{ isResolved }
            pageInfo{ hasNextPage endCursor }
          }
        }
      }
    }' -f o="$OWNER" -f r="$REPO" -F n="$PR" 2>/dev/null \
    | jq -s '[.[].data.repository.pullRequest.reviewThreads.nodes[]
             | select(.isResolved==false)] | length')
  state="fail=${failing:-?} pending=${pending:-?} unresolved=${unresolved:-?}"
  if [ "$state" != "$last" ]; then
    if [ "$failing" = "0" ] && [ "$pending" = "0" ] && [ "$unresolved" = "0" ]; then
      echo "clean: all checks pass, none pending, zero unresolved threads"
    else
      echo "change: $state — triage needed"
    fi
    last="$state"
  fi
  sleep 60 || exit 0
done
```

Monitor discipline:
- **60s poll floor.** Don't drop below; you'll rate-limit yourself against
  the GitHub API and waste tokens on noise.
- **Only state transitions emit.** Stable state is silent.
- **Transient failures are tolerated** (`2>/dev/null` on both `gh`
  calls, `|| echo '[]'` on `gh pr checks`) so a single API blip yields
  a degraded poll instead of killing the watch. The `${var:-?}`
  fallbacks in the state string ensure partial results still produce
  an event rather than a blank state.
- **One line per event** keeps notifications terse — Monitor turns every
  stdout line into a chat notification.

## Reacting to a `change:` event

When the monitor emits `change: fail=N pending=P unresolved=M`, do the
following. Keep the monitor running the whole time.

### 1. Failing checks

```bash
gh pr checks <pr>
gh run view --log-failed <run-id>
```

Fix the root cause in code. Never bypass with `--no-verify`, flaky-retry
loops, or skip directives.

### 2. Fetch every review thread with resolution + outdated state

REST doesn't expose `isResolved`/`isOutdated`. Use GraphQL, and
**paginate** via `--paginate` + an `$endCursor` variable so PRs with >100
threads don't silently truncate:

```bash
gh api graphql --paginate -f query='
query($owner:String!, $repo:String!, $number:Int!, $endCursor:String) {
  repository(owner:$owner, name:$repo) {
    pullRequest(number:$number) {
      reviewThreads(first:100, after:$endCursor) {
        nodes {
          id
          isResolved
          isOutdated
          path
          line
          originalLine
          comments(first:50) {
            nodes { databaseId author { login } body createdAt }
          }
        }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
}' -f owner=<owner> -f repo=<repo> -F number=<pr>
```

`gh --paginate` walks `pageInfo.endCursor` until `hasNextPage: false` and
emits one JSON document per page to stdout. When consuming, slurp with
`jq -s` and iterate across pages (e.g. `jq -s '[.[].data.repository.pullRequest.reviewThreads.nodes[]] | ...'`).

### 3. Triage each unresolved thread

Judge each one individually. Do **not** blanket-skip any category.

- **Human comment** — address it.
- **CodeRabbit actionable** — address it.
- **CodeRabbit _nitpick_** — DO NOT auto-dismiss. CodeRabbit is often
  overly polite and hides real issues under "nitpick". Read each one and
  decide: is this a legit correctness/clarity/safety concern, or purely
  stylistic noise that conflicts with repo conventions? Only skip if the
  suggestion is actively wrong, contradicts `CLAUDE.md`, or is irrelevant
  to the change's intent. When in doubt, apply the fix.
- **Thread marked `isOutdated: true`** — DO NOT skip. "Outdated" means
  the line numbers moved since the comment was written, **not** that the
  feedback is obsolete. Re-read the comment against the current code at
  that region and decide whether the concern still applies. Usually it
  does.

### 4. Fix in code

Actually edit source. Never reply-without-fix. If a comment implies a
design decision the user should own, stop your own work and ask — but
**leave the monitor running**. It's silent while state is stable, costs
nothing, and will resume emitting as soon as the discussion ends in a
push or a resolve.

### 5. Reply + resolve each addressed thread

```bash
# Reply (the in_reply_to form of the review comments API)
gh api repos/<owner>/<repo>/pulls/<pr>/comments/<comment-databaseId>/replies \
  -f body="Fixed."

# Resolve
gh api graphql -f query='
mutation($threadId:ID!) {
  resolveReviewThread(input:{threadId:$threadId}) {
    thread { isResolved }
  }
}' -f threadId=<thread-node-id>
```

For threads you intentionally skipped (e.g., a nitpick that's wrong),
reply with one sentence explaining why, then still resolve the thread.

### 6. Commit + push

Bundle the round into one commit following repo style (`CLAUDE.md`:
one-line `type(scope): summary`, ≤72 chars).

This repo's `CLAUDE.md` says: **"Always confirm with human before doing
a git commit or a git push."** Honor that unless the user has explicitly
told you to run autonomously.

**Do not stop the monitor after pushing.** A new push triggers new CI
runs and may draw new CodeRabbit follow-ups; the monitor will fire again
when they land, and the loop continues naturally.

**Greptile will not follow up on its own.** This repo's `greptile.json`
sets `triggerOnUpdates: false`, so Greptile reads the PR when it opens and
then not again until someone asks. CodeRabbit still re-reviews every push;
Greptile does not. That is what step 7 is for.

### 7. Re-trigger Greptile on the final state

Once every thread is addressed and CI is green — i.e. right before you
would exit — ask Greptile to read the merged-ready state:

```bash
gh pr comment <n> --body "@greptileai"
```

(The same thing is available as the **"Re-trigger Greptile"** button in
Greptile's own comment footer.)

Then wait for the new review and triage whatever it returns exactly like
any other round: fix, reply, resolve, push, re-trigger. A PR should be
merged on a Greptile review of the code that is actually merging, not of
the first draft of it.

Skip this only when Greptile never reviewed the PR at all (it is not
installed on the repo, or the PR carries the `no-greptile` label).

## Exit

When the monitor emits `clean: ...`:

1. Run step 7 if you have not already — re-trigger Greptile and let the
   final-state review land. If it opens new threads, you are not clean;
   go back to step 3.
2. `TaskStop` the monitor.
3. Report a summary: what was fixed, what was intentionally skipped and
   why (one bullet per skipped thread), final check + thread counts, and
   whether the final Greptile re-trigger came back clean.

## Guardrails

- **Always use `Monitor`.** Not `sleep` in a Bash call, not manual
  repeated polling. Monitor streams events so the agent reacts
  immediately and runs concurrently with other work.
- **Never skip outdated comments blindly.** `isOutdated` = line moved.
- **Never blanket-dismiss CodeRabbit nitpicks.** Judge each on merit.
- **Never bypass failing checks** with `--no-verify` or similar.
- **Never merge on a stale Greptile read.** Greptile does not re-review on
  push here; re-trigger it at the final state (step 7).
- **Confirm before `git push`** unless running autonomously.
- **Pause and ask** if a comment implies a design decision the user
  should own, rather than guessing — but **keep the monitor running**
  during the discussion. It's silent while state is stable.
- **One-line commit messages**, detail in the PR body.

## Related

- `CLAUDE.md` — repo commit/PR conventions, CodeRabbit reply protocol.
- `greptile.json` — repo review config; `triggerOnUpdates: false` is why
  step 7 exists. `CONTRIBUTING.md` explains it for humans.
- `Monitor` tool — session-length, `persistent: true`, one stdout line =
  one event, stop with `TaskStop`.
