package agentwork

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestIsStaleRecording_StartedAtWinsOverFreshMtime is the regression test for
// ox-zj3y: a `.recording.json` whose JSON `started_at` says the recording is
// >24h old MUST be flagged stale even when the file's mtime was recently
// touched (filesystem snapshot, antivirus scan, accidental `ls -lct`,
// macOS Spotlight reindex). Before this fix, daemon staleness was driven by
// mtime, so any of those benign touches could revive an abandoned recording
// and block daemon finalize indefinitely.
//
// Failure prevented: stale recordings appearing alive forever because some
// background process bumped the marker's mtime.
func TestIsStaleRecording_StartedAtWinsOverFreshMtime(t *testing.T) {
	dir := t.TempDir()
	recPath := filepath.Join(dir, ".recording.json")

	// JSON says the recording started 48h ago — well past the 24h threshold.
	startedAt := time.Now().Add(-48 * time.Hour).UTC()
	body, err := json.Marshal(struct {
		StartedAt time.Time `json:"started_at"`
		AgentID   string    `json:"agent_id"`
		ParentPID int       `json:"parent_pid"`
	}{
		StartedAt: startedAt,
		// no PID — forces age path (PID liveness short-circuits otherwise)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	// Recently bump mtime to simulate the filesystem-touch failure mode that
	// made this bug originally observable. If staleness used mtime alone,
	// this would mark the recording as fresh.
	now := time.Now()
	if err := os.Chtimes(recPath, now, now); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(recPath)
	if err != nil {
		t.Fatal(err)
	}

	stale, age, method := isStaleRecording(recPath, info, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !stale {
		t.Errorf("expected stale=true (StartedAt=48h ago must win over fresh mtime); got stale=false age=%v method=%s",
			age, method)
	}
	// The recovered age MUST come from StartedAt, not from mtime — otherwise
	// the staleness decision was made for the wrong reason and the bug is
	// only accidentally hidden.
	if age < 47*time.Hour {
		t.Errorf("expected age derived from started_at (~48h); got %v — the bug is back if age tracks mtime", age)
	}
}

// TestIsStaleRecording_FreshStartedAtBeatsOldMtime is the inverse regression:
// a recording with a recent `started_at` must NOT be flagged stale just
// because the marker file itself happens to be old on disk (e.g., a session
// that resumed after a long idle period and rewrote the JSON in place
// without updating mtime, or a clock-skew edge case).
//
// Failure prevented: live sessions getting prematurely finalized because
// staleness was driven by mtime instead of the authoritative StartedAt.
func TestIsStaleRecording_FreshStartedAtBeatsOldMtime(t *testing.T) {
	dir := t.TempDir()
	recPath := filepath.Join(dir, ".recording.json")

	startedAt := time.Now().Add(-1 * time.Hour).UTC()
	body, err := json.Marshal(struct {
		StartedAt time.Time `json:"started_at"`
		AgentID   string    `json:"agent_id"`
		ParentPID int       `json:"parent_pid"`
	}{StartedAt: startedAt})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	// Mtime says 48h ago — older than threshold. Must be ignored in favor
	// of started_at.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(recPath, old, old); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(recPath)
	if err != nil {
		t.Fatal(err)
	}

	stale, age, _ := isStaleRecording(recPath, info, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if stale {
		t.Errorf("expected stale=false (StartedAt=1h ago must beat old mtime); got stale=true age=%v", age)
	}
}

// TestIsStaleRecording_FallsBackToMtimeWhenJSONUnreadable documents the
// intentional fallback: when `.recording.json` cannot be read or parsed, the
// daemon has no choice but to use mtime. This is the *only* path on which
// mtime is authoritative, and it must keep working — without it, a
// corrupted marker would block all finalize attempts forever.
//
// Failure prevented: future "always trust StartedAt" refactor accidentally
// removing the only viable signal for unparseable markers.
func TestIsStaleRecording_FallsBackToMtimeWhenJSONUnreadable(t *testing.T) {
	dir := t.TempDir()
	recPath := filepath.Join(dir, ".recording.json")

	// Garbage JSON forces the parse-fail branch.
	if err := os.WriteFile(recPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(recPath, old, old); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(recPath)
	if err != nil {
		t.Fatal(err)
	}

	stale, _, method := isStaleRecording(recPath, info, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !stale {
		t.Error("unparseable marker with old mtime must fall back to mtime-based staleness")
	}
	if method != "time_threshold" {
		t.Errorf("expected method=time_threshold for mtime fallback, got %q", method)
	}
}
