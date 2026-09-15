# Bounded ledger read sync

Status: client implementation contract for [ox #891](https://github.com/sageox/ox/issues/891), shared with [agent-toolkit #57](https://github.com/sageox/agent-toolkit/issues/57). The backend `ledger.read_url` capability is additive and must be enabled before live acceptance. This document does not announce a release or a version consumers can already pin.

## Command and selection

```sh
# Supply SAGEOX_TOKEN through the runtime's secret environment.
export SAGEOX_ENDPOINT=https://sageox.ai
export XDG_DATA_HOME=/var/lib/my-reader/data

ox sync --read-only --repo repo_019ff2f5-2079-7be1-b05e-8caad2772e61 \
  --timeout 5m --json

# Offline verification/recovery of the same local checkout.
ox sync --read-only --repo repo_019ff2f5-2079-7be1-b05e-8caad2772e61 \
  --check --timeout 5s --json
```

- **Repository:** `--repo` is required and accepts the canonical `repo_<uuid>` identity. No repository selection comes from the working directory, source remote, or project configuration.
- **Endpoint:** `SAGEOX_ENDPOINT`, normalized by the existing endpoint helper, defaults to `https://sageox.ai`. It must resolve to an HTTPS origin without userinfo, query, fragment, or a path prefix. Neither a project endpoint nor the only human login can select another identity.
- **Credential:** each remote operation resolves the currently selected `SAGEOX_TOKEN` team access token (TAT). An absent, malformed, expired, revoked, or unauthorized token never falls back to a disk login or provider credential. Rotation takes effect on the next operation; a running process retains its environment, so replace/restart that process when rotating an environment-mounted token.
- **Data home:** set an absolute `XDG_DATA_HOME` for each isolated consumer identity. The checkout is selected exclusively through `config.DefaultLedgerPath(repoID, endpoint)`, under the existing SageOx data layout. The returned `path` is authoritative; consumers must not reconstruct it. Relative data homes and `OX_XDG_DISABLE` are rejected because they defeat this isolation contract. Omitting `XDG_DATA_HOME` uses the normal user data home.
- **Budget:** `--timeout` is a positive Go duration, default `5m`. One context bounds discovery, waiting for the checkout lock, Git subprocesses, materialization, and hydration. SIGINT and SIGTERM cancel the operation. Runtime cancellation returns `interrupted` when the process can render a result; SIGKILL cannot produce JSON.
- **Flags:** `--read-only` is required for `--repo`, `--timeout`, and `--check`. Positional arguments and combinations with `--team`, `--all-teams`, `--remove-team`, `--config`, or `--profile` are rejected. Ordinary `ox sync` keeps its existing daemon behavior.

The headless path skips dotenv loading, project CLI initialization, telemetry, feature-setting IPC, heartbeats, daemon startup, and friction retries. It performs discovery and download operations plus local checkout maintenance. It does not push, upload LFS objects, ingest sessions, drain outboxes, or send application-write notifications. Source-code credentials remain separate from ledger credentials.

## Discovery and transport

Discovery uses `GET /api/v1/cli/repos/{repo_id}` with the selected TAT. An authorized ready ledger may include:

```json
{
  "ledger": {
    "status": "ready",
    "read_url": "https://sageox.ai/api/v1/cli/repos/repo_019ff2f5-2079-7be1-b05e-8caad2772e61/ledger.git"
  }
}
```

The backend contract uses standard Git smart HTTP, including protocol v2 and object fetches. Route-local Basic authentication uses username `ox` and the current TAT as password. Git/provider credentials remain on the server. The client accepts only the discovered exact HTTPS origin and canonical repository path, with the documented Git and LFS protocol children. Discovery cannot provision a missing ledger. A missing capability is `unavailable`; an explicitly missing/not-ready ledger is `missing_ledger`. A 404 cannot safely establish whether a repository exists and is reported as unavailable.

Git credentials never appear in remote URLs, command arguments, persisted Git configuration, or an additional credential file. Inherited Git helpers are cleared for the bounded operation. Redirects cannot move authorization to another origin or repository. LFS uses the same scoped endpoint and credential for download authorization, verifies content hashes, and keeps credentials off unrelated object-storage hosts. Neither this client mode nor the proposed read route authorizes push or LFS upload.

Both `read-only` and `full-access` (read/write) TATs can authorize this read capability for an accessible repository. The TAT itself authenticates ledger requests; no separate client ledger token is issued. Effective permissions are the intersection of the current TAT scope, repository authorization, and supported operation. This read-only command and route deny writes for both scopes. A future explicit write capability must require `full-access` and current repository authorization; a broader server-side provider credential must never elevate the caller.

The server must check the current TAT scope and repository authorization on every new request. Downloads and requests already in flight cannot be recalled by revocation. A consumer must independently establish current repository authorization before serving cached content; a ready or recently refreshed file is not proof of permission.

## JSON result and exit status

`--json` writes one JSON object followed by a newline to stdout. Error bodies, token values, credential-bearing URLs, and raw subprocess output are excluded. Consumers should ignore additional fields introduced within `schema_version: 1`.

```json
{
  "schema_version": 1,
  "repo_id": "repo_019ff2f5-2079-7be1-b05e-8caad2772e61",
  "endpoint": "https://sageox.ai",
  "path": "/var/lib/my-reader/data/sageox/sageox.ai/ledgers/repo_019ff2f5-2079-7be1-b05e-8caad2772e61",
  "head": "0123456789012345678901234567890123456789",
  "ready": true,
  "last_successful_sync": "2026-09-08T17:00:00Z",
  "history": "full",
  "coverage": {
    "complete": true,
    "paths": ["sessions/", "data/plans/"],
    "files": 12,
    "empty": false
  },
  "hydration": {"state": "complete", "required": 2, "completed": 2}
}
```

The sample paths and counts are illustrative; the returned coverage describes the actual selected materialization policy.

| Field | Meaning |
| --- | --- |
| `schema_version` | Result/receipt format version, currently `1`. |
| `repo_id`, `endpoint`, `path` | Selected identity and canonical local checkout. Unvalidated input is not echoed on argument errors. |
| `head` | Verified commit identity; empty when no commit has been verified. |
| `ready` | Local materialization is safe to read while holding the checkout lock and under the stated coverage. This is independent of current authorization and freshness. |
| `last_successful_sync` | UTC time of authorized remote Git ref observation for the subsequently verified revision, or `null` if unknown. |
| `history` | `full`, `shallow`, or `unknown`. A shallow checkout cannot report full coverage/readiness. |
| `coverage.complete` | Every required path for the receipt’s pinned sparse/activity window has been verified. |
| `coverage.paths` | Required materialized paths/patterns for the receipt. Sessions and plans are always retained. |
| `coverage.files`, `coverage.empty` | Verified file count and whether the selected committed tree is empty. Empty is distinct from failed discovery or missing hydration. An unborn HEAD does not qualify and remains unavailable. |
| `hydration.state` | `complete`, `missing`, or `unknown`. |
| `hydration.required`, `hydration.completed` | Required current-worktree LFS objects and successfully verified objects. |
| `error_class` | Sanitized failure category, omitted when none. |
| `error_detail` | What failed, omitted when the failure carries no recognized reason. See [Failure detail](#failure-detail). |

| Exit | Contract |
| --- | --- |
| `0` | Operation completed with `ready: true` and no `error_class`. |
| `1` | Runtime failure. Inspect `error_class`; any retained local readiness is not a successful remote refresh. |
| `2` | Invalid invocation or unsafe endpoint/data-home selection (`invalid_arguments`). |

Failure categories are `invalid_arguments`, `denied`, `unavailable`, `missing_ledger`, `interrupted`, `dirty`, `missing_hydration`, `incomplete_history`, `incomplete_coverage`, `identity_mismatch`, and `git_failed`. Authentication failures, including a missing selected TAT, use `denied`. A deadline or canceled lock wait uses `interrupted`. `unavailable` also covers discovery transport failure and an unusable backend response. Consumers must tolerate additional failure categories in future releases.

Discovery failure does not inspect an existing checkout and returns `ready: false`. Consumers that need offline recovery may run `--check` separately, subject to their authorization policy.

### Failure detail

Many distinct conditions share one `error_class` — a refused object, a malformed grant, and a corrupt download are all `missing_hydration`. A failure that is about one object therefore also carries `error_detail`, so an operator can act on it instead of correlating server request logs against object storage by hand. This is additive within `schema_version: 1`: `error_class` keeps its published vocabulary and consumers that match only on the class are unaffected.

```json
{
  "error_class": "missing_hydration",
  "error_detail": {
    "reason": "object_refused",
    "path": "sessions/2026-09-08-planning/session.md",
    "oid": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
    "server_code": 404
  }
}
```

| Field | Meaning |
| --- | --- |
| `reason` | Which condition failed. Always present; every other field applies only to some reasons. |
| `path` | Repo-relative path of the file the object materializes. |
| `oid` | Bare SHA-256 object identifier. |
| `expected_oid` | Object identity the pointer should have named. |

An `oid` or `expected_oid` is omitted when the value supplied for it is not a canonical bare SHA-256 identifier. A batch response may name an object that was never requested, and a committed pointer's `oid` line is unvalidated text; neither is republished.
| `expected_size`, `actual_size` | Pointer-declared and observed sizes, in bytes. |
| `server_code` | Status the server reported for this object. |

| `reason` | `error_class` | Condition |
| --- | --- | --- |
| `malformed_pointer` | `missing_hydration` | An LFS pointer in the checkout cannot be parsed. |
| `nested_stub` | `missing_hydration` | A materialized file's content is itself a pointer, and not the one HEAD commits. |
| `empty_object_oid_mismatch` | `missing_hydration` | A size-0 pointer names an object other than the empty one. |
| `shared_object_size_conflict` | `missing_hydration` | Two files name one object with different sizes. |
| `batch_response_incomplete` | `missing_hydration` | The batch response holds fewer objects than the batch. |
| `batch_object_unrequested` | `missing_hydration` | The batch response holds an object that was not requested. |
| `batch_object_duplicated` | `missing_hydration` | The batch response repeats an object. |
| `object_refused` | `missing_hydration`, `denied` | The server reported a per-object error. |
| `object_size_mismatch` | `missing_hydration` | The granted size differs from the pointer's. |
| `object_missing_actions` | `missing_hydration` | The grant carries no actions. |
| `object_missing_download_action` | `missing_hydration` | The grant carries actions but no download. |
| `download_refused` | `missing_hydration`, `denied` | The object download returned an HTTP failure. |
| `download_failed` | `missing_hydration` | The object download failed, or its content did not verify. |
| `download_stat_failed` | `missing_hydration` | The downloaded object could not be inspected locally. |
| `downloaded_size_mismatch` | `missing_hydration` | The downloaded bytes do not match the pointer's size. |
| `object_not_materialized` | `missing_hydration` | Verification found a covered file still left as a stub. |

Detail obeys the same redaction rules as the rest of the result: no credential, credential-bearing URL, response body, or subprocess output. Server-supplied and pointer-supplied identifiers are validated before they are carried, never sanitized in place. There is no server message field. The client replaces a read route's per-object error prose with the status text for that error's code before any caller sees it, so a message field could only restate `server_code` and would misrepresent a client-generated string as the server's own.

Consumers must tolerate three things: additional reasons; a detail carrying `reason` alone, because a batch-level failure such as `batch_response_incomplete` identifies no single object; and an absent `error_detail`, because a failure raised without a reason — a denied batch request, a Git failure, a canceled operation — carries none.

## Remote evidence and local recovery

`last_successful_sync` records a successful authorized Git ref observation, not command completion or a metadata authorization check. The timestamp is captured when refs are observed and published only after that selected revision has been materialized, hydrated, and verified. Hydration time does not move the observation forward. A successful fetch with unchanged refs can advance the timestamp.

Readiness is durably invalidated before checkout or hydration mutation. A verified receipt is published atomically after completion. Failed or interrupted clone staging is never exposed as a ready checkout. Dirty files, local commits, sessions, and plans are preserved; an unsafe refresh fails instead of discarding or recloning over them.

Previously hydrated objects whose committed pointers change or disappear are retained under `<path>/.sageox/cache/read-sync/objects/<OID>` before worktree replacement. The OID is the bare SHA-256 object identifier. Unchanged hydrated files remain materialized through refresh.

LFS hydration requests at most 100 unique objects per batch, within the backend's 64 KiB request limit. Batch responses are limited to 1 MiB before JSON decoding, including responses without a declared length. Each batch's object identities, sizes, and actions are validated before its files are materialized. A later failed or canceled batch preserves earlier verified files while readiness stays false; a retry requests only the remaining objects.

`--check` acquires the materialization lock, verifies local state without contacting the server or triggering lazy fetches, and can republish recovered local readiness. It requires an existing identity-matched receipt; an arbitrary checkout or missing/corrupt receipt remains unavailable until an authorized refresh. It never advances freshness. A retained observation timestamp is usable only when its recorded HEAD still matches the verified HEAD. Recovery at a different or unconfirmed HEAD yields unknown remote freshness. Missing local objects/hydration remain unavailable offline.

Full Git commit history is retained by default; sparse checkout and the existing activity window bound worktree materialization. The canonical coverage includes `.sageox`, `.sync`, `sessions`, `audit`, `data/plans`, 30 days of GitHub activity, and 12 hours of murmurs. Full history does not promise that every historical large object is eagerly hydrated. Each refresh pins its coverage window once and persists it in `coverage.paths`. Offline checks verify that saved window; crossing an hour or day boundary does not expire local readiness or advance freshness.

An owned shallow cache whose origin already equals the discovered read URL can upgrade after discovery proves the selected repository identity. It must establish full history before reporting readiness and preserve local data on failure. Arbitrary direct GitLab/provider caches are not automatically rebound to a new remote: those callers retain their explicit legacy path until a separate authorized migration is available.

## Guarded reader commands

A successful command or `--check` is a point-in-time result. It cannot guarantee that a later unlocked file read will not race a refresh. Consumers that only need the ledger's sessions or recent activity should therefore call a **guarded reader command** rather than opening files themselves:

```sh
# Sessions, newest first. Same JSON schema as the project-scoped form.
ox session list --repo repo_019ff2f5-2079-7be1-b05e-8caad2772e61 --limit 10 --json

# Murmur and session activity in an explicit window.
ox glance --repo repo_019ff2f5-2079-7be1-b05e-8caad2772e61 \
  --since 2026-09-08T16:00:00Z --until 2026-09-08T17:00:00Z
```

- **Selection:** identical to `ox sync --read-only` — `SAGEOX_ENDPOINT`, the caller's isolated `XDG_DATA_HOME`, and the canonical `repo_<uuid>`. No working directory, project config, source remote, or disk login participates. `ox session list --repo` also still accepts a filesystem path for its existing project-scoped behavior; the two forms cannot collide, because only a canonical `repo_<uuid>` selects a hosted read. `ox glance --repo` has no path form and rejects one.
- **Guard:** each command takes the materialization lock, verifies the receipt against the current HEAD and coverage, reads every file it needs, and only then releases. A refresh cannot replace the worktree mid-read. `--check` followed by an ordinary unguarded read is **not** equivalent and must not be used as a substitute.
- **No credential required.** These commands read local state only. They never contact the server, so they neither establish nor renew authorization, and they cannot advance `last_successful_sync`. Establish current repository authorization separately before serving what they return.
- **Budget:** the lock wait is bounded by the repository lock timeout and by `SIGINT`/`SIGTERM`, so a hosted runtime's tool deadline terminates the read with a sanitized failure rather than truncated output. Run the refresh on its own budget.
- **Time window:** `ox glance --repo` reports the activity window it was given. It does not read or advance the local checkpoint that a bare `ox glance` resumes from, so a caller that omits `--since` gets a default 4-hour window every time. Supply explicit bounds. The murmur partitions are keyed by UTC hour; pass RFC 3339 UTC timestamps rather than relative durations.
- **Failure:** stdout stays empty and the sanitized class is written to stderr as `Ledger read failed: <class>`, with exit `1` for a refused read and `2` for an unsafe or malformed selection. A refused read can never be mistaken for a ledger that is genuinely empty. The class vocabulary is the one listed above.
- **Before the first refresh** the class is `interrupted`, the same class `--check` reports. Readiness is invalidated *before* any mutation, so a checkout with no receipt is indistinguishable from one whose refresh was interrupted before it could publish a new one. Treat it as "run a refresh", not as "retry the same read".
- **Locally modified content is `dirty`, never empty.** Deleting or editing materialized ledger files refuses the read rather than reporting a ledger with fewer sessions in it.

## Reading the checkout directly

Go readers inside ox use `ledger.WithReadCheckout(ctx, path, repoID, endpoint, callback)` from `internal/ledger`. The function holds the same exclusive advisory lock as materialization, verifies the receipt against HEAD and coverage, republishes local readiness, and invokes the callback only for a ready checkout. Keep the lock for the entire read, including opening files and consuming their contents. Readers are serialized with other readers and writers. The callback must not invoke another locking ledger operation: the lock is not reentrant.

The on-disk receipt is `<path>/.sageox/cache/read-sync/receipt.json`. It contains the result fields plus the credential-free discovered `read_url`, and is atomically replaced with file and containing-directory synchronization. Consumers must reject a missing, malformed, unsupported-version, identity-mismatched, or `ready: false` receipt. The cross-process locking contract applies to the supported Unix platforms; the existing Windows lock implementation only serializes within one process.

External consumers that need content the guarded reader commands do not expose must implement the same lock-and-receipt protocol before reading the live checkout:

1. Use the exact absolute `path` returned by ox; all participants must share the same filesystem and OS temporary directory namespace.
2. Compute the lock target as the cleaned absolute path `<path>/.git/ox-sync`. Hash its UTF-8 bytes with SHA-256 and take the first 16 lowercase hexadecimal characters. The physical lock is `<OS tempdir>/sageox-locks/<hash>.lock`, as implemented by `fileutil.LockPath(gitutil.RepoLockTarget(path))`.
3. Open that sidecar without replacing or unlinking it and acquire an exclusive `flock`, honoring the reader's own cancellation budget. This is the same lock used by `gitutil.WithRepoLock`. The kernel releases it when the holder dies.
4. Under the held lock, load the receipt and verify `schema_version`, `repo_id`, `endpoint`, `path`, `ready`, current HEAD, full history, required coverage, and complete hydration. A receipt does not authorize data access. Do not run `--check` while holding the lock: that command also acquires it.
5. Read all required contents before releasing the lock. If verification fails, release it and request refresh/local recovery rather than exposing an unverified checkout.

Do not use a successful `--check` as an unlock-and-read protocol. Do not run an unrelated daemon or unmanaged Git writer against an isolated hosted checkout: it may not participate in receipt invalidation, even when its Git operations use the existing mutation lock. This protocol uses the repository's existing local-machine locks; sharing a data home across hosts requires a separately supported filesystem/locking arrangement.

Give background refresh and individual tool calls separate budgets. A short tool-call wait should stop waiting for readiness; it should not repeatedly cancel a valid cold clone owned by the background refresher. Cancellation by the owner stops that refresh and leaves readiness invalid until verification succeeds. Choose freshness policy independently of read readiness and current authorization. Cold-clone, warm-refresh, subprocess cancellation, and reader-wait budgets need measurement with the actual downstream consumer and enabled backend before a release is pinned.

## Acceptance and rollout

Hermetic tests can exercise Git smart HTTP, current-TAT authorization, real subprocesses, locking, sparse history, hydration failures, and recovery against controlled fixtures. Live acceptance additionally requires an enabled backend supporting clone/fetch, protocol v2/lazy object fetches, LFS download, per-request revocation, and repository scoping. Backend acceptance must exercise both TAT scopes for reads, deny pushes/uploads on the read route for both, and recheck scope downgrades on the next request. Exact Git upload-pack and LFS download-batch POSTs are reads for scope enforcement; arbitrary POSTs retain mutation checks. The consumer must prove separate data-home isolation and safe concurrent reads in its own runtime.

Keep existing human/direct-Git behavior and explicit legacy consumer paths available until callers can migrate. There is no automatic credential fallback. Ryan's review is required for the new data-access contract and discovery source-of-truth use under this repository's engineering policy. A released ox version and matching CLI/reference docs must be published before toolkit pins this capability.
