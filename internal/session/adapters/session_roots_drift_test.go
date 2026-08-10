package adapters

import (
	"path/filepath"
	"testing"
)

// This file is the drift alarm for the session-file allow-list.
//
// The allow-list is a hand-maintained map in a different package from the
// adapters it governs, so nothing forced the two to agree. Three adapters
// drifted out of it and each failure was silent: the daemon logged a warning
// nobody read and recording simply never started.
//
//	pi       — moved to ~/.pi/agent/sessions, was not listed
//	droid    — moved to ~/.factory/sessions, was not listed
//	opencode — stopped returning a path at all; returns an opaque handle
//	goose    — same
//
// The table below records what each adapter's find-session ACTUALLY emits,
// captured by running the adapter binaries. When an adapter changes where it
// stores sessions, this test fails instead of the feature silently dying.
type handleCase struct {
	adapter string
	// sessionFile is verbatim what that adapter's find-session returned.
	sessionFile string
	// captured says how this value was obtained, so a future maintainer can
	// re-derive it rather than trusting a literal in a test file.
	captured string
}

// realHandles are values observed from the shipped adapter binaries against
// real local stores on a dev machine, 2026-08-09, home dir normalized to ~.
var realHandles = []handleCase{
	{
		adapter:     "pi",
		sessionFile: "~/.pi/agent/sessions/-Users-someone-src-project/2026-04-04T01-03-29-089Z_3c51b155.jsonl",
		captured:    "ox-adapter-pi find-session --repo-root <repo>",
	},
	{
		adapter:     "droid",
		sessionFile: "~/.factory/sessions/-Users-someone-src-project/43800deb-fcc5-4a6a-92d5-4dc6c3de2377.jsonl",
		captured:    "ox-adapter-droid find-session --repo-root <repo>",
	},
	{
		adapter:     "codex",
		sessionFile: "~/.codex/sessions/2026/08/09/rollout-2026-08-09T10-33-16-019fe6f1.jsonl",
		captured:    "ox-adapter-codex find-session --repo-root <repo>",
	},
	{
		adapter:     "claude-code",
		sessionFile: "~/.claude/projects/-Users-someone-src-project/8f3c1e7a.jsonl",
		captured:    "ox-adapter-claude-code find-session --repo-root <repo>",
	},
	// These two do not tail a file at all. Both read from a SQLite database,
	// so find-session hands back an opaque "<adapter>:<id>" handle that only
	// the adapter itself can resolve. The allow-list was written assuming
	// every adapter names a path, and rejected both outright.
	{
		adapter:     "opencode",
		sessionFile: "opencode:ses_096fdea05ffeagb7OGYpJCte6a",
		captured:    "ox-adapter-opencode find-session --repo-root <repo>",
	},
	{
		adapter:     "goose",
		sessionFile: "goose:20260602_3",
		captured:    "ox-adapter-goose find-session --repo-root <repo>",
	},
	// Amp has no lifecycle hooks, so ox's own embedded plugin writes this
	// sidecar and ox tails it in place. The allow-list covered neither the
	// plugin's directory nor the legacy one.
	{
		adapter:     "amp",
		sessionFile: "~/.cache/amp/ox-sessions/T-a1b2c3d4.jsonl",
		captured:    "cmd/ox-adapter-amp/plugin/ox-bridge.ts writes join(homedir(), '.cache', 'amp', 'ox-sessions')",
	},
	{
		adapter:     "amp",
		sessionFile: "~/.amp/sessions/T-a1b2c3d4.jsonl",
		captured:    "cmd/ox-adapter-amp/main.go: legacy path for pre-2026 Amp installs",
	},
}

// TestAllowList_AiderIsStructurallyUngovernable records a gap the table above
// cannot express.
//
// Aider writes .aider.chat.history.md into the PROJECT ROOT, but every root in
// adapterSessionRoots is home-relative — KnownSessionRoots joins each onto
// homeDir. So no aider transcript can be allow-listed unless the repository
// happens to sit inside one of those home paths, and the daemon rejects every
// aider session it is handed.
//
// Fixing it means letting an adapter declare a project-relative session file
// and giving the daemon the repo root to check against, which widens the trust
// boundary the allow-list exists to hold. That is a deliberate design decision,
// not a data fix, so this test asserts the CURRENT behavior and names the gap
// rather than quietly papering over it.
func TestAllowList_AiderIsStructurallyUngovernable(t *testing.T) {
	const home = "/Users/someone"
	projectTranscript := "/Users/someone/src/project/.aider.chat.history.md"

	if IsSessionFileAllowed("aider", projectTranscript, home) {
		t.Fatal("aider transcripts are now allow-listed — delete this test and add aider to realHandles above")
	}
	t.Log("KNOWN GAP: aider writes .aider.chat.history.md into the project root, " +
		"but the allow-list is home-relative, so the daemon rejects every aider session. " +
		"Needs a project-relative declaration and a repo root on SessionWatchStartPayload.")
}

