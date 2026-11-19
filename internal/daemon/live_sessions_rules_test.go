package daemon

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The secret rules refresh runs beside the sweep (F2). Failure prevented: a
// rules fetch that never answers held every drain behind it, so a network that
// blocks raw.githubusercontent.com streamed nothing at all.
func TestLiveRulesRefreshThatHangsNeverHoldsADrain(t *testing.T) {
	e := newLiveEnv(t)
	refreshing := make(chan struct{})
	e.live.refreshRules = func(ctx context.Context) error {
		close(refreshing)
		<-ctx.Done() // hangs until shutdown
		return ctx.Err()
	}
	e.spool(`{"type":"msg","n":1}` + "\n")

	ctx, cancel := context.WithCancel(t.Context())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		e.live.Run(ctx)
	}()
	defer func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop after its context ended")
		}
	}()
	<-refreshing

	deadline := time.Now().Add(3 * time.Second)
	for e.stored() != `{"type":"msg","n":1}`+"\n" {
		if time.Now().After(deadline) {
			t.Fatal("the spool was not drained while the rules refresh hung")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A failed refresh is logged once per failure streak, not every interval, and
// the recovery once: an outage of hours must not page every 15 minutes.
func TestLiveRulesRefreshFailureIsLoggedOncePerStreak(t *testing.T) {
	e := newLiveEnv(t)
	logs := e.captureLogs()
	outcomes := []error{errors.New("catalog unavailable"), errors.New("catalog unavailable"), nil, nil, errors.New("catalog unavailable")}
	e.live.refreshRules = func(context.Context) error {
		err := outcomes[0]
		outcomes = outcomes[1:]
		return err
	}
	for range 5 {
		e.live.refreshRulesOnce(t.Context())
	}

	if n := len(logs.records(t, "live_secret_rules_refresh_failed")); n != 2 {
		t.Fatalf("refresh failures logged %d times over two streaks, want 2", n)
	}
	if n := len(logs.records(t, "live_secret_rules_refresh_recovered")); n != 1 {
		t.Fatalf("recovery logged %d times, want 1", n)
	}
	for _, rec := range logs.records(t, "live_secret_rules_refresh_failed") {
		if rec["level"] != "WARN" {
			t.Fatalf("refresh failure logged at %v, want WARN", rec["level"])
		}
	}
}
