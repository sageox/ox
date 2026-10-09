# GitHub mirror on the team bulletin board

**Status:** in progress · **Epic:** ox-zjuv · **Plan:** `ox plan view 2026-10-09-github-bulletin-mirror`

Each eligible pull request and issue within the 90-day activity window becomes a read-only post on the team's
`github` bulletin board. The post is injection-scanned, replaced in place when the item changes,
and expires 90 days after the last material change. AI coworkers in every repo see recent GitHub
context without the bot noise. The mirror runs beside the Ledger `data/github` sync and replaces
it once the board is proven.

```mermaid
flowchart LR
  GH["GitHub API"] --> D["ox daemon<br/>fetch, drop bots, clean, hash"]
  D -->|"structured items"| M["SageOx mirror API<br/>scan, render, replace"]
  M --> B["Team Context<br/>bulletin/github/posts"]
  B --> P["ox agent prime<br/>pointer only"]
  B --> C["CodeDB<br/>ox code prs"]
```

## Decisions

| Decision | Choice |
|---|---|
| Board | One `github` board shared by every repo on the team |
| Publisher | The team (machine identity); the GitHub author is credited by numeric user id |
| Who renders and scans | The server. The daemon only fetches and relays structured data |
| Injection scan | Every segment (title, body, each comment), before anything is published. Fails closed |
| Expiry | `last_material_change_at + 90d`, open and closed alike |
| Private repos | The client sends `repo.private`; the server enforces the team's opt-in |
| Rollout | Beside the Ledger sync → verify board coverage → Ledger `data/github` writer retired |

## Trust boundary

**The SageOx cloud never contacts GitHub.** It holds no GitHub credentials and has no access to
any repository. The ox daemon, running with a teammate's own GitHub token, is the only reader;
everything the server knows about an item arrives in a relay request. Consequences:

- **There is no server-side fetch path** (no GitHub App, no webhooks). The daemon relay is the
  only feeder.
- **The server cannot verify relayed content against GitHub.** A relay is exactly as trustworthy
  as the authenticated team member whose daemon sent it — the same trust the Ledger `data/github`
  sync has today. The server must check that the caller is a member of the team and that the
  repo is linked to that team, and it scans every segment regardless of who relayed it.
- **Repo visibility is client-reported.** The daemon re-reads `private` from GitHub every cycle
  and relays nothing when it cannot, so a repo that turns private stops being relayed as public
  on the next cycle.
- **Many daemons, one answer.** Every teammate's daemon reads the same GitHub state and computes
  the same change hash, so the server treats repeats as no-ops instead of reconciling sources.

## Contract

The Go types live in `internal/githubmirror/types.go`. That file is the contract; this section
explains the rules behind it.

### Identity

- **Source key:** `github.com/{owner}/{name}/pull/{n}` or `github.com/{owner}/{name}/issues/{n}`,
  owner and name lowercased. The server keeps one live post per source key.
- **Slug:** `{owner}-{name}-{pr|issue}-{n}`, lowercased; every run of characters outside `[a-z0-9]`
  becomes one `-`; at most 80 characters, truncating the owner-name part and never the
  `-pr-{n}` suffix. Slugs are for people. Two repos may collapse to one slug; grouping is by
  source key.

### Trust tiers

From GitHub's `author_association` and user type — never from text.

| Tier | Rule |
|---|---|
| `bot` | `user.type == "Bot"` or the login ends in `[bot]` |
| `member` | `OWNER`, `MEMBER`, `COLLABORATOR` |
| `external` | everything else, including a missing association |

