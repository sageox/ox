package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/gitutil"
)

// Clone budgets. Vars (not consts) so tests can shrink them.
var (
	// teamContextCloneTimeout bounds the team-context two-phase clone. It is a
	// partial clone whose blobs arrive on demand, so a fixed budget fits it.
	teamContextCloneTimeout = 60 * time.Second

	// ledgerCloneTimeout is only a backstop for a Ledger clone that hangs
	// somewhere curl's stall check cannot see. A Ledger clone downloads the
	// whole history, hundreds of MB for an active team, so how long it takes
	// is the coworker's link speed, not something ox can budget. The old
	// shared 60s budget killed every attempt that outlasted it (a 700 MB
	// Ledger needs ~12 MB/s to fit), and each retry started over in a fresh
	// temp dir, so the Ledger never arrived. A transfer that stops making
	// progress is still aborted, within ~20s, by gitHTTPTimeoutFlags
	// (http.lowSpeedLimit / http.lowSpeedTime).
	ledgerCloneTimeout = time.Hour

	// cloneProgressInterval is how often a running Ledger clone's size is
	// sampled; cloneProgressLogEvery is how often that size is logged.
	cloneProgressInterval = 2 * time.Second
	cloneProgressLogEvery = 30 * time.Second
)

// errCloneInterrupted marks a clone the daemon's own shutdown stopped. It is
// not a clone failure: the retry loop must not back off or report it.
var errCloneInterrupted = errors.New("daemon is shutting down")

// cloneContext gives one clone attempt its own budget. It deliberately does
// not derive from whoever asked for the clone: an IPC caller hanging up must
// not kill a daemon-owned clone. It does end with the daemon, though. git runs
// in its own process group, so a clone that outlived the daemon would keep
// downloading, possibly for many minutes, into a temp dir nothing would ever
// remove.
func (s *SyncScheduler) cloneContext(budget time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	stop := context.AfterFunc(s.backgroundCloneContext(), cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// watchCloneProgress samples how large the clone at dir has grown while git
// runs, and logs it every cloneProgressLogEvery so a long first clone shows
// progress in the daemon log. git deletes a failed clone's directory as it
// exits, so the size has to be sampled while git runs: reading it after a kill
// finds nothing. stop ends sampling and returns the largest size seen.
func (s *SyncScheduler) watchCloneProgress(dir string, start time.Time) (stop func() int64) {
	var largest int64
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		tick := time.NewTicker(cloneProgressInterval)
		defer tick.Stop()
		lastLog := start
		for {
			select {
			case <-done:
				return
			case now := <-tick.C:
				largest = max(largest, dirSize(dir))
				if now.Sub(lastLog) >= cloneProgressLogEvery {
					lastLog = now
					s.logger.Info("checkout: clone in progress",
						"path", dir, "elapsed", now.Sub(start).Round(time.Second), "received_bytes", largest)
				}
			}
		}
	}()
	return func() int64 {
		close(done)
		<-finished
		return largest
	}
}

// ledgerCloneError turns a failed Ledger clone into the error that the retry
// loop and `ox status` report. When our own deadline or the daemon's shutdown
// killed git, its output ("fetch-pack: unexpected disconnect while reading
// sideband packet", "early EOF") describes the kill, not the server, so it is
// replaced with what actually happened.
//
// No returned error wraps a context error: IsPreCloneLockBusy reads
// context.DeadlineExceeded and context.Canceled as benign lock contention,
// which would hide a timed-out clone from backoff and from `ox status`.
func (s *SyncScheduler) ledgerCloneError(cloneCtx context.Context, repoType string, budget, elapsed time.Duration, received int64, output []byte, err error) error {
	sanitizedOutput := gitutil.SanitizeOutput(string(output))
	switch {
	case errors.Is(cloneCtx.Err(), context.DeadlineExceeded):
		msg := fmt.Sprintf("clone %s timed out after %s (%s received)", repoType, budget, formatCloneBytes(received))
		s.logger.Error("checkout: clone timed out",
			"type", repoType, "budget", budget, "elapsed", elapsed.Round(time.Millisecond), "received_bytes", received)
		s.logger.Debug("checkout: git output from the timed-out clone", "error", err, "output", sanitizedOutput)
		s.recordError(repoType, msg)
		return errors.New(msg)
	case errors.Is(cloneCtx.Err(), context.Canceled):
		s.logger.Info("checkout: clone stopped, daemon is shutting down",
			"type", repoType, "elapsed", elapsed.Round(time.Millisecond), "received_bytes", received)
		return fmt.Errorf("clone %s interrupted: %w", repoType, errCloneInterrupted)
	}
	s.logger.Error("checkout: clone failed", "error", err, "output", sanitizedOutput,
		"elapsed", elapsed.Round(time.Millisecond), "received_bytes", received)
	s.recordError(repoType, fmt.Sprintf("clone %s failed: %v", repoType, err))
	if sanitizedOutput != "" {
		return fmt.Errorf("git clone failed: %s", sanitizedOutput)
	}
	return fmt.Errorf("git clone failed: %w", err)
}

// formatCloneBytes renders a clone size for a status line, e.g. "412.3 MB".
func formatCloneBytes(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/1e6)
}

// dirSize is the total size of the regular files under dir: 0 when dir is
// missing, and whatever remains countable when parts vanish mid-walk.
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
