# Rule: ox Never Depends on the `git-lfs` Binary

**Applies to:** all code in `~/Code/sageox/ox`

---

## The rule

ox does not shell out to `git-lfs`. Ever. Not for detection, not for upload,
not for download, not for repair, not in tests, not in doctor checks.

ox uses the LFS *concept* — content-addressed pointer files referencing
blobs stored out-of-band — but implements every piece of the transport in
pure Go under `internal/lfs/`. The git-lfs binary is not a dependency of
the ox CLI, and users installing ox are not expected to have it.

**ox also never writes `.gitattributes` entries that would trigger
git-lfs-managed smudge/clean filters on checkout.** Dehydrated clones are
the default — hydration is explicit and goes through `internal/lfs` calls
to the Batch API, not through `git checkout` + smudge.

## Why

1. **Users almost never have `git-lfs` installed.** Requiring it would break
   the ox CLI for the majority of coworkers on their first invocation.
2. **`.gitattributes` + smudge filters would auto-hydrate content on every
   `git checkout`**, which we explicitly do not want. ox clones are
   intentionally dehydrated by default; hydration is opt-in via our own
   code path.
3. **GitLab's Git LFS Batch API is well-specified and trivial to call over
   HTTP.** We do not gain anything by going through a subprocess — only
   latency, a binary dependency, and a layer of error-path guessing.
4. **Every time we've been tempted to shell out, it caused bugs.** Past
   examples include a `RepairMissingLFSObjects` code path that was gated on
   `.gitattributes` existing (which ox never writes), making the repair
   silently no-op on every real user machine. Deleted in
   `ryan/lfs-remove-git-lfs-binary` (2026-04-11) — see that PR for the
   full post-mortem.

## Canonical locations

All LFS functionality lives in `internal/lfs/`:

| File             | What it does                                                      |
|------------------|-------------------------------------------------------------------|
| `pointer.go`     | `FormatPointer`, `ParsePointer`, `IsPointerFile`, `WritePointerFile` — spec-compliant pointer I/O in pure Go |
| `client.go`      | Pure-HTTP Git LFS Batch API client (`NewClient`, `BatchObject`)   |
| `transfer.go`    | Upload/download flows built on the Batch API client               |
| `meta.go`        | `SessionMeta`, `FileRef`, `HydrationStatus` — parallel OID manifest stored in `meta.json` alongside pointer files |
| `session_upload.go` | Session-specific upload flow built on transfer primitives      |

If you need to do anything LFS-related, it goes through one of these files.
No exceptions.

## Banned patterns

If you're about to write any of these, stop and reroute through `internal/lfs`:

```go
// BANNED — shelling out to git-lfs binary
exec.Command("git", "lfs", "...")
exec.Command("git-lfs", "...")
exec.LookPath("git-lfs")

// BANNED — assuming .gitattributes to gate LFS behavior
os.Stat(filepath.Join(repoPath, ".gitattributes"))  // in LFS-related code
strings.Contains(content, "filter=lfs")

// BANNED — writing .gitattributes with LFS filter declarations
os.WriteFile(".gitattributes", []byte("... filter=lfs ..."), 0644)

// BANNED — telling users to install git-lfs
"Install git-lfs: https://git-lfs.com"
"Run `git lfs install` to initialize LFS"
```

## Correct patterns

```go
import "github.com/sageox/ox/internal/lfs"

// Detect whether a file is an LFS pointer
if lfs.IsPointerFile(path) { ... }

// Parse a pointer file's OID + size
ref, err := lfs.ReadPointerFile(path)

// Write a pointer file (content already uploaded to the LFS store)
err := lfs.WritePointerFile(path, lfs.FileRef{OID: "sha256:...", Size: 1024})

// Upload or download blobs via the Batch API
client := lfs.NewClient(repoURL, "oauth2", token)
resp, err := client.Batch(ctx, "upload", []lfs.BatchObject{{OID: "...", Size: 1024}})
```

## The GitLab error trap

When a push fails with `remote: GitLab: LFS objects are missing. Ensure LFS
is properly set up or try a manual "git lfs push --all"` — the error message
is a **false trail**. The fix is never "install git-lfs and run git lfs push
--all." The correct response is one of:

1. The pointer files in the push reference OIDs that were never uploaded to
   GitLab's LFS store via `internal/lfs/client.go`. Find the missing uploads
   and complete them via the Batch API.
2. Or, the pointer files are stale/orphaned and should not be in the commit
   at all. Figure out which code path is creating pointer files without
   upload and fix the caller.

Do not paper over the error by invoking `git lfs push --all` — ox cannot
guarantee the binary is installed, the behavior differs across git-lfs
versions, and the resulting state is not reproducible from ox code.

## Enforcement

`make check-no-git-lfs-shell` greps the codebase for shell-out patterns and
fails the build if any reappear. It runs as part of `make test-preflight`.
