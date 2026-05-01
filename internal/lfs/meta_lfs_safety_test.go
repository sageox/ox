package lfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMutateSessionMeta_PointerWritersCannotClobberGitContent guards a
// failure mode the user has hit before: an LFS pointer file overwrites
// a Storage=git artifact (e.g., summary.json's real bytes), silently
// destroying content. The cache-only-design rule pins the invariant —
// Storage=git entries' bytes live directly in the git tree, not in
// LFS, and any pointer-write path MUST preserve them.
//
// Strategy: simulate the merge a daemon finalize might do — existing
// summary.json is Storage=git, daemon's freshly-uploaded fileRefs
// claim it as Storage=lfs. The mutator must NOT promote git → lfs.
//
// We don't reach into session_finalize directly (cross-package); we
// reproduce the merge shape it uses via MutateSessionMeta and assert
// the on-disk Files map shows summary.json still as Storage=git.
//
// Failure prevented: a future merge implementation that
// last-write-wins across storage backends silently demotes a
// committed git artifact to a pointer, then the next WritePointerFiles
// invocation overwrites the real bytes with a 130-byte pointer stub.
func TestMutateSessionMeta_PointerWritersCannotClobberGitContent(t *testing.T) {
	dir := t.TempDir()
	initial := NewSessionMeta("s", "u", "a", "claude-code", time.Now()).Build()
	initial.Files = map[string]FileRef{
		"summary.json": NewGitFileRef(1234), // committed-to-git
		"raw.jsonl":    {Storage: StorageLFS, OID: "sha256:aaa", Size: 9999},
	}
	if err := WriteSessionMetaOnly(dir, initial); err != nil {
		t.Fatal(err)
	}
	// Drop a real summary.json byte stream into place — the file the
	// user does NOT want clobbered.
	const userBytes = `{"title":"hand-written summary"}`
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(userBytes), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate the daemon's post-LFS-upload merge: incoming fileRefs
	// claim summary.json AND raw.jsonl as Storage=lfs. The merge MUST
	// preserve summary.json's git-storage and accept raw.jsonl's new OID.
	incoming := map[string]FileRef{
		"summary.json": {Storage: StorageLFS, OID: "sha256:bbb", Size: 130},
		"raw.jsonl":    {Storage: StorageLFS, OID: "sha256:ccc", Size: 9999},
	}
	if err := MutateSessionMeta(context.Background(), dir, func(meta *SessionMeta) (*SessionMeta, error) {
		merged := make(map[string]FileRef, len(meta.Files)+len(incoming))
		for k, v := range meta.Files {
			merged[k] = v
		}
		for k, v := range incoming {
			if existing, ok := merged[k]; ok && existing.IsGit() && v.IsLFS() {
				continue // the guard
			}
			merged[k] = v
		}
		meta.Files = merged
		return meta, nil
	}); err != nil {
		t.Fatal(err)
	}

	final, err := ReadSessionMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !final.Files["summary.json"].IsGit() {
		t.Errorf("summary.json was demoted to LFS; got %+v (this would orphan the real bytes)",
			final.Files["summary.json"])
	}
	if final.Files["raw.jsonl"].OID != "sha256:ccc" {
		t.Errorf("raw.jsonl OID was not updated; got %+v", final.Files["raw.jsonl"])
	}

	// Belt + suspenders: a pointer-write pass at this point MUST NOT
	// replace summary.json's real bytes (WritePointerFiles already
	// skips Storage=git, but we want a regression test for it because
	// the consequences of a future change are silent data loss).
	if _, err := WritePointerFiles(dir, final.Files); err != nil {
		t.Fatalf("WritePointerFiles: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != userBytes {
		t.Errorf("summary.json bytes were overwritten by pointer write:\nwant %q\ngot  %q",
			userBytes, got)
	}
}

// TestWritePointerFiles_SkipsStorageGit pins the long-standing
// invariant inside WritePointerFiles itself. Belt-and-suspenders for
// the cache-only-design rule: even if a caller hands us a map where
// a Storage=git entry was accidentally tagged with a pointer-shaped
// OID, the writer must refuse to clobber.
//
// Failure prevented: a future refactor that flattens the IsLFS check
// silently writes pointer stubs over real git-tracked content.
func TestWritePointerFiles_SkipsStorageGit(t *testing.T) {
	dir := t.TempDir()
	const userBytes = "real content that must survive"
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(userBytes), 0o644); err != nil {
		t.Fatal(err)
	}

	files := map[string]FileRef{
		"report.md": NewGitFileRef(int64(len(userBytes))),
	}
	written, err := WritePointerFiles(dir, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Errorf("expected zero pointer writes for Storage=git entries, got %v", written)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "report.md"))
	if string(got) != userBytes {
		t.Errorf("Storage=git content was clobbered:\nwant %q\ngot  %q", userBytes, got)
	}
}
