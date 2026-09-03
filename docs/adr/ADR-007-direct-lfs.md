# ADR-007: Direct LFS Without git-lfs CLI

**Status**: Accepted
**Date**: 2026-04-02
**Amended**: 2026-09-02 — checkout-side insulation (see "Addendum" at the end)

## Context

ox uploads session recordings (raw.jsonl, summary.md, session.md) to team ledgers backed by Git LFS. These files range from kilobytes to megabytes — too large to commit as regular git blobs, but well-suited for content-addressed LFS storage.

The standard approach would be to depend on the `git-lfs` CLI binary: configure `.gitattributes` with `filter=lfs`, and let git's clean/smudge filters handle upload and download transparently.

Three problems with that approach:

1. **git-lfs is an optional install.** Many developers don't have it. Requiring it creates a hard dependency that blocks ox from working at all for those users — violating the principle that ox should reduce friction, not add it.

2. **git-lfs global config causes push failures.** When git-lfs is installed globally with `filter.lfs.required=true`, it injects `lfs.repositoryformatversion` into `.git/config`. This triggers HTTP 403 errors on push to GitLab's ALB. ox must actively strip this config before every push (`StripLFSConfig`).

3. **No control over the upload lifecycle.** ox needs to guarantee that content files are never replaced with pointer stubs until after the push succeeds (see ADR-005). With git-lfs's automatic clean filter, pointer replacement happens at `git add` time — before push — making content recovery impossible if the push fails.

## Decision

### Speak the LFS Batch API Directly

ox implements a pure-Go LFS client (`internal/lfs/`) that talks directly to the Git LFS Batch Transfer API (`POST <repo>.git/info/lfs/objects/batch`). No git-lfs binary required.

### OID Computation

Content addressing uses SHA256, matching the LFS spec:

```go
func ComputeOID(content []byte) string {
    h := sha256.Sum256(content)
    return hex.EncodeToString(h[:])
}
```

OIDs are stored with the `sha256:` prefix in `meta.json` (e.g., `sha256:abcdef...`).

### Upload Pipeline

Five steps, orchestrated by `lfs.UploadSessionFiles`:

```
1. Read content files from session directory
2. Compute SHA256 OID + size for each file (skip if already a pointer)
3. POST batch request with operation: "upload" to get action URLs
4. PUT each blob to the action href (up to 4 concurrent uploads)
5. POST to verify href if server provides one
```

Returns a `map[filename]FileRef` for inclusion in `meta.json`.

### Pointer Files Without .gitattributes

ox writes standard LFS pointer files (same format as git-lfs), but does **not** register a clean/smudge filter in `.gitattributes`:

```
version https://git-lfs.github.com/spec/v1
oid sha256:<hex>
size <bytes>
```

Without a filter, git treats pointers as regular ~130-byte text files. Hydration is handled entirely by ox's own download path. The pointers are committed to git to prevent LFS garbage collection on the server.

Detection uses content inspection (`IsPointerFile` reads up to 200 bytes and tries `ParsePointer`), not filenames or `.gitattributes` rules.

### Post-Push Pointer Replacement

The critical ordering guarantee — content must survive push failure:

```
LFS upload blobs
    -> WriteSessionMetaOnly (meta.json with refs, content files intact)
    -> git commit + push
    -> WritePointerFiles (NOW replace content with pointer stubs)
```

If the push fails at step 3, content files are still on disk. The next attempt re-uploads (idempotent — same OID = no-op on server) and retries the push.

The CLI path (`ox session upload`) enforces this ordering. The daemon anti-entropy path (`session_finalize.go`) follows the same sequence.

### Credential Resolution

LFS auth uses the Git PAT (not the OAuth API token) via HTTP Basic auth:

```
Authorization: Basic base64(username:token)
```

Credentials are loaded from the local credential store (`gitserver.LoadCredentialsForEndpoint`). The remote URL may contain an embedded PAT, which is stripped before constructing the LFS batch URL.

## Consequences

