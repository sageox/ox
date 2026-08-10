package agentwork

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// opencode and goose do not tail a file — they read from a SQLite database, so
// their find-session returns an opaque "<adapter>:<id>" handle. StartWatch
// required an absolute path and rejected both, which meant recording never
// started for either agent no matter how correct their readers were. The
// rejection was a returned error the caller logged and dropped, so the only
// symptom was a ledger that stayed empty.

func newWatcherManager(t *testing.T) *SessionWatcherManager {
	t.Helper()
	return NewSessionWatcherManager(slog.New(slog.DiscardHandler))
}

func TestStartWatch_AcceptsOpaqueHandles(t *testing.T) {
	cases := []struct {
		adapter     string
		sessionFile string
	}{
		{"opencode", "opencode:ses_096fdea05ffeagb7OGYpJCte6a"},
		{"goose", "goose:20260602_3"},
	}

	for _, tc := range cases {
		t.Run(tc.adapter, func(t *testing.T) {
			m := newWatcherManager(t)
			defer m.StopAll()

			dir := t.TempDir()
			err := m.StartWatch("sess-"+tc.adapter, tc.sessionFile, tc.adapter,
				filepath.Join(dir, "ledger"), filepath.Join(dir, "cache"))

			// resolveAdapter may fail in a test environment with no adapter
			// binary on PATH; that is a different failure and acceptable here.
			// What must NOT happen is rejection for the shape of the handle.
			if err != nil && strings.Contains(err.Error(), "must be an absolute path") {
				t.Errorf("%s: watcher rejected its own session handle %q — recording can never start for this agent\n  error: %v",
					tc.adapter, tc.sessionFile, err)
			}
		})
	}
}

func TestStartWatch_StillRejectsUnsafeSessionFiles(t *testing.T) {
	cases := []struct {
		name        string
		adapter     string
		sessionFile string
	}{
		{"relative path", "codex", ".codex/sessions/x.jsonl"},
		{"bare filename", "codex", "x.jsonl"},
		{"handle for an adapter that does not use handles", "codex", "codex:../../.ssh/id_rsa"},
		{"handle with traversal", "opencode", "opencode:../../.ssh/id_rsa"},
		{"handle naming another adapter", "opencode", "goose:20260602_3"},
		{"empty", "opencode", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newWatcherManager(t)
			defer m.StopAll()

			dir := t.TempDir()
			err := m.StartWatch("sess", tc.sessionFile, tc.adapter,
				filepath.Join(dir, "ledger"), filepath.Join(dir, "cache"))
			if err == nil {
				t.Errorf("watcher accepted %q for adapter %s — this is the path an IPC peer would use to aim the tail loop at a file it should not read",
					tc.sessionFile, tc.adapter)
			}
		})
	}
}
