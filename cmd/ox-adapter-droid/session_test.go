package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestProjectDirMatchesRepo_SymlinkResolution(t *testing.T) {
	// create a real repo dir and resolve any intermediate symlinks (e.g., /tmp -> /private/tmp on macOS)
	realRepoDir := t.TempDir()
	realRepoDir, err := filepath.EvalSymlinks(realRepoDir)
	if err != nil {
		t.Fatalf("EvalSymlinks on realRepoDir: %v", err)
	}

	// create a project dir with a session file whose cwd points to the real repo
	projectDir := t.TempDir()
	sessionContent := fmt.Sprintf(`{"type":"session_start","cwd":"%s"}`, realRepoDir)
	if err := os.WriteFile(filepath.Join(projectDir, "test-session.jsonl"), []byte(sessionContent+"\n"), 0644); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	// create a symlink pointing to the real repo dir
	symlinkDir := filepath.Join(t.TempDir(), "symlink-repo")
	if err := os.Symlink(realRepoDir, symlinkDir); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	// symlink path should match the real repo
	if !projectDirMatchesRepo(projectDir, symlinkDir) {
		t.Errorf("expected projectDirMatchesRepo(projectDir, symlinkPath) to be true")
	}

	// real path should also still match
	if !projectDirMatchesRepo(projectDir, realRepoDir) {
		t.Errorf("expected projectDirMatchesRepo(projectDir, realRepoDir) to be true")
	}
}

func TestProjectDirMatchesRepo_ReverseSymlink(t *testing.T) {
	// create a real repo dir and resolve intermediate symlinks
	realRepoDir := t.TempDir()
	realRepoDir, err := filepath.EvalSymlinks(realRepoDir)
	if err != nil {
		t.Fatalf("EvalSymlinks on realRepoDir: %v", err)
	}

	// create a symlink pointing to the real repo dir
	symlinkDir := filepath.Join(t.TempDir(), "symlink-repo")
	if err := os.Symlink(realRepoDir, symlinkDir); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	// write a session file whose cwd is the symlink path (reverse case)
	projectDir := t.TempDir()
	sessionContent := fmt.Sprintf(`{"type":"session_start","cwd":"%s"}`, symlinkDir)
	if err := os.WriteFile(filepath.Join(projectDir, "test-session.jsonl"), []byte(sessionContent+"\n"), 0644); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	// real repo path should match even though session recorded the symlink path
	if !projectDirMatchesRepo(projectDir, realRepoDir) {
		t.Errorf("expected projectDirMatchesRepo(projectDir, realRepoDir) to be true when session cwd is symlink")
	}
}

// --- Directory resolution ---
//
// Every test above exercises projectDirMatchesRepo directly against a
// caller-supplied dirPath, so a wrong base directory name in findSessionFile
// itself was structurally invisible: the hardcoded ".factory"/"projects" join
// was never on the call path any test took. These tests drive the real entry
// points (droidSessionsDir, findSessionFile, handleDetect) through HOME, the
// same seam cmd/ox-adapter-pi's TestFindPiSession_MostRecent uses.

// TestDroidSessionsDir_RealPath pins the directory this adapter reads from to
// ~/.factory/sessions — confirmed against a real droid 0.126.0 install, NOT
// ~/.factory/projects, which does not exist on a real machine.
func TestDroidSessionsDir_RealPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := droidSessionsDir()
	if err != nil {
		t.Fatalf("droidSessionsDir: %v", err)
	}
	want := filepath.Join(home, ".factory", "sessions")
	if got != want {
		t.Errorf("droidSessionsDir() = %q, want %q", got, want)
	}
}

// TestFindSessionFile_RealStoreLayout is the regression gate: a session
// written to the real on-disk layout (~/.factory/sessions/<slug>/<uuid>.jsonl)
// must be found. Before the fix, findSessionFile joined ".factory"/"projects"
// — a directory droid never creates — so os.ReadDir failed and every lookup
// returned "no sessions for project X" against a repo that had real sessions.
// Failure prevented: tail-mode session discovery (agent_prime.go ->
// adapter.FindSessionFile) permanently missing every real droid session.
func TestFindSessionFile_RealStoreLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repoRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// "-some-project-slug" stands in for droid's real slug scheme (a
	// "-"-joined form of the cwd, e.g. "/Users/dev/project" ->
	// "-Users-dev-project"); findSessionFile matches on the session_start
	// cwd field rather than reconstructing the slug, so the directory name
	// itself is deliberately arbitrary here.
	projectDir := filepath.Join(home, ".factory", "sessions", "-some-project-slug")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sessionFile := filepath.Join(projectDir, "11111111-1111-1111-1111-111111111111.jsonl")
	content := fmt.Sprintf(`{"type":"session_start","id":"11111111-1111-1111-1111-111111111111","cwd":%q,"version":2}`+"\n"+
		`{"type":"message","timestamp":"2026-05-15T15:37:01.058Z","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`+"\n",
		repoRoot)
	if err := os.WriteFile(sessionFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, _, err := findSessionFile(repoRoot, "", "", "")
	if err != nil {
		t.Fatalf("findSessionFile: %v", err)
	}
	if got != sessionFile {
		t.Errorf("findSessionFile() = %q, want %q", got, sessionFile)
	}
}

// TestFindSessionFile_NoSessionsDir verifies a clean, typed error (not a
// panic or a silent empty match) when the sessions directory doesn't exist
// at all.
func TestFindSessionFile_NoSessionsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, _, err := findSessionFile(t.TempDir(), "", "", "")
	if err == nil {
		t.Error("expected an error when ~/.factory/sessions does not exist")
	}
}

