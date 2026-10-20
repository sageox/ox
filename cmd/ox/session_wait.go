package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/errkind"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

var sessionWaitCmd = &cobra.Command{
	Use:   "wait --since <time>",
	Short: "Finish and upload this machine's sessions, and report what reached the Ledger",
	Long: `Make sure every session recorded on this machine reached the Ledger, for a
machine that goes away after its job, such as a CI-like pod. Run it from the
project after the AI coworkers on the machine have exited.

Where the ox daemon will not outlive the session (the daemon is off, or the
machine goes away with its AI coworker), it finishes every recording whose AI
coworker is gone, as session end would have, publishes every session still
waiting on this machine, as 'ox session upload' does, and pushes Ledger
commits an earlier push left behind. Where the daemon uploads sessions, it
leaves them to the daemon and only reports.

It reports how many sessions finished since --since and how many of those are
in the Ledger, the older pending sessions it found, and every session that is
not in the Ledger with the reason. It exits 0 only when every session is in
the Ledger.

Examples:
  ox session wait --since 2026-10-12T19:00:00Z --json`,
	Args: cobra.NoArgs,
	RunE: runSessionWait,
}

func init() {
	sessionCmd.AddCommand(sessionWaitCmd)
	sessionWaitCmd.Flags().String("since", "", "count sessions that finished at or after this time (RFC3339 or YYYY-MM-DD); required")
}

// sessionWaitOutput is the --json contract a runner reads.
type sessionWaitOutput struct {
	Since        int                  `json:"since"`         // sessions that finished at or after --since
	InLedger     int                  `json:"in_ledger"`     // of those, the ones on the Ledger's remote
	OlderPending []string             `json:"older_pending"` // pending sessions that finished before --since
	Failed       []sessionWaitFailure `json:"failed"`        // every session not in the Ledger, any age
}

type sessionWaitFailure struct {
	Session string `json:"session"`
	Error   string `json:"error"`
}

func runSessionWait(cmd *cobra.Command, _ []string) error {
	raw, _ := cmd.Flags().GetString("since")
	since, err := parseConversationSince(raw)
	if err != nil {
		return err
	}
	if since.IsZero() {
		return errkind.Errorf(errkind.Usage, "--since needs a time: RFC3339 or YYYY-MM-DD")
	}
	// requireProjectRoot accepts any directory holding a .sageox folder, and the
	// legacy ~/.sageox makes $HOME one.
	projectRoot, err := requireProjectRoot()
	if err == nil && !config.IsInitialized(projectRoot) {
		err = errkind.Errorf(errkind.NotInitialized, "not in a SageOx project: %s is not initialized\nRun this command from a repository where SageOx has been initialized", projectRoot)
	}
	if err != nil {
		return err
	}
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return err
	}

	out, err := waitForSessions(cmd.Context(), projectRoot, ledgerPath, since)
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	if cfg != nil && cfg.JSON {
		err = cli.PrintJSONTo(w, out)
	} else {
		printSessionWait(w, out, since)
	}
	if err == nil && len(out.Failed) > 0 {
		err = silentFailure(errkind.Other, "sessions_not_in_ledger", nil)
	}
	return err
}

