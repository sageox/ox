package livesession_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/sessionid"
)

// openAcceptsNotes decodes each open body the server saw and reports, per open,
// whether accepts_notes was present and its value. Decoded, never substring
// matched: a nested or renamed key must not pass.
func openAcceptsNotes(t *testing.T, srv *livesessiontest.Server) (present []bool, values []bool) {
	t.Helper()
	for _, body := range srv.OpenBodies() {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatalf("open body is not a JSON object: %v", err)
		}
		raw, ok := fields["accepts_notes"]
		present = append(present, ok)
		var v bool
		if ok {
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatalf("accepts_notes = %s, want a bool", raw)
			}
		}
		values = append(values, v)
	}
	return present, values
}

func openWith(t *testing.T, srv *livesessiontest.Server, cfg livesession.Config) (livesession.State, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := livesession.Open(t.Context(), cfg, dir, livesession.OpenParams{
		SessionID: sessionid.GenerateSessionID(), SessionName: "2026-03-04T13-41-person-a-Oxabc1",
		RepoID: "repo_test", Agent: "claude-code", StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return st, dir
}

// Failure prevented: a user who turned teammate notes off still has the web
// offer a note box for their session, because the server was never told.
func TestOpenTellsTheServerWhenThisMachineRefusesNotes(t *testing.T) {
	tests := []struct {
		name        string
		accepts     func() bool
		wantPresent bool
	}{
		// absent means true on the server, so older servers see the same body
		{name: "no switch wired: field omitted", accepts: nil, wantPresent: false},
		{name: "switch on: field omitted", accepts: func() bool { return true }, wantPresent: false},
		{name: "switch off: accepts_notes false", accepts: func() bool { return false }, wantPresent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := livesessiontest.New(t)
			cfg := srv.Config(t)
			cfg.AcceptsNotes = tt.accepts
			openWith(t, srv, cfg)

			present, values := openAcceptsNotes(t, srv)
			if len(present) != 1 {
				t.Fatalf("opens = %d, want 1", len(present))
			}
			if present[0] != tt.wantPresent {
				t.Fatalf("accepts_notes present = %v, want %v", present[0], tt.wantPresent)
			}
			if tt.wantPresent && values[0] {
				t.Fatal("accepts_notes = true, want false")
			}
		})
	}
}

// Failure prevented: a stream that restarts on a fresh layer (the server asked
// for one) silently starts accepting notes again, since the server reads the
// switch from the newest layer.
func TestReplacementLayerOpenCarriesTheOptOutToo(t *testing.T) {
	srv := livesessiontest.New(t)
	cfg := srv.Config(t)
	cfg.AcceptsNotes = func() bool { return false }
	st, dir := openWith(t, srv, cfg)
	if _, err := livesession.Spool(t.Context(), dir, withPrompt(lines(0, 5)), livesession.SpoolOptions{}); err != nil {
		t.Fatal(err)
	}
	srv.ForceNewLayer(1)
	if _, err := livesession.Drain(t.Context(), cfg, dir, testBudget); err != nil {
		t.Fatalf("drain: %v", err)
	}
	after, err := livesession.ReadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if after.LayerID == st.LayerID {
		t.Fatal("setup: the drain did not move to a new layer")
	}
	present, values := openAcceptsNotes(t, srv)
	if len(present) != 2 {
		t.Fatalf("opens = %d, want the first open and the replacement layer's", len(present))
	}
	for i := range present {
		if !present[i] || values[i] {
			t.Fatalf("open %d: accepts_notes present=%v value=%v, want present and false", i, present[i], values[i])
		}
	}
}
