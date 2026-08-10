package agentwork

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/session/adapters"
)

// startWatchAt is the single chokepoint both entry points funnel through — IPC
// via StartWatch and doctor via DetectAndRestart — so it is where the
// session-file rules have to hold.
//
// These tests assert on SENTINEL errors, not on message text. An earlier
// version matched substrings and was blind to the very regression it existed to
// catch: reordering IsSessionFileAllowed so the absolute-path check ran before
// the opaque-handle check would kill opencode and goose recording again while
// producing a differently-worded error, and the test stayed green.
//
// They also assert the SPECIFIC rejection reason. Asserting only "err != nil"
// is worse than useless here, because resolveAdapter fails for every input on a
// machine with no adapter binaries on PATH — so the whole enforcement block
// could be deleted and a bare non-nil check would never notice.

func newWatcherManager(t *testing.T, home string) *SessionWatcherManager {
	t.Helper()
	m := NewSessionWatcherManager(slog.New(slog.DiscardHandler))
	m.SetHomeDirForTest(home)
	t.Cleanup(m.StopAll)
	return m
}

// piSessionsRoot builds a home containing pi's real session-root layout.
func piSessionsRoot(t *testing.T) (home, sessions string) {
	t.Helper()
	home = t.TempDir()
	sessions = filepath.Join(home, ".pi", "agent", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, sessions
}

func startWatch(t *testing.T, m *SessionWatcherManager, sessionFile, adapter string) error {
	t.Helper()
	dir := t.TempDir()
	return m.StartWatch("sess", sessionFile, adapter,
		filepath.Join(dir, "ledger"), filepath.Join(dir, "cache"))
}

// TestStartWatch_AcceptsOpaqueHandles: opencode and goose read from a SQLite
// database, so find-session returns an id rather than a path. Requiring a path
// rejected both outright and recording never started for either.
func TestStartWatch_AcceptsOpaqueHandles(t *testing.T) {
	for _, tc := range []struct{ adapter, sessionFile string }{
		{"opencode", "opencode:ses_096fdea05ffeagb7OGYpJCte6a"},
		{"goose", "goose:20260602_3"},
	} {
		t.Run(tc.adapter, func(t *testing.T) {
			home, _ := piSessionsRoot(t)
			err := startWatch(t, newWatcherManager(t, home), tc.sessionFile, tc.adapter)

			// resolveAdapter may fail when no adapter binary is on PATH; that
			// is a different failure and acceptable. What must not happen is a
			// rejection for the SHAPE or the LOCATION of the handle.
			if errors.Is(err, ErrSessionFileShape) {
				t.Errorf("%s: its own session handle %q was rejected as malformed — recording can never start for this agent: %v",
					tc.adapter, tc.sessionFile, err)
			}
			if errors.Is(err, adapters.ErrSessionFileNotAllowed) || errors.Is(err, adapters.ErrSessionFileEscapes) {
				t.Errorf("%s: its own session handle %q was rejected by the allow-list: %v",
					tc.adapter, tc.sessionFile, err)
			}
		})
	}
}

func TestStartWatch_RejectsMalformedSessionFiles(t *testing.T) {
	for _, tc := range []struct{ name, adapter, sessionFile string }{
		{"relative path", "codex", ".codex/sessions/x.jsonl"},
		{"bare filename", "codex", "x.jsonl"},
		{"handle for an adapter that does not use handles", "codex", "codex:../../.ssh/id_rsa"},
		{"handle with traversal", "opencode", "opencode:../../.ssh/id_rsa"},
		{"handle naming another adapter", "opencode", "goose:20260602_3"},
		{"empty", "opencode", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := piSessionsRoot(t)
			err := startWatch(t, newWatcherManager(t, home), tc.sessionFile, tc.adapter)
			if !errors.Is(err, ErrSessionFileShape) {
				t.Errorf("%q for adapter %s was not rejected as malformed (got %v) — this is how an IPC peer aims the tail loop somewhere it should not go",
					tc.sessionFile, tc.adapter, err)
			}
		})
	}
}

func TestStartWatch_RejectsPathsOutsideTheAdapterRoots(t *testing.T) {
	home, _ := piSessionsRoot(t)

	for _, tc := range []struct{ name, adapter, sessionFile string }{
		{"ssh key", "pi", filepath.Join(home, ".ssh", "id_rsa")},
		{"ox auth token", "pi", filepath.Join(home, ".sageox", "auth.json")},
		{"another adapter's root", "pi", filepath.Join(home, ".codex", "sessions", "x.jsonl")},
		{"unknown adapter", "totally-made-up", filepath.Join(home, ".pi", "agent", "sessions", "x.jsonl")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := startWatch(t, newWatcherManager(t, home), tc.sessionFile, tc.adapter)
			if !errors.Is(err, adapters.ErrSessionFileNotAllowed) {
				t.Errorf("%q for adapter %s was not rejected by the allow-list (got %v)",
					tc.sessionFile, tc.adapter, err)
			}
		})
	}
}

// TestStartWatch_RejectsASymlinkPlantedInsideAnAllowedRoot proves the symlink
// defense is actually WIRED into the daemon, not merely present in the adapters
// package. Unit coverage of SafeSessionFilePath passes whether or not
// startWatchAt calls it.
func TestStartWatch_RejectsASymlinkPlantedInsideAnAllowedRoot(t *testing.T) {
	home, sessions := piSessionsRoot(t)

	secret := filepath.Join(home, ".ssh", "id_rsa")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}

	planted := filepath.Join(sessions, "notes.jsonl")
	if err := os.Symlink(secret, planted); err != nil {
		t.Skipf("cannot create symlink on this platform: %v", err)
	}

	err := startWatch(t, newWatcherManager(t, home), planted, "pi")
	if !errors.Is(err, adapters.ErrSessionFileEscapes) {
		t.Errorf("a symlink to %s inside pi's own session root was not refused (got %v) — the tail loop would open it and route the contents through the upload pipeline",
			secret, err)
	}
}

// TestStartWatch_RejectsADanglingSymlink covers the plant-then-create attack:
// the link's target does not exist at validation time, so a check that treats
// "does not resolve" as "not written yet" hands back the unresolved link.
func TestStartWatch_RejectsADanglingSymlink(t *testing.T) {
	home, sessions := piSessionsRoot(t)

	planted := filepath.Join(sessions, "notes.jsonl")
	if err := os.Symlink(filepath.Join(home, ".aws", "credentials"), planted); err != nil {
		t.Skipf("cannot create symlink on this platform: %v", err)
	}

	err := startWatch(t, newWatcherManager(t, home), planted, "pi")
	if !errors.Is(err, adapters.ErrSessionFileEscapes) {
		t.Errorf("a dangling symlink inside pi's session root was not refused (got %v) — creating the target afterwards would make the daemon read it", err)
	}
}