// --- Real transcript round trip ---
//
// testdata/session-real.jsonl preserves the record types, field names,
// nesting, and block ordering of a transcript a real droid 0.126.0 binary
// wrote under ~/.factory/sessions/ — but the real conversation, environment,
// and reasoning content has been replaced with neutral placeholders. See
// testdata/PROVENANCE.md for exactly what was replaced and why. Hand
// -authored fixtures are exactly what let this adapter ship pointed at a
// directory ("projects") droid never wrote to: every real line was silently
// dropped and every test still passed.

const realFixture = "testdata/session-real.jsonl"

// TestReadSessionFile_RealTranscript is the gate: a transcript a real droid
// wrote must produce a real, non-empty conversation with both sides of the
// exchange represented.
//
// NOTE: none of the real transcripts available on this machine contain a
// tool_use/tool_result block (all are plain Q&A), so this test cannot and
// does not assert a "tool"-role entry. That parsing path remains unproven
// against real droid data.
func TestReadSessionFile_RealTranscript(t *testing.T) {
	entries, meta, err := readSessionFile(realFixture)
	if err != nil {
		t.Fatalf("readSessionFile: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("real droid transcript produced zero entries — the parser no longer matches droid's real format")
	}

	var roles []string
	for _, e := range entries {
		roles = append(roles, e.Role)
	}
	for _, want := range []string{"user", "assistant"} {
		if !containsRole(roles, want) {
			t.Errorf("no %q entry in %v — round trip incomplete", want, roles)
		}
	}

	if meta == nil {
		t.Fatal("no metadata extracted from a real session_start record")
	}
	if meta.AgentVersion == "" {
		t.Error("no agent version extracted from a real session_start record")
	}
	if meta.Model == "" {
		t.Error("no model extracted from the real companion .settings.json")
	}
}

// TestReadSessionFile_RealTranscript_ThinkingDropped verifies assistant
// "thinking" blocks (present in the real fixture) don't leak into the
// recorded content.
// Failure prevented: internal reasoning surfacing in the shared team ledger.
func TestReadSessionFile_RealTranscript_ThinkingDropped(t *testing.T) {
	entries, _, err := readSessionFile(realFixture)
	if err != nil {
		t.Fatalf("readSessionFile: %v", err)
	}
	for _, e := range entries {
		if e.Role == "assistant" && e.Content == "" {
			t.Errorf("empty assistant entry — a thinking-only block should be skipped, not emitted empty")
		}
	}
}

func containsRole(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// --- Incremental resume ---
//
// readFromOffset used to be a hand-rolled bufio.Scanner loop that advanced
// newOffset to the file's current size on every call, regardless of whether
// the last line scanned was complete. Droid writes its transcript
// incrementally, so a read that lands mid-write sees a partial trailing line;
// advancing past it acknowledges bytes that were never parsed, and the rest
// of that turn is silently lost forever once droid finishes writing it. The
// fix routes through adapterruntime.TailJSONL, which stops at the last
// complete newline instead.

// TestReadFromOffset_PartialTrailingLineNotConsumed is the regression test
// for that class of bug: a read landing mid-write must not advance the
// offset past the incomplete line, and completing the write later must
// produce that turn's entry exactly once — not corrupted, not dropped.
// Failure prevented: a turn written while the daemon's catch-up read is
// in-flight vanishes from the ledger permanently.
func TestReadFromOffset_PartialTrailingLineNotConsumed(t *testing.T) {
	sessionStart := `{"type":"session_start","id":"s1","cwd":"/tmp/x","version":2}` + "\n"
	line2 := `{"type":"message","id":"m2","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":[{"type":"text","text":"line two complete"}]}}` + "\n"
	line3Full := `{"type":"message","id":"m3","timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"line three complete"}]}}` + "\n"
	line3Partial := line3Full[:40] // cut mid-JSON, no trailing newline — simulates a write in flight

	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(sessionStart+line2+line3Partial), 0o644); err != nil {
		t.Fatal(err)
	}

	entries1, offset1, err := readFromOffset(path, 0)
	if err != nil {
		t.Fatalf("readFromOffset (partial write in flight): %v", err)
	}
	if len(entries1) != 1 || entries1[0].Content != "line two complete" {
		t.Fatalf("got %d entries %+v, want exactly the line-2 entry", len(entries1), entries1)
	}
	wantOffset1 := int64(len(sessionStart) + len(line2))
	if offset1 != wantOffset1 {
		t.Fatalf("offset after partial write = %d, want %d (must stop before the incomplete line, not advance to file size %d)",
			offset1, wantOffset1, len(sessionStart)+len(line2)+len(line3Partial))
	}

	// the write completes: the rest of line 3 is appended directly after the
	// partial bytes, exactly as droid would finish writing it
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line3Full[len(line3Partial):]); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	entries2, offset2, err := readFromOffset(path, offset1)
	if err != nil {
		t.Fatalf("readFromOffset (resumed after completion): %v", err)
	}
	if len(entries2) != 1 || entries2[0].Content != "line three complete" {
		t.Fatalf("got %d entries %+v, want exactly the now-complete line-3 entry — the old implementation lost this turn permanently",
			len(entries2), entries2)
	}
	wantOffset2 := offset1 + int64(len(line3Full))
	if offset2 != wantOffset2 {
		t.Fatalf("offset after resume = %d, want %d", offset2, wantOffset2)
	}
}
