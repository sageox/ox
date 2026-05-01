package lfs

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)


// TestSessionMeta_ConcurrentReadModifyWrite_DoesNotLoseFields is the
// regression test for ox-e1ot.
//
// Two writers — daemon (session_finalize.go after summarization) and CLI
// (session_upload.go via registerGitArtifactInMeta / backfillGitArtifactInMeta)
// — race on the same meta.json with no flock, no version field, no CAS.
// Today the byte-level write is atomic (temp + rename), but the
// read-modify-write window is unprotected:
//
//	   daemon                        CLI
//	   ----------------------       ----------------------
//	   meta := Read(metaPath)
//	                                 meta := Read(metaPath)
//	   meta.Summary = "..."
//	                                 meta.Files["x.json"] = ...
//	   Write(meta)
//	                                 Write(meta)   ← clobbers Summary
//
// Whichever writer commits second silently overwrites the other's
// in-memory copy of the file. Both writers carry the value they care
// about, so the data isn't observably wrong from EITHER writer's
// perspective — but the union is lost.
//
// In production this surfaces as: daemon writes Summary + Files manifest
// post-finalize, CLI later registers a git artifact and (because it
// reads stale bytes) writes back without the Summary, so the next push
// goes out with no summary recorded. Or worse, the LFS pointer manifest
// the CLI wrote is dropped by a concurrent daemon update, so push fails
// LFS reachability.
//
// THIS TEST IS EXPECTED TO FAIL TODAY. It's checked in to lock the
// failure mode in place so the fix (flock / version field / CAS — see
// ox-e1ot acceptance criteria) lands with a passing assertion.
//
// Failure prevented: silent meta.json field loss when daemon and CLI
// both update the manifest concurrently.
func TestSessionMeta_ConcurrentReadModifyWrite_DoesNotLoseFields(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns concurrent writers")
	}
	dir := t.TempDir()

	// Seed an initial meta.json that both writers will modify.
	initial := NewSessionMeta("session-x", "user", "agent-1", "claude-code", time.Now()).
		Build()
	if err := WriteSessionMetaOnly(dir, initial); err != nil {
		t.Fatal(err)
	}

	// Daemon role: read → set Summary → write, all under MutateSessionMeta.
	const wantSummary = "daemon: summarized"
	daemonWrite := func() {
		err := MutateSessionMeta(context.Background(), dir, func(meta *SessionMeta) (*SessionMeta, error) {
			if meta == nil {
				t.Errorf("daemon: meta missing")
				return nil, nil
			}
			meta.Summary = wantSummary
			// hold the in-memory copy for a beat to widen the race window
			// without sleeping — pure goroutine reschedule.
			yield()
			return meta, nil
		})
		if err != nil {
			t.Errorf("daemon mutate: %v", err)
		}
	}

	// CLI role: read → register a git artifact → write, also under
	// MutateSessionMeta. Both writers MUST go through it for the lock to
	// matter; that's the contract callers in production must follow.
	const wantFile = "summary.json"
	const wantSize = int64(1234)
	cliWrite := func() {
		err := MutateSessionMeta(context.Background(), dir, func(meta *SessionMeta) (*SessionMeta, error) {
			if meta == nil {
				t.Errorf("cli: meta missing")
				return nil, nil
			}
			if meta.Files == nil {
				meta.Files = map[string]FileRef{}
			}
			meta.Files[wantFile] = NewGitFileRef(wantSize)
			yield()
			return meta, nil
		})
		if err != nil {
			t.Errorf("cli mutate: %v", err)
		}
	}

	// Run them concurrently. The race window is open between Read and
	// Write in each goroutine — whichever writes second will not have
	// seen the other's mutation, so the loser's field disappears.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); daemonWrite() }()
	go func() { defer wg.Done(); cliWrite() }()
	wg.Wait()

	// Read final state. The fix's contract: BOTH fields must survive.
	final, err := ReadSessionMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if final.Summary != wantSummary {
		t.Errorf("Summary lost in race: want %q, got %q (daemon's write was clobbered by CLI)",
			wantSummary, final.Summary)
	}
	if ref, ok := final.Files[wantFile]; !ok || ref.Size != wantSize {
		t.Errorf("Files[%q] lost in race: want size=%d, got %+v (CLI's write was clobbered by daemon)",
			wantFile, wantSize, ref)
	}

	// Verify the disk file (post-rename) is intact and parseable. This
	// catches the secondary failure mode: temp/rename leaves a half-
	// written file or removes the original when both writers race on
	// the temp path.
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Errorf("post-race meta.json unreadable: %v", err)
	}
	if len(data) == 0 {
		t.Error("post-race meta.json is empty")
	}
}

