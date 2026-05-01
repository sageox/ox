//go:build !short

package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestEnsureOxPrimeMarker_ConcurrentInjections_NoDuplicates is the
// regression test for ox-l07b. Multiple adapters (or `ox doctor` racing
// with `ox prime`) calling EnsureOxPrimeMarker concurrently must
// converge on AGENTS.md with each marker present exactly once — no
// duplicates from a double-injection, no user edits silently dropped.
//
// Failure prevented: a CRDT-violating "two writers each inject" pattern
// where the file ends up with two header blocks or the second writer's
// in-memory copy overwrites a user paragraph the first writer added.
func TestEnsureOxPrimeMarker_ConcurrentInjections_NoDuplicates(t *testing.T) {
	gitRoot := t.TempDir()
	// AGENTS.md exists with user content but no markers — the realistic
	// pre-injection state on a project a coworker just opened.
	agentsPath := filepath.Join(gitRoot, "AGENTS.md")
	const userContent = "# Project Agents\n\nThis is user-authored guidance that\nMUST survive marker injection.\n"
	if err := os.WriteFile(agentsPath, []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}

	const N = 8
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := EnsureOxPrimeMarker(gitRoot); err != nil {
				t.Errorf("EnsureOxPrimeMarker: %v", err)
			}
		}()
	}
	wg.Wait()

	final, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(final)

	// User content must survive verbatim.
	if !strings.Contains(body, "user-authored guidance") || !strings.Contains(body, "MUST survive marker injection.") {
		t.Errorf("user content lost during concurrent injections:\n%s", body)
	}

	// Each marker present exactly once. Anything more means a writer
	// double-injected because it didn't see another writer's update —
	// the exact ox-l07b failure.
	if got := strings.Count(body, OxPrimeMarker); got != 1 {
		t.Errorf("OxPrimeMarker present %d times, want 1; concurrent writers double-injected", got)
	}
	if got := strings.Count(body, OxPrimeCheckMarker); got != 1 {
		t.Errorf("OxPrimeCheckMarker present %d times, want 1", got)
	}
}

// TestEnsureOxPrimeMarker_ConcurrentSeed_NoDuplicateFiles verifies the
// "neither AGENTS.md nor CLAUDE.md exists" path: when many goroutines
// hit it together, the file ends up with exactly one set of markers
// regardless of how many writers raced.
//
// Design note: this used to assert "exactly one writer reports
// injected" under a per-file flock. After the lock was removed (the
// realistic contended writer is the user's editor, not another ox
// process — flock between two ox processes does nothing about that),
// multiple writers MAY report injected; what matters is the
// post-condition of the file. The atomic write (random temp +
// rename) guarantees readers always see one or the other writer's
// complete bytes, never a torn intermediate, and the marker
// idempotence (strings.Contains) means re-running converges.
//
// Failure prevented: a future change re-introduces a non-atomic
// write to the seed path, allowing one writer to truncate-then-write
// while another reads, producing torn or empty AGENTS.md.
func TestEnsureOxPrimeMarker_ConcurrentSeed_NoDuplicateFiles(t *testing.T) {
	gitRoot := t.TempDir()
	// no AGENTS.md, no CLAUDE.md — the fresh-init failure window

	const N = 8
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := EnsureOxPrimeMarker(gitRoot); err != nil {
				t.Errorf("EnsureOxPrimeMarker: %v", err)
			}
		}()
	}
	wg.Wait()

	body, err := os.ReadFile(filepath.Join(gitRoot, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(body), OxPrimeMarker); got != 1 {
		t.Errorf("seed produced %d footer markers, want exactly 1\nbody:\n%s", got, body)
	}
	if got := strings.Count(string(body), OxPrimeCheckMarker); got != 1 {
		t.Errorf("seed produced %d header markers, want exactly 1\nbody:\n%s", got, body)
	}
}