func TestAllowList_AcceptsWhatAdaptersActuallyReturn(t *testing.T) {
	const home = "/Users/someone"

	for _, tc := range realHandles {
		t.Run(tc.adapter, func(t *testing.T) {
			sessionFile := tc.sessionFile
			if len(sessionFile) > 1 && sessionFile[0] == '~' {
				sessionFile = filepath.Join(home, sessionFile[2:])
			}

			if !IsSessionFileAllowed(tc.adapter, sessionFile, home) {
				t.Errorf("the daemon would reject %s's own session handle, so recording never starts\n"+
					"  handle:   %s\n"+
					"  captured: %s\n"+
					"  fix:      add the root to adapterSessionRoots, or declare the adapter uses opaque handles",
					tc.adapter, tc.sessionFile, tc.captured)
			}
		})
	}
}

// TestAllowList_StillRejectsPathTraversal keeps the widening honest: opaque
// handles must not become a hole through which a same-UID IPC peer can name a
// file for the daemon to read and upload.
func TestAllowList_StillRejectsPathTraversal(t *testing.T) {
	const home = "/Users/someone"

	rejects := []struct {
		name        string
		adapter     string
		sessionFile string
	}{
		{"absolute path outside any root", "pi", "/etc/passwd"},
		{"ssh key via traversal", "pi", home + "/.pi/agent/sessions/../../.ssh/id_rsa"},
		{"ox auth token", "droid", home + "/.sageox/auth.json"},
		{"relative path", "codex", ".codex/sessions/x.jsonl"},
		{"empty", "codex", ""},
		{"unknown adapter", "totally-made-up", home + "/.codex/sessions/x.jsonl"},
		{"handle for an adapter that does not use handles", "pi", "pi:../../.ssh/id_rsa"},
		{"handle with a path separator", "opencode", "opencode:../../.ssh/id_rsa"},
		{"handle with a parent reference", "goose", "goose:.."},
		{"handle naming another adapter", "opencode", "goose:20260602_3"},
		{"handle with an absolute path", "opencode", "opencode:/etc/passwd"},
		{"bare scheme with no id", "opencode", "opencode:"},
		{"handle with a backslash", "opencode", `opencode:..\..\secrets`},
		{"handle with a null byte", "opencode", "opencode:ses_a\x00/etc/passwd"},
	}

	for _, tc := range rejects {
		t.Run(tc.name, func(t *testing.T) {
			if IsSessionFileAllowed(tc.adapter, tc.sessionFile, home) {
				t.Errorf("allow-list accepted %q for adapter %s — a same-UID IPC peer could route this file through the upload pipeline",
					tc.sessionFile, tc.adapter)
			}
		})
	}
}

// TestAllowList_EveryAdapterIsGoverned catches the other drift direction: a new
// adapter added to the registry but never given roots is rejected by default
// (fail-closed, which is correct) — but silently, which is not. Listing the
// governed set here makes adding an adapter a deliberate act.
func TestAllowList_EveryAdapterIsGoverned(t *testing.T) {
	governed := map[string]bool{}
	for name := range adapterSessionRoots {
		governed[name] = true
	}
	for name := range adapterSessionHandles {
		governed[name] = true
	}

	// every adapter shipped in cmd/ox-adapter-* that records sessions
	recording := []string{
		"claude-code", "codex", "gemini", "amp", "opencode",
		"pi", "aider", "droid", "goose", "generic",
	}

	for _, name := range recording {
		if !governed[CanonicalAdapterName(name)] {
			t.Errorf("adapter %q records sessions but has no entry in adapterSessionRoots or adapterSessionHandles — "+
				"the daemon will reject every session it finds, silently", name)
		}
	}
}