// TestSessionMeta_RepeatedConcurrentWrites_ConvergesWithoutLoss exercises
// a tighter version of the race: many short read-modify-write rounds
// from each side. Without serialization, this loses fields with very
// high probability; with proper serialization (the fix), every round
// must contribute its mutation to the final state.
//
// Why a separate test: the single-shot race above can occasionally
// "pass" by luck — both writes happen far enough apart that one finishes
// before the other reads. Under N rounds the bug is essentially certain
// to manifest, making this the more reliable canary post-fix.
//
// Failure prevented: a fix that only narrows but doesn't close the
// read-modify-write window.
func TestSessionMeta_RepeatedConcurrentWrites_ConvergesWithoutLoss(t *testing.T) {
	if testing.Short() {
		t.Skip("short: many concurrent writes")
	}
	dir := t.TempDir()
	if err := WriteSessionMetaOnly(dir, NewSessionMeta("s", "u", "a", "claude-code", time.Now()).Build()); err != nil {
		t.Fatal(err)
	}

	const rounds = 50
	var wg sync.WaitGroup
	wg.Add(2)

	// Each side increments a different conceptual counter (daemon
	// extends Files, CLI extends Summary). After both finish, the file
	// must contain ALL daemon-side entries AND the final CLI-side string.
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			err := MutateSessionMeta(context.Background(), dir, func(meta *SessionMeta) (*SessionMeta, error) {
				if meta == nil {
					return nil, nil
				}
				if meta.Files == nil {
					meta.Files = map[string]FileRef{}
				}
				meta.Files[fileKey(i)] = NewGitFileRef(int64(i))
				yield()
				return meta, nil
			})
			if err != nil {
				t.Errorf("daemon mutate round %d: %v", i, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			err := MutateSessionMeta(context.Background(), dir, func(meta *SessionMeta) (*SessionMeta, error) {
				if meta == nil {
					return nil, nil
				}
				meta.Summary = summaryFor(i)
				yield()
				return meta, nil
			})
			if err != nil {
				t.Errorf("cli mutate round %d: %v", i, err)
				return
			}
		}
	}()
	wg.Wait()

	final, err := ReadSessionMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	missing := 0
	for i := 0; i < rounds; i++ {
		if _, ok := final.Files[fileKey(i)]; !ok {
			missing++
		}
	}
	if missing > 0 {
		t.Errorf("%d/%d daemon-written file entries lost to CLI clobbers", missing, rounds)
	}
	if final.Summary == "" {
		t.Error("CLI Summary entirely missing — every cli write was clobbered")
	}
}

// fileKey/summaryFor: small helpers that keep the test bodies above terse
// and make the per-round payloads inspectable in failures.
func fileKey(i int) string  { return "f-" + itoa(i) + ".json" }
func summaryFor(i int) string { return "summary-" + itoa(i) }
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	const digits = "0123456789"
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = digits[i%10]
		i /= 10
	}
	return string(b[pos:])
}

// yield triggers a goroutine reschedule without sleeping. Widens the
// race window deterministically — far more reliable than time.Sleep
// for exposing read-modify-write hazards.
func yield() {
	for i := 0; i < 8; i++ {
		runtime.Gosched()
	}
}
