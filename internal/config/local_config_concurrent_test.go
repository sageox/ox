package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// TestMutateLocalConfig_ConcurrentSetTeamContext_NoLostRows is the
// regression test for ox-dfy4. Two writers (the daemon discovering
// teams via /api/v1/cli/repos and the CLI updating the same file
// during ox login / ox init) racing on SetTeamContext must converge
// with EVERY team they wrote present in the final file — none lost
// to the read-modify-write window.
//
// Failure prevented: silent loss of [[team_contexts]] rows when two
// processes update config.local.toml concurrently. Surfaces in
// production as "team I joined yesterday isn't visible in ox status"
// or, worse, a daemon's path entries getting clobbered by a CLI
// update so subsequent syncs go to the wrong filesystem location.
func TestMutateLocalConfig_ConcurrentSetTeamContext_NoLostRows(t *testing.T) {
	if testing.Short() {
		t.Skip("short: spawns concurrent writers")
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}

	// seed an empty config so MutateLocalConfig has something to load
	if err := SaveLocalConfig(root, &LocalConfig{}); err != nil {
		t.Fatal(err)
	}

	const N = 12
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := MutateLocalConfig(context.Background(), root, func(cfg *LocalConfig) error {
				cfg.SetTeamContext(
					fmt.Sprintf("team_%02d", i),
					fmt.Sprintf("Team %02d", i),
					fmt.Sprintf("/some/path/team_%02d", i),
				)
				// expose the read-modify-write window deterministically
				for j := 0; j < 4; j++ {
					runtime.Gosched()
				}
				return nil
			})
			if err != nil {
				t.Errorf("mutate %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	final, err := LoadLocalConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tc := range final.TeamContexts {
		got[tc.TeamID] = true
	}
	for i := 0; i < N; i++ {
		id := fmt.Sprintf("team_%02d", i)
		if !got[id] {
			t.Errorf("team_contexts row %q lost to a concurrent write", id)
		}
	}
	if len(final.TeamContexts) != N {
		t.Errorf("expected exactly %d team_contexts rows, got %d (duplicates or losses)",
			N, len(final.TeamContexts))
	}
}

// TestSaveLocalConfig_DedupsTeamContexts pins the dedup invariant: even
// if a producer hands us a LocalConfig with duplicates, the on-disk
// file MUST NOT inherit them. Otherwise a future read would surface
// the same team twice and ox status / pickers would render duplicates.
//
// Failure prevented: a future caller bypasses SetTeamContext and
// appends directly to cfg.TeamContexts, leaking duplicates onto disk.
func TestSaveLocalConfig_DedupsTeamContexts(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &LocalConfig{
		TeamContexts: []TeamContext{
			{TeamID: "t1", TeamName: "first", Path: "/p1"},
			{TeamID: "t2", TeamName: "second", Path: "/p2"},
			{TeamID: "t1", TeamName: "first-updated", Path: "/p1-new"}, // duplicate
		},
	}
	if err := SaveLocalConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLocalConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TeamContexts) != 2 {
		t.Fatalf("expected 2 rows after dedup, got %d", len(got.TeamContexts))
	}
	// last write wins for the duplicated id
	for _, tc := range got.TeamContexts {
		if tc.TeamID == "t1" && tc.TeamName != "first-updated" {
			t.Errorf("t1 row not deduped to last write: got name=%q want first-updated", tc.TeamName)
		}
	}
}
