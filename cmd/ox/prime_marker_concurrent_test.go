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
// hit it together, exactly ONE creates the file and the rest see it.
// Without per-file flock, multiple writers each truncate-create the
// file and the result is non-deterministic.
//
// Failure prevented: race between adapters during a fresh `ox init`
// where one adapter's write clobbers another's freshly-seeded content.
func TestEnsureOxPrimeMarker_ConcurrentSeed_NoDuplicateFiles(t *testing.T) {
	gitRoot := t.TempDir()
	// no AGENTS.md, no CLAUDE.md — the fresh-init failure window

	const N = 8
	var wg sync.WaitGroup
	var injectedCount int
	var mu sync.Mutex
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			injected, err := EnsureOxPrimeMarker(gitRoot)
			if err != nil {
				t.Errorf("EnsureOxPrimeMarker: %v", err)
				return
			}
			if injected {
				mu.Lock()
				injectedCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Exactly one writer should report "injected" — the rest hit the
	// double-check inside the lock and bail out.
	if injectedCount != 1 {
		t.Errorf("injectedCount = %d, want 1 (multiple writers raced past the seed guard)", injectedCount)
	}

	body, err := os.ReadFile(filepath.Join(gitRoot, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(body), OxPrimeMarker); got != 1 {
		t.Errorf("seed produced %d footer markers, want 1", got)
	}
	if got := strings.Count(string(body), OxPrimeCheckMarker); got != 1 {
		t.Errorf("seed produced %d header markers, want 1", got)
	}
}
