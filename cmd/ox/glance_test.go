package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/glance"
	"github.com/sageox/ox/internal/ledger"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

// runOxInProc runs one shipped command through the real root hooks, with its
// own flags and root's persistent flags reset to their defaults for the run.
func runOxInProc(t *testing.T, target *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	cmd := &cobra.Command{
		Use:                target.Name(),
		RunE:               target.RunE,
		PersistentPreRunE:  rootCmd.PersistentPreRunE,
		PersistentPostRunE: rootCmd.PersistentPostRunE,
		SilenceErrors:      true,
		SilenceUsage:       true,
	}
	// Copy only target's own flags. Once cobra has merged root's persistent
	// flags into target.Flags(), it never removes those copies, and a test that
	// replaces root's flag set leaves them stale.
	target.Flags().VisitAll(func(flag *pflag.Flag) {
		if rootCmd.PersistentFlags().Lookup(flag.Name) == nil {
			cmd.Flags().AddFlag(flag)
		}
	})
	// Persistent, as on rootCmd: the root hooks set --json on
	// cmd.Root().PersistentFlags() (publishJSONFlag), and cmd is its own root.
	cmd.PersistentFlags().AddFlagSet(rootCmd.PersistentFlags())
	// These flag values are shared with every test, so each run starts from
	// the defaults and restores them afterwards: a --since an earlier run left
	// set would otherwise decide this run's window.
	for _, set := range []*pflag.FlagSet{cmd.Flags(), cmd.PersistentFlags()} {
		set.VisitAll(func(flag *pflag.Flag) {
			value, changed := flag.Value.String(), flag.Changed
			t.Cleanup(func() {
				_ = flag.Value.Set(value)
				flag.Changed = changed
			})
			require.NoError(t, flag.Value.Set(flag.DefValue))
			flag.Changed = false
		})
	}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

// Failure prevented: `ox glance` stops reporting its project's own Ledger
// activity, or stops advancing the checkpoint a bare invocation resumes from.
func TestProjectGlanceStillReportsItsOwnLedger(t *testing.T) {
	projectRoot, ledgerPath := setupLedgerProject(t)
	t.Chdir(projectRoot)

	now := time.Now().UTC()
	body, err := json.Marshal(ledger.MurmurFile{
		SchemaVersion: "1",
		ID:            "mur_project",
		Timestamp:     now,
		AgentID:       "Ox0002",
		PrincipalID:   "avery",
		Topic:         "wip",
		Importance:    "normal",
		Content:       "project-scoped glance",
	})
	require.NoError(t, err)
	dir := filepath.Join(ledgerPath, ledger.MurmurDateHourDir(now))
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mur_project.json"), body, 0600))

	stdout, stderr, err := runOxInProc(t, glanceCmd,
		"--since", now.Add(-time.Hour).Format(time.RFC3339),
		"--until", now.Add(time.Hour).Format(time.RFC3339))
	require.NoError(t, err, stderr)

	var activity glance.ActivityData
	require.NoError(t, json.Unmarshal([]byte(stdout), &activity), stdout)
	require.Equal(t, filepath.Base(projectRoot), activity.Repo, "project reads label by directory, not repo ID")
	require.Equal(t, 1, activity.Stats.TotalMurmurs)

	// Reading advances the checkpoint, so a later bare invocation resumes from
	// here.
	require.False(t, glance.GetSince(ledgerPath).Before(now), "MarkRead must advance the checkpoint")

	// A malformed window is rejected before the ledger is consulted.
	_, _, err = runOxInProc(t, glanceCmd, "--since", "not-a-window")
	require.Error(t, err)
	_, _, err = runOxInProc(t, glanceCmd, "--until", "not-a-window")
	require.Error(t, err)

	// The default invocation resolves its own window from the checkpoint the
	// read above advanced, so it reports nothing new rather than replaying.
	stdout, stderr, err = runOxInProc(t, glanceCmd)
	require.NoError(t, err, stderr)
	require.NoError(t, json.Unmarshal([]byte(stdout), &activity), stdout)
	require.Equal(t, 0, activity.Stats.TotalMurmurs)
}

// Failure prevented: a project read that cannot see its activity reports a
// quiet ledger instead of failing. Each of these is a different way the ledger
// is unreadable, and every one of them must be louder than "nothing happened".
func TestProjectGlanceFailsRatherThanUnderReport(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name   string
		break_ func(t *testing.T, ledgerPath string)
	}{
		{"no ledger on disk", func(t *testing.T, ledgerPath string) {
			require.NoError(t, os.RemoveAll(ledgerPath))
		}},
		{"unreadable murmur", func(t *testing.T, ledgerPath string) {
			dir := filepath.Join(ledgerPath, ledger.MurmurDateHourDir(now))
			require.NoError(t, os.MkdirAll(dir, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0600))
		}},
		{"sessions is not a directory", func(t *testing.T, ledgerPath string) {
			// A regular file where a directory belongs fails on every platform,
			// unlike a chmod, and is what a bad extraction actually leaves behind.
			require.NoError(t, os.WriteFile(filepath.Join(ledgerPath, "sessions"), []byte("x"), 0600))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectRoot, ledgerPath := setupLedgerProject(t)
			t.Chdir(projectRoot)
			tc.break_(t, ledgerPath)

			stdout, _, err := runOxInProc(t, glanceCmd,
				"--since", now.Add(-time.Hour).Format(time.RFC3339),
				"--until", now.Add(time.Hour).Format(time.RFC3339))
			require.Error(t, err)
			require.Empty(t, stdout, "a failed harvest must not render as a quiet ledger")
		})
	}
}

// Failure prevented: the activity checkpoint advances before the activity is
// delivered, so a failed write silently skips that window forever — the next
// bare `ox glance` resumes past murmurs the consumer never received.
func TestProjectGlanceKeepsTheCheckpointWhenOutputFails(t *testing.T) {
	projectRoot, ledgerPath := setupLedgerProject(t)
	t.Chdir(projectRoot)

	now := time.Now().UTC()
	body, err := json.Marshal(ledger.MurmurFile{
		SchemaVersion: "1", ID: "mur_x", Timestamp: now,
		AgentID: "Ox0003", PrincipalID: "riley", Topic: "wip",
		Importance: "normal", Content: "must survive a failed write",
	})
	require.NoError(t, err)
	dir := filepath.Join(ledgerPath, ledger.MurmurDateHourDir(now))
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mur_x.json"), body, 0600))

	cmd := &cobra.Command{Use: "glance", RunE: glanceCmd.RunE, SilenceErrors: true, SilenceUsage: true}
	cmd.Flags().AddFlagSet(glanceCmd.Flags())
	cmd.PersistentFlags().AddFlagSet(rootCmd.PersistentFlags())
	// A closed pipe is what a consumer that hung up actually leaves behind.
	r, w := io.Pipe()
	require.NoError(t, r.Close())
	defer w.Close()
	cmd.SetOut(w)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--since", now.Add(-time.Hour).Format(time.RFC3339)})
	require.Error(t, cmd.Execute(), "a failed write must surface")

	// With no checkpoint recorded, GetSince falls back to DefaultWindow ago.
	// MarkRead would have stored ~now instead, so the distance from now is what
	// separates "never advanced" from "advanced past undelivered activity" —
	// comparing against a pre-run GetSince cannot, since the fallback itself
	// moves with the clock.
	resume := glance.GetSince(ledgerPath)
	require.Greater(t, time.Since(resume), time.Hour,
		"checkpoint advanced to %v despite the write failing; the next glance would skip that window", resume)
}
