package recap

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/pkg/sessionsummary"
	"github.com/stretchr/testify/require"

	_ "github.com/sageox/ox/internal/testenv" // isolates git config for fixtures that shell out to git
)

// fixtureNow is the fixed "current time" test fixtures anchor around, so
// window-boundary assertions are deterministic instead of racing wall clock.
var fixtureNow = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

// fx bundles the on-disk fixture a recap test needs: a ledger checkout
// (sessions/ + .sageox/cache/), a team-context checkout, and a project root
// that tests may git-init for trailer-commit coverage. Every path lives
// under t.TempDir() so tests never touch the real filesystem.
type fx struct {
	t *testing.T

	Ledger  string // <tmp>/ledger
	Team    string // <tmp>/team
	Project string // <tmp>/project
}

// newFixture creates the ledger/, team/, and project/ directories empty.
// Tests populate them with the Write*/Git* helpers below.
func newFixture(t *testing.T) *fx {
	t.Helper()
	root := t.TempDir()
	f := &fx{
		t:       t,
		Ledger:  filepath.Join(root, "ledger"),
		Team:    filepath.Join(root, "team"),
		Project: filepath.Join(root, "project"),
	}
	require.NoError(t, os.MkdirAll(filepath.Join(f.Ledger, "sessions"), 0o755))
	require.NoError(t, os.MkdirAll(f.Team, 0o755))
	require.NoError(t, os.MkdirAll(f.Project, 0o755))
	return f
}

// SessionOpt mutates a fixture session's meta before it is written.
type SessionOpt func(*lfs.SessionMeta)

func WithUserID(id string) SessionOpt      { return func(m *lfs.SessionMeta) { m.UserID = id } }
func WithUsername(name string) SessionOpt  { return func(m *lfs.SessionMeta) { m.Username = name } }
func WithSessionID(id string) SessionOpt   { return func(m *lfs.SessionMeta) { m.SessionID = id } }
func WithRepoID(id string) SessionOpt      { return func(m *lfs.SessionMeta) { m.RepoID = id } }
func WithTitle(title string) SessionOpt    { return func(m *lfs.SessionMeta) { m.Title = title } }
func WithSummary(s string) SessionOpt      { return func(m *lfs.SessionMeta) { m.Summary = s } }
func WithCreatedAt(t time.Time) SessionOpt { return func(m *lfs.SessionMeta) { m.CreatedAt = t } }

// WriteSession writes a realistic meta.json for session `name` under the
// fixture ledger and returns its directory. Defaults produce a valid
// session belonging to the fixture's default identity (see defaultIdentity);
// override any field via opts.
func (f *fx) WriteSession(name string, opts ...SessionOpt) string {
	f.t.Helper()
	dir := filepath.Join(f.Ledger, "sessions", name)
	require.NoError(f.t, os.MkdirAll(dir, 0o755))

	meta := &lfs.SessionMeta{
		Version:     "1.0",
		SessionName: name,
		Username:    "ryan-snodgrass",
		UserID:      "user_test01",
		AgentID:     "Ox0001",
		AgentType:   "claude-code",
		SessionID:   "ses_" + name,
		Title:       "Session " + name,
		CreatedAt:   fixtureNow,
		RepoID:      "repo_test01",
		Files:       map[string]lfs.FileRef{},
	}
	for _, opt := range opts {
		opt(meta)
	}
	require.NoError(f.t, lfs.WriteSessionMetaOnly(dir, meta))
	return dir
}

// defaultIdentity matches the identity WriteSession stamps by default.
func defaultIdentity() Identity {
	return Identity{UserID: "user_test01", DisplayName: "ryan-snodgrass"}
}

// WriteTrace writes context-trace.jsonl in place (real content, not a
// pointer) for session `name`.
func (f *fx) WriteTrace(name string, events ...contexttrace.Event) {
	f.t.Helper()
	dir := filepath.Join(f.Ledger, "sessions", name)
	require.NoError(f.t, os.MkdirAll(dir, 0o755))
	w := contexttrace.NewWriter(dir)
	for _, ev := range events {
		require.NoError(f.t, w.Append(ev))
	}
}

