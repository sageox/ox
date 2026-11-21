package livesession_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/sessionid"
)

// copyState writes st as the stream state of a new folder, as a Ledger
// contributor force-adds one.
func copyState(t *testing.T, st livesession.State) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stream-state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Failure prevented (ox #2002 review): the daemon ran the person's model CLI
// over a forged closing state's spool before Close authorized it. The check it
// now makes first must refuse exactly what every signed request refuses, and
// pass the stream this machine minted for its folder.
func TestVerifyProvenance(t *testing.T) {
	srv := livesessiontest.New(t)
	cfg := srv.Config(t)
	dir := filepath.Join(t.TempDir(), "2026-03-04T13-41-person-a-Oxabc1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mine, err := livesession.Prepare(t.Context(), cfg, dir, livesession.OpenParams{SessionID: sessionid.GenerateSessionID()})
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := cfg
	elsewhere.DataDir = t.TempDir()
	theirs, err := livesession.Prepare(t.Context(), elsewhere, t.TempDir(), livesession.OpenParams{SessionID: sessionid.GenerateSessionID()})
	if err != nil {
		t.Fatal(err)
	}
	otherEndpoint := mine
	otherEndpoint.Endpoint = livesessiontest.New(t).URL

	tests := []struct {
		name      string
		dir       string
		st        livesession.State
		untrusted bool
	}{
		{"this machine's stream in its own folder", dir, mine, false},
		{"this machine's id copied into another folder", copyState(t, mine), mine, true},
		{"a conversation minted on another machine", copyState(t, theirs), theirs, true},
		{"a state naming another endpoint", copyState(t, otherEndpoint), otherEndpoint, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := livesession.VerifyProvenance(t.Context(), cfg, tt.dir, tt.st)
			if tt.untrusted != errors.Is(err, livesession.ErrUntrustedState) || (!tt.untrusted && err != nil) {
				t.Fatalf("VerifyProvenance = %v, want untrusted=%v", err, tt.untrusted)
			}
		})
	}
}

// Failure prevented: the daemon's config names only the endpoint and a logger
// it may leave unset; checked without the defaults every other entry point
// applies, a refusal had no logger to report to and the registry was looked
// up relative to the working directory.
func TestVerifyProvenanceAppliesTheConfigDefaults(t *testing.T) {
	srv := livesessiontest.New(t)
	cfg := srv.Config(t)
	cfg.Logger = nil
	cnv, err := livesession.ConversationID(sessionid.GenerateSessionID())
	if err != nil {
		t.Fatal(err)
	}
	forged := livesession.State{ConversationID: cnv, Endpoint: cfg.Endpoint, Status: livesession.StatusClosing}
	if err := livesession.VerifyProvenance(t.Context(), cfg, copyState(t, forged), forged); !errors.Is(err, livesession.ErrUntrustedState) {
		t.Fatalf("VerifyProvenance = %v, want ErrUntrustedState", err)
	}

	cfg.Endpoint = ""
	if err := livesession.VerifyProvenance(t.Context(), cfg, t.TempDir(), forged); err == nil || errors.Is(err, livesession.ErrUntrustedState) {
		t.Fatalf("VerifyProvenance with no endpoint = %v, want a config error", err)
	}
}
