package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// serveCheckoutIPC serves checkout over a real IPC socket and returns a
// client that gives up after idle without word from the daemon, the way
// `ox doctor --fix` does after 60s.
func serveCheckoutIPC(t *testing.T, checkout func(CheckoutPayload, *ProgressWriter) (*CheckoutResult, error), idle time.Duration) *Client {
	t.Helper()
	runtimeDir, err := os.MkdirTemp("/tmp", "ox-ckipc-") // short: macOS caps socket paths at 104 bytes
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	t.Setenv("OX_XDG_ENABLE", "1")
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	server := NewServer(slog.New(slog.DiscardHandler))
	server.SetHandlers(func() error { return nil }, func() {}, func() *StatusData { return &StatusData{Running: true} })
	server.SetCheckoutHandler(checkout)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	client := &Client{socketPath: SocketPath(), timeout: idle}
	require.Eventually(t, func() bool { return client.Ping() == nil }, 2*time.Second, 10*time.Millisecond, "IPC server never answered ping")
	return client
}

// TestLedgerClone_IPCCheckoutWaitsWhileDownloadProgresses verifies a CLI
// waiting on the daemon's Ledger clone over IPC keeps waiting for as long as
// the download is moving, and sees how much has arrived.
// Failure prevented: Client.Checkout gave the whole clone a fixed 60s, so
// `ox doctor --fix` printed a read timeout and failed its check while the
// daemon went on to finish the clone. The CLI's window is scaled from 60s to
// 1.5s here; the fixture downloads for ~5s.
func TestLedgerClone_IPCCheckoutWaitsWhileDownloadProgresses(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	setCloneBudgets(t, 60*time.Second, 2*time.Minute)
	srv := servePacedLedger(t, ledgerFixtureBytes, ledgerPace, 0)
	s, ledgerPath := newCloneLifecycleScheduler(t, srv.URL)
	const idle = 1500 * time.Millisecond
	client := serveCheckoutIPC(t, s.Checkout, idle)

	var mu sync.Mutex
	var downloading []string
	start := time.Now()
	result, err := client.Checkout(CheckoutPayload{CloneURL: srv.URL, RepoPath: ledgerPath, RepoType: "ledger"},
		func(stage string, _ *int, message string) {
			if stage == "downloading" {
				mu.Lock()
				downloading = append(downloading, message)
				mu.Unlock()
			}
		})
	elapsed := time.Since(start)

	require.NoError(t, err, "the CLI must keep waiting while the daemon's clone is still downloading")
	require.True(t, result.Cloned)
	require.Greater(t, elapsed, 2*idle, "the clone finished inside the CLI's window, so this run proved nothing; slow the pace")
	require.DirExists(t, ledgerPath)
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, downloading, "the CLI must be told the clone is still downloading")
	last := downloading[len(downloading)-1]
	require.Contains(t, last, "MB received", "progress must say how much of the Ledger has arrived")
	require.False(t, strings.HasPrefix(last, "Downloading: 0.0 MB"), "the last report must reflect bytes that arrived: %q", last)
}

// TestLedgerClone_IPCCheckoutReportsInProgressWhenDaemonGoesQuiet verifies a
// CLI whose idle window passes with no word from the daemon gets
// ErrCheckoutInProgress, not a bare read error.
// Failure prevented: doctor printed "Clone failed: read: i/o timeout" and
// failed its check for a clone the daemon was still running.
func TestLedgerClone_IPCCheckoutReportsInProgressWhenDaemonGoesQuiet(t *testing.T) {
	release := make(chan struct{})
	client := serveCheckoutIPC(t, func(p CheckoutPayload, _ *ProgressWriter) (*CheckoutResult, error) {
		<-release // busy elsewhere, e.g. waiting out its own clone of this Ledger
		return &CheckoutResult{Path: p.RepoPath, Cloned: true}, nil
	}, 300*time.Millisecond)
	t.Cleanup(func() { close(release) }) // runs before the server stops

	_, err := client.Checkout(CheckoutPayload{CloneURL: "http://127.0.0.1:1/ledger.git", RepoPath: t.TempDir() + "/ledger", RepoType: "ledger"}, nil)
	require.ErrorIs(t, err, ErrCheckoutInProgress)
}

