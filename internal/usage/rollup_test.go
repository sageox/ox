package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/recap"
	"github.com/sageox/ox/internal/session/contexttrace"
)

var rollupNow = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

type person struct{ userID, username, agentID, slug string }

var (
	alice = person{"user_alice01", "Alice Zed", "OxAa11", "alice-zed"}
	bob   = person{"user_bob0002", "Bob Yu", "OxBb22", "bob-yu"}
)

func sessionName(p person, hour int) string {
	return rollupNow.Add(-time.Duration(hour)*time.Hour).Format("2006-01-02T15-04") + "-" + p.slug + "-" + p.agentID
}

// writeLedgerSession writes a realistic session (meta.json + an in-place
// context-trace) into ledger and returns its name.
func writeLedgerSession(t *testing.T, ledger string, p person, hoursAgo int, mutate func(*lfs.SessionMeta), events ...contexttrace.Event) string {
	t.Helper()
	name := sessionName(p, hoursAgo)
	dir := filepath.Join(ledger, "sessions", name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	meta := &lfs.SessionMeta{
		Version: "1.0", SessionName: name, Username: p.username, UserID: p.userID,
		AgentID: p.agentID, AgentType: "codex", SessionID: "ses_" + name, Title: "work " + name[len(name)-6:],
		CreatedAt: rollupNow.Add(-time.Duration(hoursAgo) * time.Hour), RepoID: "repo_x", Files: map[string]lfs.FileRef{},
	}
	if mutate != nil {
		mutate(meta)
	}
	require.NoError(t, lfs.WriteSessionMetaOnly(dir, meta))
	if len(events) > 0 {
		w := contexttrace.NewWriter(dir)
		require.NoError(t, w.AppendAll(events))
	}
	return name
}

func writePointerTrace(t *testing.T, ledger, name string) {
	t.Helper()
	ref := lfs.NewFileRef([]byte(`{"type":"invoked"}`))
	require.NoError(t, lfs.WritePointerFile(filepath.Join(ledger, "sessions", name, contexttrace.FileName), lfs.AssertUploaded(ref)))
}

func rollupIn(ledger string, id recap.Identity) RollupInput {
	return RollupInput{LedgerPath: ledger, Identity: id, Since: rollupNow.Add(-7 * 24 * time.Hour), Until: rollupNow.Add(time.Hour), Window: "7d", Limit: 10, Width: 80}
}

func aliceID() recap.Identity { return recap.Identity{UserID: alice.userID} }

func queryCall(q string) contexttrace.Event {
	ev := invoked("searched team memory", "query", 200, 3)
	ev.Query, ev.RefType = q, "query"
	return ev
}

// TestBuildMe: one row per own session, honest about what each trace holds.
// Failure prevented: a teammate's session shows in "my" rollup, a draft is
// counted as a finished session, a dehydrated trace is read as "no usage",
// or a pre-usage session reads as zero ox calls.
func TestBuildMe(t *testing.T) {
	ledger := t.TempDir()
	scored := func(m *lfs.SessionMeta) { m.SageoxScoreCategory = "moderate" }
	traced := writeLedgerSession(t, ledger, alice, 1, scored,
		primeEvent(), memoryProvided(), invoked("checked murmurs", "murmur list", 300, 2), selfReport("MEMORY.md"))
	old := writeLedgerSession(t, ledger, alice, 2, nil, memoryProvided())
	stub := writeLedgerSession(t, ledger, alice, 3, nil)
	writePointerTrace(t, ledger, stub)
	writeLedgerSession(t, ledger, alice, 4, func(m *lfs.SessionMeta) { m.Draft, m.Title = true, "" })
	writeLedgerSession(t, ledger, bob, 1, nil, invoked("checked murmurs", "murmur list", 300, 2))
	writeLedgerSession(t, ledger, alice, 24*30, nil, invoked("checked murmurs", "murmur list", 300, 2)) // outside the window

	r := BuildMe(rollupIn(ledger, aliceID()))

	assert.Equal(t, ViewMe, r.View)
	assert.Equal(t, guidanceMe, r.Guidance)
	require.Len(t, r.Sessions, 3, "own, in-window, non-draft sessions only")
	assert.Equal(t, []string{traced, old, stub}, []string{r.Sessions[0].Name, r.Sessions[1].Name, r.Sessions[2].Name}, "newest first")

	row := r.Sessions[0]
	assert.Equal(t, RowTraced, row.Trace)
	assert.Equal(t, 2, row.Calls)
	assert.Equal(t, 4300, row.Tokens)
	assert.Equal(t, 1, row.Surfaced)
	assert.Equal(t, 1, row.Useful, "a self-report on a surfaced item is reported-grade use evidence")
	assert.Equal(t, "moderate", row.Score)
	assert.Equal(t, RowNoTrace, r.Sessions[1].Trace)
	assert.Equal(t, RowNotHydrated, r.Sessions[2].Trace)
	assert.Equal(t, MeTotals{Sessions: 3, Traced: 1, NotHydrated: 1, Calls: 2, Tokens: 4300, Useful: 1}, r.Totals)

	assert.Contains(t, r.Display, "no usage trace")
	assert.Contains(t, r.Display, "—")
	assert.Contains(t, r.Display, "1 not hydrated; `ox session view <name> --context`")
	assertPlainFits(t, r.Display, 80)
	assertPlainFits(t, DisplayMe(r, 120), 120)
}

// TestBuildMe_LimitAndEmpty: --limit is honored and an empty window says so.
func TestBuildMe_LimitAndEmpty(t *testing.T) {
	ledger := t.TempDir()
	for h := 1; h <= 5; h++ {
		writeLedgerSession(t, ledger, alice, h, nil, invoked("checked murmurs", "murmur list", 10, 1))
	}
	in := rollupIn(ledger, aliceID())
	in.Limit = 2
	assert.Len(t, BuildMe(in).Sessions, 2)

	empty := BuildMe(rollupIn(t.TempDir(), aliceID()))
	assert.Empty(t, empty.Sessions)
	assert.NotNil(t, empty.Sessions)
	assert.Contains(t, empty.Display, "no recorded sessions in this window")
}

// TestBuildTeam_AggregatesWithoutPrincipals is the ADR-027 rule 2 guard: the
// team view sums across coworkers and never says who did what. Failure
// prevented: a value report that doubles as a surveillance tool — a name,
// principal ID, session name (which embeds a username), murmur author or a
// person's own query text leaking into the team payload or display.
func TestBuildTeam_AggregatesWithoutPrincipals(t *testing.T) {
	ledger := t.TempDir()
	w := whisper()
	w.From = alice.agentID
	names := []string{
		writeLedgerSession(t, ledger, alice, 1, nil, primeEvent(), memoryProvided(), invoked("checked murmurs", "murmur list", 300, 2),
			queryCall("alice secret roadmap"), w),
		writeLedgerSession(t, ledger, alice, 2, nil, invoked("checked murmurs", "murmur list", 100, 2)),
		writeLedgerSession(t, ledger, bob, 3, nil, primeEvent(), memoryProvided(), invoked("pulled plan", "plan view", 50, 1)),
		writeLedgerSession(t, ledger, bob, 4, nil, memoryProvided()), // pre-usage trace: counted as a session, not traced
	}

	r := BuildTeam(rollupIn(ledger, recap.Identity{}))

	assert.Equal(t, ViewTeam, r.View)
	assert.Equal(t, 4, r.Sessions)
	assert.Equal(t, 3, r.Traced)
	assert.Contains(t, r.Calls, LabelStat{Label: "checked murmurs", Count: 2, Tokens: 400})
	assert.Contains(t, r.Calls, LabelStat{Label: "loaded team context", Count: 2, Tokens: 8000})
	assert.Contains(t, r.Context, SourceStat{Source: "prime:team", Tokens: 6200})
	require.NotEmpty(t, r.TopSurfaced)
	assert.Equal(t, DocStat{Ref: "MEMORY.md", Sessions: 2}, r.TopSurfaced[0])
	assert.Contains(t, r.Guidance, "ox recap")
	assert.Equal(t, "ox recap", r.NextActions[0].Action)

	payload, err := json.Marshal(r)
	require.NoError(t, err)
	out := string(payload) + "\n" + r.Display + "\n" + DisplayTeam(r, 120)
	forbidden := []string{"alice secret roadmap", "mur_x"}
	forbidden = append(forbidden, names...)
	for _, p := range []person{alice, bob} {
		forbidden = append(forbidden, p.userID, p.username, p.agentID, p.slug)
	}
	for _, f := range forbidden {
		assert.NotContains(t, strings.ToLower(out), strings.ToLower(f), "team view leaked %q", f)
	}
	assertPlainFits(t, r.Display, 80)
}

func assertPlainFits(t *testing.T, display string, width int) {
	t.Helper()
	assert.NotContains(t, display, "\x1b")
	lines := strings.Split(strings.TrimRight(display, "\n"), "\n")
	assert.LessOrEqual(t, len(lines), maxDisplayLines)
	for _, l := range lines {
		assert.LessOrEqual(t, ansi.StringWidth(l), width, "overflows %d: %q", width, l)
	}
}
