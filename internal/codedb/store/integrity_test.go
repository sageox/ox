package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/blevesearch/bleve/v2"
)

// --- probeBleveIndex ---

// TestProbeBleveIndex_EmptyIndexPasses verifies a fresh empty Bleve index
// (no segments) passes the probe. Document() on a non-existent ID against
// an empty index returns (nil, nil) without touching any FST.
//
// Failure prevented: probe rejects freshly-opened indexes and triggers
// needless full reindexes during normal daemon startup.
func TestProbeBleveIndex_EmptyIndexPasses(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: bleve open")
	}
	mapping := bleve.NewIndexMapping()
	idx, err := bleve.NewMemOnly(mapping)
	if err != nil {
		t.Fatalf("NewMemOnly: %v", err)
	}
	defer func() { _ = idx.Close() }()

	if err := probeBleveIndex(idx, "code"); err != nil {
		t.Errorf("expected nil for empty index, got: %v", err)
	}
}

// TestProbeBleveIndex_PopulatedIndexExercisesFST indexes a real document so
// segments exist with _id and field FSTs, then probes. Document() forces
// segment.Dictionary("_id") loading via TermFieldReader (synchronous,
// caller goroutine). If the probe was regressed to bypass FST loading
// (e.g. silently switched back to a doc-id-bitmap query), this test would
// still pass — but a real corrupt-bytes scenario in CI would catch a
// real-world miss.
//
// Failure prevented: the probe is rewritten to skip segment dictionary
// loading and silently passes on indexes whose FST is unloadable.
func TestProbeBleveIndex_PopulatedIndexExercisesFST(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short: bleve open + index")
	}
	mapping := bleve.NewIndexMapping()
	idx, err := bleve.NewMemOnly(mapping)
	if err != nil {
		t.Fatalf("NewMemOnly: %v", err)
	}
	defer func() { _ = idx.Close() }()

	if err := idx.Index("doc1", map[string]string{"content": "hello world"}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	if err := probeBleveIndex(idx, "code"); err != nil {
		t.Errorf("expected nil for healthy populated index, got: %v", err)
	}
}

// --- runBleveProbe ---

// TestRunBleveProbe_RecoversPanicAsErrCorrupt verifies that a panic from
// the probe closure (e.g. nil FST in a corrupt segment) is converted to
// ErrCorrupt instead of crashing the daemon.
//
// Failure prevented: nil-FST panic during the integrity check kills the
// daemon process rather than reporting corruption — and the deferred
// db.Close() in doIndex deadlocks on the still-held bbolt mutex.
func TestRunBleveProbe_RecoversPanicAsErrCorrupt(t *testing.T) {
	t.Parallel()
	err := runBleveProbe("comment", func() error {
		panic("simulated vellum.(*FST).GetMaxKey nil deref")
	})
	if err == nil {
		t.Fatal("expected error from panicking probe, got nil")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("expected ErrCorrupt, got: %v", err)
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Errorf("error should mention panic, got: %v", err)
	}
	if !strings.Contains(err.Error(), "comment") {
		t.Errorf("error should name the failing index, got: %v", err)
	}
}

// TestRunBleveProbe_SearchErrorWrappedAsErrCorrupt verifies that a normal
// (non-panic) error returned by the probe is wrapped as ErrCorrupt —
// preserves the pre-existing behavior of CheckIntegrity treating any
// probe failure as corruption.
func TestRunBleveProbe_SearchErrorWrappedAsErrCorrupt(t *testing.T) {
	t.Parallel()
	err := runBleveProbe("diff", func() error {
		return errors.New("search subsystem broken")
	})
	if err == nil {
		t.Fatal("expected error from erroring probe, got nil")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("expected ErrCorrupt, got: %v", err)
	}
	if !strings.Contains(err.Error(), "diff") {
		t.Errorf("error should name the failing index, got: %v", err)
	}
}

// TestRunBleveProbe_NilOnSuccess verifies the happy path returns nil.
func TestRunBleveProbe_NilOnSuccess(t *testing.T) {
	t.Parallel()
	if err := runBleveProbe("code", func() error { return nil }); err != nil {
		t.Errorf("expected nil, got: %v", err)
	}
}

// TestRunBleveProbe_RecoversNonStringPanic verifies the recover handles
// runtime panics (e.g. nil pointer deref), not just string panics. This is
// the actual shape of the in-the-wild incident: vellum dereferences a nil
// FST pointer, which raises runtime.Error rather than a string.
func TestRunBleveProbe_RecoversNonStringPanic(t *testing.T) {
	t.Parallel()
	err := runBleveProbe("code", func() error {
		var p *int
		_ = *p
		return nil
	})
	if err == nil {
		t.Fatal("expected error from nil-deref panic, got nil")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("expected ErrCorrupt, got: %v", err)
	}
}