// TestLedgerClone_IPCCheckoutIsPendingWorkWhileItDownloads verifies a Ledger
// clone a CLI asked for over IPC holds off the daemon's inactivity exit.
// Failure prevented: IPC traffic counts as activity only when a message
// arrives, so a long clone `ox doctor --fix` started could be canceled by the
// idle exit mid-download, and the next start downloaded it again from zero.
func TestLedgerClone_IPCCheckoutIsPendingWorkWhileItDownloads(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	setCloneBudgets(t, 60*time.Second, 2*time.Minute)
	srv := servePacedLedger(t, ledgerFixtureBytes, ledgerPace, 0)
	s, ledgerPath := newCloneLifecycleScheduler(t, srv.URL)
	d := New(&Config{InactivityTimeout: time.Hour, PendingWorkGracePeriod: time.Minute}, slog.New(slog.DiscardHandler))
	d.scheduler = s
	client := serveCheckoutIPC(t, s.Checkout, 5*time.Second)
	require.False(t, d.hasPendingWork(), "nothing is cloning yet")

	done := make(chan error, 1)
	go func() {
		_, err := client.Checkout(CheckoutPayload{CloneURL: srv.URL, RepoPath: ledgerPath, RepoType: "ledger"}, nil)
		done <- err
	}()
	<-srv.streaming // the Ledger is downloading
	require.True(t, d.hasPendingWork(), "a clone still downloading is pending work")

	require.NoError(t, <-done)
	require.False(t, d.hasPendingWork(), "a finished clone is not")
}

// TestLedgerClone_IPCCallerHangingUpDoesNotStopClone verifies a Ledger clone
// keeps downloading, and lands, after the CLI that asked for it hangs up.
// Failure prevented: doctor tells the coworker that a clone it stopped
// waiting on will finish in the background. A clone that died with the IPC
// connection (or with the progress reports it can no longer deliver) would
// make that a lie, and the Ledger would never arrive.
func TestLedgerClone_IPCCallerHangingUpDoesNotStopClone(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real paced Git clone")
	}
	setCloneBudgets(t, 60*time.Second, 2*time.Minute)
	srv := servePacedLedger(t, ledgerFixtureBytes, ledgerPace, 0)
	s, ledgerPath := newCloneLifecycleScheduler(t, srv.URL)
	client := serveCheckoutIPC(t, s.Checkout, 5*time.Second)

	// ask for the clone, then hang up once it is downloading, the way doctor
	// does when its window runs out or the coworker presses Ctrl-C
	conn, err := client.Connect()
	require.NoError(t, err)
	payload, err := json.Marshal(CheckoutPayload{CloneURL: srv.URL, RepoPath: ledgerPath, RepoType: "ledger"})
	require.NoError(t, err)
	msg, err := json.Marshal(Message{Type: MsgTypeCheckout, Payload: payload})
	require.NoError(t, err)
	_, err = conn.Write(append(msg, '\n'))
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		require.NoError(t, err, "the clone never reported downloading")
		var resp ProgressResponse
		require.NoError(t, json.Unmarshal(line, &resp))
		require.NotNil(t, resp.Progress, "the clone finished before the CLI could hang up; slow the pace")
		if resp.Progress.Stage == CheckoutStageDownloading {
			break
		}
	}
	require.NoError(t, conn.Close())

	require.Eventually(t, func() bool {
		return exec.Command("git", "-C", ledgerPath, "rev-parse", "--verify", "-q", "HEAD").Run() == nil
	}, 30*time.Second, 100*time.Millisecond, "the Ledger must land after the CLI that asked for it hangs up")
	size, err := exec.Command("git", "-C", ledgerPath, "cat-file", "-s", "HEAD:sessions/history.bin").Output()
	require.NoError(t, err, "the Ledger's history must have arrived in full")
	require.Equal(t, fmt.Sprint(ledgerFixtureBytes), strings.TrimSpace(string(size)))
}