**Benefits**:
- ox works for users who don't have git-lfs installed — zero external binary dependency
- Full control over when content files are replaced with pointers (post-push only)
- Content-addressed uploads are inherently idempotent — retries are always safe
- Parallel uploads (4-way semaphore) without git-lfs's sequential clean filter
- No `.gitattributes` filter registration avoids conflicts with users' existing git-lfs config

**Tradeoffs**:
- ox must implement and maintain its own LFS client against the Batch Transfer API spec. The API is stable and well-documented, but ox owns the HTTP plumbing.
- `git lfs ls-files` is still used in one place: `RepairMissingLFSObjects` (push pre-flight) scans for orphaned pointers. This gracefully degrades — if git-lfs is not installed, the repair step is skipped.
- Users cannot `git lfs pull` to hydrate ox's pointer files, in clones without a server-provided `filter=lfs` attribute (no `.gitattributes` filter, since ox never writes one). Hydration goes through `ox` commands. This is intentional — ledger content is accessed through ox, not raw git operations. (Team-context clones are the documented exception: they *do* carry a server-seeded `filter=lfs` — see the 2026-09-02 addendum below for the consequence that has for a machine with git-lfs installed.)
- The `StripLFSConfig` pre-flight on every push is a workaround for a GitLab ALB behavior. If GitLab fixes this, the workaround becomes dead code (harmless but unnecessary).

## Addendum (2026-09-02): the user's git-lfs still runs on ox's clones

This ADR insulated ox from the user's global git-lfs on the **push** side (`StripLFSConfig`)
and assumed the **checkout** side was safe because ox never writes `.gitattributes` with
`filter=lfs`. That assumption does not hold for every clone ox manages: team-context repos are
seeded server-side and *do* ship `.gitattributes` with `filter=lfs` on discussion attachments
(their bytes are served through the API's LFS resolver — a deliberate decision in the owning
service). On any machine where git-lfs is installed globally, its clean/smudge filters
therefore run on every checkout of those clones.

Observed consequence (COE 2026-09-02): a nested LFS pointer committed upstream is unwrapped one
layer by git-lfs smudge on every checkout, so the worktree can never equal the index. The
daemon's `pull --rebase --autostash` then fails on every cycle, leaks an autostash each time,
and the team context silently stops syncing. `ox doctor` has a repair for this shape
(`restoreRawLFSPointers`) but the check gating it did not run against the affected clone.

**Amended decision — planned, not yet implemented (`ox-baz5.4`).** The insulation will be
complete only when it covers both directions:

- Every git command ox runs — daemon and CLI — must pass
  `-c filter.lfs.smudge=cat -c filter.lfs.clean=cat -c filter.lfs.process= -c filter.lfs.required=false`.
  That's a wider surface than it first looks: `internal/gitutil.RunGit` — the most widely
  used git-invocation helper in the codebase — currently sets only `commit.gpgsign`/
  `tag.gpgsign` and none of the four `filter.lfs.*` overrides, so every one of its many
  callers is exposed today. `restoreRawLFSPointers` (`cmd/ox/doctor_team.go`) already
  overrides three of the four (`smudge`, `process`, `required`) for its read-only `checkout`
  repair; the fourth, `clean=cat`, will also be needed there — `git pull --autostash` can
  invoke the clean filter when it stashes dirty LFS-filtered content, which a plain checkout
  never does. Several other call sites build `exec.Command("git", …)` directly, bypassing
  `RunGit` entirely, and will need the same treatment.
- The consequence "hydration goes through ox's own download path, never through smudge" is
  only true once every path above carries the overrides — not yet the case.
- `StripLFSConfig` remains as the push-side half (already implemented, unaffected by this).

Until all of the above lands, `ox-baz5.4` stays open and the COE's Actions table correctly
marks it "not started" — the analysis above is the decision, not a status report.

Tracked as bd `ox-baz5.4` (insulation) and `ox-baz5.5` (doctor check scoping). Related:
ADR-030 (per-clone serialization), `.claude/rules/lfs-no-git-lfs-binary.md`.