// waitForSessions uploads this project's sessions through the paths that own
// them, then reports what is in the Ledger. The daemon owns uploads wherever
// it outlives the session; there this only reports, so the two never work on
// the same session.
func waitForSessions(ctx context.Context, projectRoot, ledgerPath string, since time.Time) (sessionWaitOutput, error) {
	out := sessionWaitOutput{OlderPending: []string{}, Failed: []sessionWaitFailure{}}
	cliUploads := !daemonOutlivesSession()
	reported := map[string]bool{}
	fail := func(name, reason string, finished time.Time) {
		reported[name] = true
		out.Failed = append(out.Failed, sessionWaitFailure{Session: name, Error: reason})
		if !finished.IsZero() && !finished.Before(since) {
			out.Since++
		}
	}
	notePending := func(name string, finished time.Time) {
		if finished.Before(since) {
			out.OlderPending = append(out.OlderPending, name)
		}
	}

	recordings, err := session.LoadAllRecordingStates(projectRoot)
	if err != nil {
		return out, err
	}
	for _, rec := range recordings {
		name := session.GetSessionName(rec.SessionPath)
		switch {
		case rec.SourceRejected:
			fail(name, "held for ownership review", time.Time{})
		case rec.IsAgentAlive():
			fail(name, "still recording", time.Time{})
		case !cliUploads:
			fail(name, "its AI coworker is gone; the daemon finishes it", time.Time{})
		default:
			// The AI coworker died before its session end ran; run it now.
			finished := session.ResolveStoppedAt(rec.StoppedAt, filepath.Join(rec.SessionPath, ledgerFileRaw), time.Now())
			notePending(name, finished)
			if err := finishRecordingAtEnd(projectRoot, rec.AgentID); err != nil {
				fail(name, err.Error(), finished)
			}
		}
	}

	unpublished := listUnpublishedCacheSessions(ledgerPath)
	for _, name := range unpublished.Held {
		if !reported[name] {
			fail(name, "held on this machine; publish it with ox session upload "+name, time.Time{})
		}
	}
	for _, name := range unpublished.Waiting {
		if reported[name] {
			continue // one attempt per run
		}
		_, cacheDir, ok, err := findPublishableCacheSession(ledgerPath, name)
		if err != nil {
			fail(name, err.Error(), time.Time{})
			continue
		}
		if !ok {
			continue // reached the Ledger since the listing
		}
		finished := session.ResolveStoppedAt(nil, filepath.Join(cacheDir, ledgerFileRaw), time.Now())
		notePending(name, finished)
		if !cliUploads {
			fail(name, "waiting for the daemon to upload it", finished)
		} else if err := publishCachedSession(projectRoot, ledgerPath, name, cacheDir); err != nil {
			fail(name, err.Error(), finished)
		}
	}

	// An upload whose push failed is committed here and reads as in the Ledger,
	// so nothing above retries it.
	ahead, err := gitutil.RunGit(ctx, ledgerPath, "rev-list", "--count", "@{upstream}..HEAD")
	if err != nil {
		return out, fmt.Errorf("check the Ledger for unpushed commits: %w", err)
	}
	if n, _ := strconv.Atoi(ahead); n > 0 {
		if err := pushLedger(ctx, ledgerPath); err != nil {
			return out, fmt.Errorf("push %d Ledger commits: %w", n, err)
		}
	}

	published, err := publishedSince(ctx, ledgerPath, since)
	if err != nil {
		return out, fmt.Errorf("count the Ledger's sessions: %w", err)
	}
	out.Since += published
	out.InLedger = published
	return out, nil
}

// publishedSince counts sessions on ledgerPath's remote that finished at or
// after since and whose commit since then carries this clone's Git author. ox
// commits a session from the machine that recorded it, so this also counts
// sessions whose local copy is already gone. The author names a person or an
// AI coworker rather than a machine, so the count includes that author's
// sessions from other machines that this clone has pulled.
func publishedSince(ctx context.Context, ledgerPath string, since time.Time) (int, error) {
	ident, err := gitutil.RunGit(ctx, ledgerPath, "var", "GIT_AUTHOR_IDENT")
	if err != nil {
		return 0, err
	}
	author, _, _ := strings.Cut(ident, ">") // "Name <email"; the timestamp follows
	// --since stops the walk at the first commit older than its date, so a day
	// of margin keeps a commit made on a skewed clock from hiding later ones;
	// the stop time applies the exact cutoff below.
	log, err := gitutil.RunGit(ctx, ledgerPath, "log", "@{upstream}", "--since="+since.Add(-24*time.Hour).Format(time.RFC3339),
		"--fixed-strings", "--author="+author+">", "--format=", "--name-only", "--", "sessions/")
	if err != nil {
		return 0, err
	}
	names := make(map[string]bool)
	for _, line := range strings.Split(log, "\n") {
		if parts := strings.Split(line, "/"); len(parts) >= 3 && parts[0] == "sessions" {
			names[parts[1]] = true
		}
	}
	count := 0
	for name := range names {
		// read from the remote-tracking branch, which a push updates
		data, err := gitutil.RunGit(ctx, ledgerPath, "show", "@{upstream}:sessions/"+name+"/meta.json")
		var meta lfs.SessionMeta
		if err != nil || json.Unmarshal([]byte(data), &meta) != nil || meta.IsDraft() {
			continue
		}
		finished := meta.CreatedAt // a meta.json with no stop recorded
		if meta.StoppedAt != nil {
			finished = *meta.StoppedAt
		}
		if !finished.Before(since) {
			count++
		}
	}
	return count, nil
}

func printSessionWait(w io.Writer, out sessionWaitOutput, since time.Time) {
	fmt.Fprintf(w, "%d of %d sessions finished since %s are in the Ledger.\n", out.InLedger, out.Since, since.Format(time.RFC3339))
	for _, name := range out.OlderPending {
		fmt.Fprintf(w, "Older pending session: %s\n", name)
	}
	for _, f := range out.Failed {
		fmt.Fprintf(w, "Not in the Ledger: %s: %s\n", f.Session, f.Error)
	}
}