// WriteTraceCache writes context-trace.jsonl into the ledger's hydration
// cache for session `name` — the cache-first location readTrace checks.
func (f *fx) WriteTraceCache(name string, events ...contexttrace.Event) {
	f.t.Helper()
	dir := filepath.Join(f.Ledger, ".sageox", "cache", "sessions", name)
	require.NoError(f.t, os.MkdirAll(dir, 0o755))
	w := contexttrace.NewWriter(dir)
	for _, ev := range events {
		require.NoError(f.t, w.Append(ev))
	}
}

// WritePointerTrace replaces session `name`'s in-place context-trace.jsonl
// with an LFS pointer stub — the dehydrated-clone shape.
func (f *fx) WritePointerTrace(name string) {
	f.t.Helper()
	dir := filepath.Join(f.Ledger, "sessions", name)
	require.NoError(f.t, os.MkdirAll(dir, 0o755))
	ref := lfs.NewFileRef([]byte(`{"fake":"trace content, never hydrated in this test"}`))
	require.NoError(f.t, lfs.WritePointerFile(filepath.Join(dir, contexttrace.FileName), lfs.AssertUploaded(ref)))
}

// WriteSummary writes summary.json for session `name`.
func (f *fx) WriteSummary(name string, resp sessionsummary.SummarizeResponse) {
	f.t.Helper()
	dir := filepath.Join(f.Ledger, "sessions", name)
	require.NoError(f.t, os.MkdirAll(dir, 0o755))
	data, err := json.MarshalIndent(resp, "", "  ")
	require.NoError(f.t, err)
	require.NoError(f.t, os.WriteFile(filepath.Join(dir, "summary.json"), data, 0o644))
}

// WriteTeamDoc writes a markdown file under the team context's docs/.
func (f *fx) WriteTeamDoc(name, content string) string {
	f.t.Helper()
	dir := filepath.Join(f.Team, "docs")
	require.NoError(f.t, os.MkdirAll(dir, 0o755))
	p := filepath.Join(dir, name)
	require.NoError(f.t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

// WriteTeamRoot writes a file at the team context root (e.g. MEMORY.md,
// AGENTS.md — the knownRootDocs artifacts).
func (f *fx) WriteTeamRoot(name, content string) string {
	f.t.Helper()
	p := filepath.Join(f.Team, name)
	require.NoError(f.t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

// WriteDiscussion writes a recorded-discussion summary under
// <team>/discussions/<dirName>/summary.md.
func (f *fx) WriteDiscussion(dirName, summaryContent string) string {
	f.t.Helper()
	dir := filepath.Join(f.Team, "discussions", dirName)
	require.NoError(f.t, os.MkdirAll(dir, 0o755))
	p := filepath.Join(dir, "summary.md")
	require.NoError(f.t, os.WriteFile(p, []byte(summaryContent), 0o644))
	return p
}

// GitInit initializes the fixture's project root as a git repo with a
// repo-local identity (never the developer's global config — testenv also
// isolates GIT_CONFIG_GLOBAL for defense in depth).
func (f *fx) GitInit() {
	f.t.Helper()
	f.git("init")
	f.git("config", "user.name", "ox-test")
	f.git("config", "user.email", "test@test.sageox.ai")
}

// GitCommit creates an empty commit (no file changes needed for trailer-join
// tests) with the given message and returns its full SHA.
func (f *fx) GitCommit(message string) string {
	f.t.Helper()
	f.git("commit", "--allow-empty", "-m", message)
	return strings.TrimSpace(f.git("rev-parse", "HEAD"))
}

func (f *fx) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.Project
	out, err := cmd.CombinedOutput()
	require.NoError(f.t, err, "git %v: %s", args, out)
	return string(out)
}

// cachePathFor returns the ledger cache path for a session content file —
// the location several readers consult when the in-place file is absent
// (e.g. a teammate's synced session that hasn't hydrated locally).
func cachePathFor(ledgerPath, sessionName, filename string) string {
	return filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName, filename)
}

// writeJSONFile marshals v as indented JSON and writes it to path, creating
// parent directories as needed.
func writeJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	data, err := json.MarshalIndent(v, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
}
