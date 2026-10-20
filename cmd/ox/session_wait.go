package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/errkind"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/spf13/cobra"
)

var sessionWaitCmd = &cobra.Command{
	Use:   "wait --since <time>",
	Short: "Wait until this machine's sessions reach the Ledger, and report what did",
	Long: `Make sure every session recorded on this machine reached the Ledger, for a
machine that goes away after its job, such as a CI-like pod. Run it from the
project after the AI coworkers on the machine have exited.

Where the ox daemon outlives the session, the daemon summarizes and uploads
sessions, as on a laptop. wait starts the daemon if it is not running, hands
it every session whose recording has ended, as session end does, and waits up
to --timeout until the daemon has put this machine's sessions in the Ledger. A
recording whose AI coworker crashed is the daemon's to finish; it notices the
crash within minutes. wait uploads nothing itself.

Where the daemon does not outlive the session (the daemon is off, or the
machine goes away with its AI coworker), session end uploads before returning,
and wait only reports.

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
	sessionWaitCmd.Flags().Duration("timeout", 20*time.Minute, "how long to wait for the daemon to upload this machine's sessions")
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
	timeout, _ := cmd.Flags().GetDuration("timeout")
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

	out, err := waitForSessions(cmd.Context(), projectRoot, ledgerPath, since, timeout)
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

// ensureDaemonForWait starts the daemon if it is not running, so sessions left
// on disk reach one even after a crash took it down. A variable so tests never
// start a daemon process.
var ensureDaemonForWait = daemon.EnsureDaemon

// waitForSessions waits, where the daemon uploads sessions, until it has put
// this machine's sessions in the Ledger, then reports. It uploads nothing
// itself, so it never works on a session beside the daemon.
func waitForSessions(ctx context.Context, projectRoot, ledgerPath string, since time.Time, timeout time.Duration) (sessionWaitOutput, error) {
	out := sessionWaitOutput{OlderPending: []string{}, Failed: []sessionWaitFailure{}}
	daemonUploads := daemonOutlivesSession()
	outside, err := sessionsOutsideLedger(ctx, projectRoot, ledgerPath, daemonUploads)
	if err != nil {
		return out, err
	}
	for _, s := range outside {
		if !s.finished.IsZero() && s.finished.Before(since) {
			out.OlderPending = append(out.OlderPending, s.name)
		}
	}

	pending := func(s outsideSession) bool { return s.pending }
	if slices.ContainsFunc(outside, pending) {
		if err := ensureDaemonForWait(); err != nil {
			return out, fmt.Errorf("start the daemon: %w", err)
		}
		for _, s := range outside {
			if s.cacheDir != "" {
				_ = sendHookFinalizeIPC(daemon.SessionFinalizeIPCPayload{
					SessionName: s.name, LedgerPath: ledgerPath, CachePath: s.cacheDir, ProjectRoot: projectRoot,
				})
			}
		}
		poll := timeout / 10
		if poll > 2*time.Second {
			poll = 2 * time.Second
		}
		for deadline := time.Now().Add(timeout); time.Now().Before(deadline) && slices.ContainsFunc(outside, pending); {
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(poll):
			}
			if outside, err = sessionsOutsideLedger(ctx, projectRoot, ledgerPath, daemonUploads); err != nil {
				return out, err
			}
		}
	}

	for _, s := range outside {
		out.Failed = append(out.Failed, sessionWaitFailure{Session: s.name, Error: s.reason})
		if !s.finished.IsZero() && !s.finished.Before(since) {
			out.Since++
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

// outsideSession is a session of this machine's that is not on the Ledger's
// remote.
type outsideSession struct {
	name, reason string
	finished     time.Time // zero when unknown
	pending      bool      // the daemon will still upload it
	cacheDir     string    // set when its recording has ended, so the daemon can be handed it
}

// sessionsOutsideLedger lists this machine's sessions that are not on the
// Ledger's remote, and why. pending is set only where daemonUploads.
func sessionsOutsideLedger(ctx context.Context, projectRoot, ledgerPath string, daemonUploads bool) ([]outsideSession, error) {
	var out []outsideSession
	add := func(s outsideSession) {
		if !slices.ContainsFunc(out, func(o outsideSession) bool { return o.name == s.name }) {
			out = append(out, s)
		}
	}

	recordings, err := session.LoadAllRecordingStates(projectRoot)
	if err != nil {
		return nil, err
	}
	for _, rec := range recordings {
		name := session.GetSessionName(rec.SessionPath)
		switch {
		case rec.SourceRejected:
			add(outsideSession{name: name, reason: "held for ownership review"})
		case rec.IsAgentAlive():
			add(outsideSession{name: name, reason: "still recording"})
		default:
			gone := outsideSession{
				name:     name,
				reason:   "its AI coworker exited without ending the session; recover it with ox agent " + rec.AgentID + " session recover",
				finished: session.ResolveStoppedAt(rec.StoppedAt, filepath.Join(rec.SessionPath, ledgerFileRaw), time.Now()),
			}
			if daemonUploads {
				// the daemon's dead-agent check drains its last entries first
				gone.reason, gone.pending = "its AI coworker exited without ending the session; waiting for the daemon to finish it", true
			}
			add(gone)
		}
	}

	unpublished := listUnpublishedCacheSessions(ledgerPath)
	for _, name := range unpublished.Held {
		add(outsideSession{name: name, reason: "held on this machine; publish it with ox session upload " + name})
	}
	for _, name := range unpublished.Waiting {
		_, cacheDir, ok, err := findPublishableCacheSession(ledgerPath, name)
		if err != nil {
			add(outsideSession{name: name, reason: err.Error()})
			continue
		}
		if !ok {
			continue // reached the Ledger since the listing
		}
		waiting := outsideSession{
			name:     name,
			reason:   "not uploaded; publish it with ox session upload " + name,
			finished: session.ResolveStoppedAt(nil, filepath.Join(cacheDir, ledgerFileRaw), time.Now()),
		}
		if daemonUploads {
			waiting.reason, waiting.pending, waiting.cacheDir = "waiting for the daemon to upload it", true, cacheDir
		}
		add(waiting)
	}

	// committed but not pushed reads as published in the clone
	log, err := gitutil.RunGit(ctx, ledgerPath, "log", "@{upstream}..HEAD", "--format=", "--name-only", "--", "sessions/")
	if err != nil {
		return nil, fmt.Errorf("check the Ledger for unpushed commits: %w", err)
	}
	for _, name := range sessionNamesIn(log) {
		add(outsideSession{name: name, reason: "committed on this machine but not pushed", pending: daemonUploads})
	}
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
	count := 0
	for _, name := range sessionNamesIn(log) {
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

// sessionNamesIn returns the sorted session names in `git log --name-only`
// output. RunGit mixes stderr into it; a warning line names no session.
func sessionNamesIn(log string) []string {
	var names []string
	for _, line := range strings.Split(log, "\n") {
		if parts := strings.Split(line, "/"); len(parts) >= 3 && parts[0] == "sessions" && !slices.Contains(names, parts[1]) {
			names = append(names, parts[1])
		}
	}
	slices.Sort(names)
	return names
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
