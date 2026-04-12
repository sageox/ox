// Package lfs implements Git LFS pointer handling and Batch API transfers
// in pure Go.
//
// # Architectural rule (read before editing any file in this package)
//
// ox never depends on the `git-lfs` binary being installed on the user's
// machine. All LFS operations — pointer detection, pointer parsing, pointer
// writing, blob upload, blob download, hydration — are implemented natively
// in this package. Do NOT shell out to `git lfs ...`. Do NOT call
// exec.LookPath("git-lfs"). Do NOT write .gitattributes files that would
// trigger git-lfs smudge/clean filters on checkout.
//
// The rationale:
//
//   - Users of ox almost never have the git-lfs binary installed; requiring
//     it would break the CLI for the majority of coworkers.
//   - Committing .gitattributes with filter=lfs entries would auto-hydrate
//     LFS content on every `git checkout`, which we explicitly do not want —
//     dehydrated clones are the default.
//   - ox uses the LFS *concept* (content-addressed pointer files referencing
//     blobs stored out-of-band) but implements the transport via pure-HTTP
//     calls to GitLab's Git LFS Batch API in client.go and transfer.go.
//
// # What lives where
//
//   - pointer.go  — FormatPointer / ParsePointer / IsPointerFile / WritePointerFile
//     (canonical spec-compliant pointer I/O, no binary dep)
//   - client.go   — Pure-HTTP Git LFS Batch API client (upload/download)
//   - transfer.go — Upload/download flows using the Batch API
//   - meta.go     — SessionMeta / FileRef: the parallel OID manifest stored
//     in meta.json alongside pointer files
//
// # If you need to work with LFS from another package
//
//   - Detect a pointer file:         lfs.IsPointerFile(path)
//   - Parse a pointer file:          lfs.ParsePointer(content) / lfs.ReadPointerFile(path)
//   - Write a pointer file:          lfs.WritePointerFile(path, FileRef{...})
//   - Upload/download blob content:  lfs.NewClient(repoURL, user, token).Batch(...)
//
// If you find yourself writing `exec.Command("git", "lfs", ...)` anywhere in
// the ox codebase — stop and come back here. The answer is always "use this
// package directly." See .claude/rules/lfs-no-git-lfs-binary.md for the full
// rationale and the list of banned patterns.
package lfs
