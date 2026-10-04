package main

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sageox/ox/internal/daemon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stopHarness is a scripted daemon for stopDaemonAndWait: it exits on its own
// a set time after the stop request, or never.
type stopHarness struct {
	mu           sync.Mutex
	stopErr      error         // what requestStop returns
	exitsAfter   time.Duration // time after requestStop at which the daemon exits; <0 never
	forceErr     error         // what forceStop returns
	forceWorks   bool          // whether forceStop actually makes the daemon exit
	stopAt       time.Time
	killed       bool
	forceCalls   int
	progressMsgs []string
	warnMsgs     []string
}

func (h *stopHarness) ops() daemonStopOps {
	return daemonStopOps{
		requestStop: func() error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.stopAt = time.Now()
			return h.stopErr
		},
		isRunning: func() bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.killed {
				return false
			}
			if h.exitsAfter >= 0 && !h.stopAt.IsZero() && time.Since(h.stopAt) >= h.exitsAfter {
				return false
			}
			return true
		},
		forceStop: func() error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.forceCalls++
			if h.forceErr != nil {
				return h.forceErr
			}
			h.killed = h.forceWorks
			return nil
		},
		progress: func(msg string) { h.mu.Lock(); h.progressMsgs = append(h.progressMsgs, msg); h.mu.Unlock() },
		warn:     func(msg string) { h.mu.Lock(); h.warnMsgs = append(h.warnMsgs, msg); h.mu.Unlock() },
	}
}

// TestStopDaemonAndWait pins the stop -> wait -> escalate decision.
//
// Failure prevented: `ox daemon restart` gave up after 2s with "daemon did not
// stop within 2s" while the daemon was still correctly draining (it has up to
// 35s of in-flight work to finish), and a second restart then died with
// "connection refused" because the draining daemon had already closed its
// listener. Both left the daemon running and nothing restarted.
func TestStopDaemonAndWait(t *testing.T) {
	const (
		budget        = 300 * time.Millisecond
		interval      = 5 * time.Millisecond
		progressAfter = 100 * time.Millisecond
	)
	refused := fmt.Errorf("connect to daemon: %w", syscall.ECONNREFUSED)

	tests := []struct {
		name         string
		harness      *stopHarness
		wantOutcome  daemonStopOutcome
		wantErr      string
		wantForce    int
		wantProgress int
		wantWarns    int
		maxElapsed   time.Duration // 0: no bound
	}{
		{
			name:        "daemon exits promptly: no progress line, no force",
			harness:     &stopHarness{exitsAfter: 20 * time.Millisecond},
			wantOutcome: daemonStopGraceful,
		},
		{
			name:         "daemon drains slowly: one progress line, still no force",
			harness:      &stopHarness{exitsAfter: 180 * time.Millisecond},
			wantOutcome:  daemonStopGraceful,
			wantProgress: 1,
		},
		{
			name:         "daemon never exits: force-stopped after the budget",
			harness:      &stopHarness{exitsAfter: -1, forceWorks: true},
			wantOutcome:  daemonStopForced,
			wantForce:    1,
			wantProgress: 1,
			wantWarns:    1,
		},
		{
			// the second-restart case: the listener is gone, the process is not
			name:        "stop request refused: force-stops without waiting out the budget",
			harness:     &stopHarness{stopErr: refused, exitsAfter: -1, forceWorks: true},
			wantOutcome: daemonStopForced,
			wantForce:   1,
			wantWarns:   1,
			maxElapsed:  budget / 2,
		},
		{
			name:        "force-stop fails: the error surfaces",
			harness:     &stopHarness{stopErr: refused, exitsAfter: -1, forceErr: errors.New("signal denied")},
			wantOutcome: daemonStopForced,
			wantErr:     "failed to force-stop daemon: signal denied",
			wantForce:   1,
			wantWarns:   1,
		},
		{
			name:        "force-stop reports success but the daemon survives: not a silent success",
			harness:     &stopHarness{stopErr: refused, exitsAfter: -1, forceWorks: false},
			wantOutcome: daemonStopForced,
			wantErr:     "still running after force-stop",
			wantForce:   1,
			wantWarns:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := tt.harness
			start := time.Now()
			outcome, err := stopDaemonAndWait(h.ops(), budget, interval, progressAfter)
			elapsed := time.Since(start)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantOutcome, outcome, "outcome")
			assert.Equal(t, tt.wantForce, h.forceCalls, "force-stop calls")
			assert.Equal(t, tt.wantProgress, len(h.progressMsgs), "progress lines")
			assert.Equal(t, tt.wantWarns, len(h.warnMsgs), "warnings")
			if tt.maxElapsed > 0 {
				assert.Less(t, elapsed, tt.maxElapsed, "must decide immediately, not after the wait budget")
			}
		})
	}
}

// TestDaemonStopWaitBudget ties the CLI's patience to the daemon's actual
// shutdown budget, so the two cannot drift apart again.
func TestDaemonStopWaitBudget(t *testing.T) {
	assert.Greater(t, daemonStopWaitBudget, daemon.MaxGracefulShutdown,
		"the CLI must outwait a healthy daemon's worst-case drain, or it force-stops a daemon that is behaving")
	assert.GreaterOrEqual(t, daemonStopWaitBudget, 40*time.Second)
	assert.Less(t, daemonStopProgressAfter, daemonStopWaitBudget)
	assert.Greater(t, daemonStopRequestTimeout, 50*time.Millisecond,
		"the default 50ms IPC timeout reads a busy daemon as dead and force-stops it needlessly")
}