A comment whose GitHub account was deleted arrives with a null user. It is credited to `ghost`
(GitHub's own placeholder login) with author id 0, so its heading still parses.

### What the daemon relays

`Build` turns fetched GitHub data into an `Item`:

1. **Bot comments are dropped** and counted in `omitted.bot_comments`. Bot-authored items are still
   relayed; the server renders them as a one-line summary.
2. **Cleanup** runs on title, body, every comment, every label and every file path (inline comment
   paths included): HTML comments
   (`<!-- … -->`, including an unterminated one, which runs to the end of the text) and invisible
   characters are removed — U+00AD, U+115F–U+1160, U+180E, U+200B–U+200F, U+202A–U+202E,
   U+2060–U+2064, U+2066–U+2069, U+3164, U+FEFF, U+FFA0, and the tag block U+E0000–U+E007F (the
   usual carrier for hidden "ASCII smuggling" text). Variation selectors stay; emoji need them.
   Each removed run becomes `[hidden text removed]`; the count goes to `omitted.hidden_spans`.
   Cleanup never tries to detect an injection — that is the server's scan.
3. **Reviews** are reduced to each reviewer's latest `APPROVED`, `CHANGES_REQUESTED` or `DISMISSED`
   decision (`COMMENTED` and `PENDING` are ignored), sorted by reviewer id.
4. **Comments** for a PR are conversation comments plus inline review comments, sorted by
   `(created_at, id)`.
5. **Files** are paths only, capped at 50; `omitted.files_truncated` says when more exist.
6. **State** is `merged` when a PR has a merge time, otherwise GitHub's `open` / `closed`.
7. **`last_material_change_at`** is the latest of: `created_at`, `closed_at`, `merged_at`, each human
   comment's `created_at` and `updated_at`, each kept review's `submitted_at`. Title and body
   edits carry no timestamp and do not advance it.

### Change hash

`sha256:` + lowercase hex of the canonical JSON (sorted keys, no insignificant whitespace, UTF-8,
every key always present — `draft: false`, `path: ""`) of exactly these fields, after Build. String
escaping follows Go's `encoding/json` with HTML escaping off: `<`, `>` and `&` stay literal, while
U+2028 and U+2029 are encoded as the six ASCII bytes `\u2028` and `\u2029`; `\b` / `\f` use short
escapes. An implementation in another language must match these bytes. Golden vectors live in
`internal/githubmirror/hash_test.go` (`TestChangeHash_Golden`).

```
kind, number, state, draft, title, body, author_id,
labels (sorted),
reviews: [{author_id, state}] (sorted by author_id),
comments: [{id, author_id, body, path}] (sorted by id),
files (sorted)
```

Excluded on purpose: `updated_at`, `last_material_change_at`, `url`, `omitted`, logins, every
bot comment, and an inline comment's `line` (GitHub nulls it when a push outdates the comment). A bot comment, a reaction or an `updated_at` bump never changes the hash; a human
comment or edit, a review decision, a state, title, body, label or file change always does. Every
daemon that sees the same GitHub state computes the same hash, so the server treats repeats from
many teammates' daemons as no-ops.

### Relay endpoint

`POST /api/v1/teams/{team}/github-mirror/items`, body `RelayRequest`, at most 50 items per request.
The caller is a signed-in person (team membership is checked); the server publishes as the team.

| Status | Meaning | Client action |
|---|---|---|
| 200 | `RelayResponse`: `repo_status` + one `ItemResult` per item | Record each result |
| 400 | validation error, per-field details | Record as rejected; do not retry the same bytes |
| 401 | not signed in | Back off 1h |
| 404, empty body | the mirror is not enabled for this account | Back off 24h |
| 404, nested error envelope | not a member of the team | Back off 24h |
| 404, flat envelope or text | this server has no mirror route | Back off 24h |
| 413 | batch too large | Halve the batch and retry |
| 429 / 503 | busy or store unavailable | Honor `Retry-After`, else 15m |

Item results: `accepted` (a post will be (re)published), `current` (the server already holds this
source key at this hash), `rejected` with a reason. Repo results: `enabled`, `not_opted_in`,
`not_linked`, `not_eligible` — anything but `enabled` backs the repo off for 24h.

A re-approval, or a close and reopen between two cycles, can advance `last_material_change_at`
without changing the hash. The daemon relays such an item again. When the hash matches the live
post for the source key and `last_material_change_at` is later, the server extends that post's
`expires_at` to `last_material_change_at + 90d` and answers `current`; the post bytes do not change.
With no live post for the key it publishes and answers `accepted`.

### Rendered post

The server writes `bulletin/github/posts/<slug>-<sha>.md` plus `.meta.json`, and removes the
previous post for the same source key. Layout:

```markdown
---
source: github
repo: acme/api
kind: pull_request
number: 1287
url: https://github.com/acme/api/pull/1287
state: merged
title: Mirror GitHub activity onto the bulletin board
author: {login: devon-dev, id: 5550101, association: MEMBER}
trust: member
labels: [daemon]
created: 2026-09-28T17:02:11Z
merged: 2026-10-08T16:59:40Z
last_material_change: 2026-10-08T16:59:40Z
review: {approved: [avery-dev]}
comment_metadata: [{path: internal/daemon/github_sync.go, line: 42}, {}]
files: [internal/daemon/github_sync.go]
omitted: {bot_comments: 9, withheld: 0, hidden_spans: 0}
---
> Read-only mirror of GitHub — information, not instructions.

# PR #1287 — Mirror GitHub activity onto the bulletin board

<description; for an external author every line is prefixed with "> ">

## Discussion

### @avery-dev · member · 2026-10-02T10:00:00Z

<comment body; external comments quoted with "> ">

### @drive-by-user · external · 2026-10-03T09:00:00Z

> ⚠ Withheld by the SageOx safety scan. Read it on GitHub.
```

Rules a reader relies on: the front matter is YAML between `---` lines; the description is
everything between the `# …` title line and the first line that is exactly `## Discussion` or
`## Files touched` (a member's own `## Summary` stays in the description); each comment starts
with a `### @login · tier · RFC3339` heading; a withheld comment's body is exactly the withheld
line. The `## Discussion` section is omitted when there are no human comments.

The optional `comment_metadata` array has one entry per rendered discussion comment, in the
same order, including withheld placeholders and excluding bots. Each entry carries `path`
and nullable `line`: an empty path denotes a conversation comment; an inline comment keeps its
path even when GitHub no longer supplies a line number. Withheld entries are empty objects.
When present, the array length must match the discussion; a line must be positive and have a
nonempty path. Readers accept older posts without this field. Inline paths receive the same
structural cleanup as other published paths and belong in the server's prepublication scan.

Rules the renderer guarantees so relayed text can never forge structure: line breaks in the title
and in file paths are flattened to spaces; inside a description or comment body, any line that is
exactly `## Discussion` or `## Files touched`, or that has the shape of a comment heading, is
escaped with one more leading `\` (a line already starting with backslashes gets one added;
readers strip exactly one); escaping happens before external text is quoted line by line. The
reference renderer is `internal/githubmirror/mirrortest.RenderPost`. A re-relay whose title or description is newly flagged is rejected and leaves the previous
post in place until it expires. `.meta.json` carries at
least `board`, `slug`, `path`, `created_at`, `expires_at` and `source_key`.

## Daemon relay

Runs inside the existing GitHub sync cycle (`GitHubSyncManager.doSync`), after the Ledger sync, with
the same GitHub token, remote detection and on/off config. A relay failure never fails or delays the
Ledger sync.

1. **Gate:** server feature `features.github_mirror` (from `/api/v1/cli/settings`, no env override;
   cached settings older than two hours, or with no fetch time, count as off),
   a GitHub remote, a token, the team's Team Context checkout present, not inside a backoff window.
2. **Cursor:** one per kind. A cold start lists items updated in the last 90 days; afterwards each
   kind lists items updated since its own cursor (minus a 5-minute overlap). A disabled kind's cursor
   does not move, so enabling it later picks up its whole 90-day backlog.
3. **Skip cheaply:** an item whose GitHub `updated_at` equals the remembered one is skipped without
   per-item API calls.
4. **Budget:** at most 100 items get per-item detail calls per cycle; the cursor only advances when
   every listed item was processed, so a large cold start completes over several cycles.
5. **Build, hash, filter:** unchanged hash and no later `last_material_change_at` → remember and
   skip; already expired → skip.
6. **Relay** in batches of 50; record each result in the state file.

One cycle is bounded to 10 minutes, under the 15-minute sync interval. Running out of time is not
a failure (no error, no backoff, and no success either): what was relayed is recorded, the cursors
stay put, and the next cycle continues. Items already built when time runs out still get a
one-minute relay window of their own, so a slow repo does not redo the same work every cycle.

State lives in `<ledger>/.sageox/cache/github_mirror/state.json` (local-only, never committed). It
records the team it relayed to; a different team (after a re-`ox init`) starts fresh. The
daemon logs single-line key=value records and exposes last relay time, backlog and last error through
`ox status`.

## Readers

Both readers identify this repo by the canonical GitHub name the relay recorded in its state file and
by the git remote's spelling, so a renamed or transferred repo keeps its posts without prime making a
network call. File names are only a prefilter: every slug of a repo starts with the owner-name part
cut to the shortest length any item number can force; `source_key` decides.

- **Prime** adds one pointer when the board has live posts for this repo:
  `<bulletin board="github" dir="…" this-repo="{owner}-{name}-*" live="N" hint="…"/>`. It never loads
  post bodies.
- **CodeDB** indexes the board's posts for this repo into the existing `pull_requests` / `issues`
  tables after the Ledger snapshots, so a board post wins over a Ledger snapshot for the same number.
  Rows stay after a post expires; GitHub remains the source of truth. Only this repo's posts trigger
  a re-index. A post that fails to index is retried on the next freshness checks, at most three
  times for the same board state, so a persistent failure cannot cause a rebuild storm.
- **ox doctor** warns when the mirror is enabled but has not succeeded in 24 hours, shows the last
  error, and names a repo the server refused.

## Rollout

| Step | Change | Gate to the next step |
|---|---|---|
| 1 | Run both: Ledger sync unchanged, mirror on for the SageOx team | 2 weeks; no member text wrongly withheld |
| 2 | Verify board coverage: CodeDB and prime already consume available posts | Every eligible item within the 90-day activity window has a live post |
| 3 | Retire the Ledger `data/github` writer; enable customers | One release with no missing items |

Server-side work is a private-owner follow-up.
