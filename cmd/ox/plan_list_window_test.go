package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// savedPlanFixture stages one saved plan directly in the ledger at a known
// time. Writing meta.json is what `ox plan save` ultimately leaves behind, and
// it is the only way to pin created_at to a past instant.
type savedPlanFixture struct {
	slug string
	age  time.Duration
}

func stagePlanLedger(t *testing.T, fixtures ...savedPlanFixture) time.Time {
	t.Helper()
	root := newPlanCaptureTestRepo(t)
	ledger := initPlanTestLedger(t, root)
	plansDir := paths.LedgerPlansDir(ledger)

	now := time.Now()
	for _, f := range fixtures {
		created := now.Add(-f.age)
		dir := filepath.Join(plansDir, created.Format("2006-01-02")+"-"+f.slug)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		meta, err := json.Marshal(plan.Meta{Topic: "Topic of " + f.slug, Slug: f.slug, CreatedAt: created})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o644))
	}
	return now
}

// runPlanListFlags drives the production flag surface and RunE.
func runPlanListFlags(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := &cobra.Command{Use: "list", RunE: planListCmd.RunE, SilenceUsage: true, SilenceErrors: true}
	cmd.SetContext(context.Background())
	registerPlanListFlags(cmd)

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	var err error
	// printUnsavedArtifactHint writes to os.Stdout; keep it out of the test log
	captureStdoutForPlanCLI(t, func() { err = cmd.Execute() })
	return buf.String(), err
}

func listedPlanSlugs(t *testing.T, jsonOut string) []string {
	t.Helper()
	var plans []struct{ Slug string }
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &plans), "output: %q", jsonOut)
	slugs := make([]string, 0, len(plans))
	for _, p := range plans {
		slugs = append(slugs, p.Slug)
	}
	return slugs
}

// TestPlanList_LimitSinceUntilAll: `ox plan list` answers to the vocabulary
// `ox session list` and `ox conversation list` already speak.
// Failure prevented: `plan list --limit 5` is "unknown flag" (19 observed
// failures), or a window silently ignores one of its ends.
func TestPlanList_LimitSinceUntilAll(t *testing.T) {
	now := stagePlanLedger(t,
		savedPlanFixture{"alpha", 30 * 24 * time.Hour},
		savedPlanFixture{"bravo", 3 * 24 * time.Hour},
		savedPlanFixture{"charlie", time.Hour},
	)
	bravoDay := now.Add(-3 * 24 * time.Hour).Format("2006-01-02")

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"default lists every plan, newest first", nil, []string{"charlie", "bravo", "alpha"}},
		{"limit keeps the newest", []string{"--limit", "2"}, []string{"charlie", "bravo"}},
		{"limit 0 means no limit", []string{"--limit", "0"}, []string{"charlie", "bravo", "alpha"}},
		{"limit above the count is harmless", []string{"--limit", "50"}, []string{"charlie", "bravo", "alpha"}},
		{"since keeps the newer end", []string{"--since", "7d"}, []string{"charlie", "bravo"}},
		{"until keeps the older end", []string{"--until", "10d"}, []string{"alpha"}},
		{"since and until pick the middle", []string{"--since", "7d", "--until", "2d"}, []string{"bravo"}},
		{"limit applies after the window", []string{"--since", "40d", "--until", "2d", "--limit", "1"}, []string{"bravo"}},
		{"a bare date is the whole day on both ends", []string{"--since", bravoDay, "--until", bravoDay}, []string{"bravo"}},
		{"window with no match", []string{"--since", "10m"}, []string{}},
		// plan list has no default view that hides rows, so --all changes nothing
		{"all is accepted and is a no-op", []string{"--all"}, []string{"charlie", "bravo", "alpha"}},
		{"all with a window still honors the window", []string{"--all", "--since", "7d"}, []string{"charlie", "bravo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runPlanListFlags(t, append([]string{"--json"}, tt.args...)...)
			require.NoError(t, err)
			if got := listedPlanSlugs(t, out); !slices.Equal(got, tt.want) {
				t.Errorf("plans = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPlanList_InvalidFlagsAreUsageErrors: a bad value names its flag and a
// working example instead of listing nothing.
func TestPlanList_InvalidFlagsAreUsageErrors(t *testing.T) {
	stagePlanLedger(t, savedPlanFixture{"alpha", time.Hour})

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"bad since", []string{"--since", "lately"}, []string{"--since", "lately", "2026-10-01"}},
		{"bad until", []string{"--until", "soon"}, []string{"--until", "soon", "2026-10-01"}},
		{"inverted window", []string{"--since", "1d", "--until", "7d"}, []string{"--since", "--until"}},
		{"negative limit", []string{"--limit", "-1"}, []string{"--limit", "-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := runPlanListFlags(t, append([]string{"--json"}, tt.args...)...)
			require.Error(t, err)
			require.Empty(t, strings.TrimSpace(out), "a refused invocation must not print a partial list")
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}
}

// TestPlanList_HumanOutputSaysWhenItIsTruncated: the table must not pretend a
// capped page is the whole ledger, and an empty window must not claim there
// are no plans at all.
func TestPlanList_HumanOutputSaysWhenItIsTruncated(t *testing.T) {
	stagePlanLedger(t,
		savedPlanFixture{"alpha", 30 * 24 * time.Hour},
		savedPlanFixture{"bravo", 3 * 24 * time.Hour},
	)

	out, err := runPlanListFlags(t, "--limit", "1")
	require.NoError(t, err)
	require.Contains(t, out, "bravo")
	require.NotContains(t, out, "alpha")
	require.Contains(t, out, "1 of 2 plans shown", "a capped table must say how many it hid")

	out, err = runPlanListFlags(t, "--since", "10m")
	require.NoError(t, err)
	require.Contains(t, out, "No saved plans match that window (2 saved)")
	require.NotContains(t, out, "No saved plans yet", "plans exist; only the window is empty")
}
