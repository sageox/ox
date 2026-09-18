package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeMemberLister drives fetchAndRenderRoster without a network.
type fakeMemberLister struct {
	resp *api.TeamRosterResponse
	err  error
}

func (f fakeMemberLister) ListTeamRoster(_ context.Context, _ string) (*api.TeamRosterResponse, error) {
	return f.resp, f.err
}

// rosterEnvelope mirrors the stable --json shape writeRosterJSON emits.
type rosterEnvelope struct {
	TeamID    string           `json:"team_id"`
	Members   []api.TeamMember `json:"members"`
	Total     int              `json:"total"`
	Available bool             `json:"available"`
}

// TestFetchAndRenderRoster_JSONContract — the --json envelope carries the
// canonical roster fields and available:true on success.
func TestFetchAndRenderRoster_JSONContract(t *testing.T) {
	lister := fakeMemberLister{resp: &api.TeamRosterResponse{
		TeamID: "team_abc",
		Total:  2,
		Members: []api.TeamMember{
			{PrincipalID: "usr_ryan", Name: "Ryan Snodgrass", Type: "human", Role: "owner", Aliases: []string{"rsnodgrass"}},
			{PrincipalID: "agt_rip", Name: "Rip", Type: "ai", Role: "member"},
		},
	}}

	var buf bytes.Buffer
	err := fetchAndRenderRoster(context.Background(), &buf, lister, "team_abc", "Acme", true)
	require.NoError(t, err)

	var got rosterEnvelope
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "team_abc", got.TeamID)
	require.Len(t, got.Members, 2)
	assert.Equal(t, "ai", got.Members[1].Type)
	assert.True(t, got.Available, "success envelope must carry available:true")
}

// TestFetchAndRenderRoster_JSONEnvelopeStable — Failure prevented: a JSON
// consumer (often an AI coworker) can't rely on a single shape because success
// emits `members:null` / omits `available` while degrade emits `members:[]`.
// Both paths must carry the same keys: members is always an array, available is
// always present.
func TestFetchAndRenderRoster_JSONEnvelopeStable(t *testing.T) {
	// success with a nil members slice must still marshal members as [].
	ok := fakeMemberLister{resp: &api.TeamRosterResponse{TeamID: "team_abc", Total: 0}}
	var okBuf bytes.Buffer
	require.NoError(t, fetchAndRenderRoster(context.Background(), &okBuf, ok, "team_abc", "Acme", true))
	assert.Contains(t, okBuf.String(), `"members": []`, "nil members must marshal to [], never null")
	assert.Contains(t, okBuf.String(), `"available": true`)

	// graceful degrade carries the identical keys.
	deg := fakeMemberLister{err: api.ErrTeamRosterUnsupported}
	var degBuf bytes.Buffer
	require.NoError(t, fetchAndRenderRoster(context.Background(), &degBuf, deg, "team_abc", "Acme", true))
	assert.Contains(t, degBuf.String(), `"members": []`)
	assert.Contains(t, degBuf.String(), `"available": false`)
}

// TestFetchAndRenderRoster_SanitizesControlChars — Failure prevented: untrusted
// server text in a member column (here `type`, the field that was rendered raw)
// reaches the TTY as an ANSI/control sequence and can repaint or forge a row.
func TestFetchAndRenderRoster_SanitizesControlChars(t *testing.T) {
	lister := fakeMemberLister{resp: &api.TeamRosterResponse{
		TeamID:  "team_abc",
		Total:   1,
		Members: []api.TeamMember{{PrincipalID: "usr_x", Name: "Person A", Type: "\x1b[2Kspoof"}},
	}}
	var buf bytes.Buffer
	require.NoError(t, fetchAndRenderRoster(context.Background(), &buf, lister, "team_abc", "Acme", false))
	assert.NotContains(t, buf.String(), "\x1b[2K", "injected control sequence in type must be stripped")
	assert.Contains(t, buf.String(), "spoof", "printable remainder of the field survives")
}

// TestFetchAndRenderRoster_TextRendersNamesAndType — the human-readable table
// shows display names and human/ai type.
func TestFetchAndRenderRoster_TextRendersNamesAndType(t *testing.T) {
	lister := fakeMemberLister{resp: &api.TeamRosterResponse{
		TeamID:  "team_abc",
		Total:   2,
		Members: []api.TeamMember{{PrincipalID: "usr_ryan", Name: "Ryan Snodgrass", Type: "human"}, {PrincipalID: "agt_rip", Name: "Rip", Type: "ai"}},
	}}

	var buf bytes.Buffer
	err := fetchAndRenderRoster(context.Background(), &buf, lister, "team_abc", "Acme", false)
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Ryan Snodgrass")
	assert.Contains(t, out, "Rip")
	assert.Contains(t, out, "ai")
}

// TestFetchAndRenderRoster_UnsupportedDegrades — a flag-off / not-deployed 404
// degrades gracefully (no error, exit 0), never surfacing as a command failure.
func TestFetchAndRenderRoster_UnsupportedDegrades(t *testing.T) {
	lister := fakeMemberLister{err: api.ErrTeamRosterUnsupported}

	var buf bytes.Buffer
	err := fetchAndRenderRoster(context.Background(), &buf, lister, "team_abc", "Acme", false)
	require.NoError(t, err, "unsupported roster must degrade gracefully, not error")
	assert.Contains(t, strings.ToLower(buf.String()), "available")

	// JSON mode reports available:false rather than erroring.
	var jbuf bytes.Buffer
	err = fetchAndRenderRoster(context.Background(), &jbuf, lister, "team_abc", "Acme", true)
	require.NoError(t, err)
	var env map[string]any
	require.NoError(t, json.Unmarshal(jbuf.Bytes(), &env))
	assert.Equal(t, false, env["available"])
}

// TestFetchAndRenderRoster_UnavailableDegrades — a down/unreachable server (or a
// 5xx) degrades gracefully (no error, exit 0) with a "couldn't reach the server"
// message, and JSON mode reports available:false — not a hard crash.
func TestFetchAndRenderRoster_UnavailableDegrades(t *testing.T) {
	lister := fakeMemberLister{err: api.ErrTeamRosterUnavailable}

	var buf bytes.Buffer
	err := fetchAndRenderRoster(context.Background(), &buf, lister, "team_abc", "Acme", false)
	require.NoError(t, err, "an unreachable server must degrade gracefully, not error")
	assert.Contains(t, strings.ToLower(buf.String()), "couldn't reach")

	var jbuf bytes.Buffer
	err = fetchAndRenderRoster(context.Background(), &jbuf, lister, "team_abc", "Acme", true)
	require.NoError(t, err)
	var env map[string]any
	require.NoError(t, json.Unmarshal(jbuf.Bytes(), &env))
	assert.Equal(t, false, env["available"])
}

// TestFetchAndRenderRoster_AuthErrorSurfaces — a 401 is a real error the caller
// must see (to prompt `ox login`), not a graceful degrade.
func TestFetchAndRenderRoster_AuthErrorSurfaces(t *testing.T) {
	lister := fakeMemberLister{err: api.ErrUnauthorized}

	var buf bytes.Buffer
	err := fetchAndRenderRoster(context.Background(), &buf, lister, "team_abc", "Acme", false)
	require.Error(t, err)
	assert.ErrorIs(t, err, api.ErrUnauthorized)
}

// writeTeamFile drops one file inside a team-context checkout, creating
// whatever directories it needs.
func writeTeamFile(t *testing.T, teamPath, relPath, content string) {
	t.Helper()
	abs := filepath.Join(teamPath, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
}

// TestReadPublishedContent_DistinguishesAbsentFromEmpty: "nothing listed" and
// "nothing published" are different facts. The author of a rule that is not
// showing up needs to know which — an absent checkout is a sync problem, an
// empty one means they have not published yet.
func TestReadPublishedContent_DistinguishesAbsentFromEmpty(t *testing.T) {
	if got := readPublishedContent(""); got != nil {
		t.Errorf("an unknown team path reported published content: %+v", got)
	}

	if got := readPublishedContent(filepath.Join(t.TempDir(), "never-cloned")); got != nil {
		t.Errorf("an absent checkout reported published content: %+v", got)
	}

	empty := t.TempDir()
	got := readPublishedContent(empty)
	if got == nil {
		t.Fatal("a present-but-empty checkout was reported as absent")
	}
	if len(got.Rules) != 0 || len(got.Skills) != 0 {
		t.Errorf("empty checkout listed content: %+v", got)
	}
}

// TestReadPublishedContent_ListsWhatIsOnDisk reads a real checkout, because
// the card's whole claim is about content the team actually published — an
// empty temp dir cannot tell us the rule and skill lists are wired to
// discovery at all.
func TestReadPublishedContent_ListsWhatIsOnDisk(t *testing.T) {
	team := t.TempDir()
	writeTeamFile(t, team, "agents/rules/escalation-policy.md",
		"---\nname: escalation-policy\ndescription: who to page\n---\nBody.\n")
	writeTeamFile(t, team, "agents/skills/fork-scout/SKILL.md",
		"---\nname: fork-scout\ndescription: scouts forks\nrepos: [\"acme/api\"]\n---\nBody.\n")

	got := readPublishedContent(team)
	if got == nil {
		t.Fatal("a populated checkout was reported as absent")
	}

	if len(got.Rules) != 1 || got.Rules[0].Name != "escalation-policy" {
		t.Fatalf("Rules = %+v", got.Rules)
	}
	if !got.Rules[0].AllRepos || len(got.Rules[0].Repos) != 0 {
		t.Errorf("a rule with no repos: list did not report all-repos reach: %+v", got.Rules[0])
	}
	if got.Rules[0].Description != "who to page" {
		t.Errorf("Rules[0].Description = %q", got.Rules[0].Description)
	}

	if len(got.Skills) != 1 || got.Skills[0].Name != "fork-scout" {
		t.Fatalf("Skills = %+v", got.Skills)
	}
	if got.Skills[0].AllRepos {
		t.Errorf("a repo-scoped skill claimed all-repos reach: %+v", got.Skills[0])
	}
	if !slices.Equal(got.Skills[0].Repos, []string{"acme/api"}) {
		t.Errorf("Skills[0].Repos = %v", got.Skills[0].Repos)
	}
}

// TestReadPublishedContent_UnreadableIsNotEmpty is the honesty case. When one
// half of discovery fails, rendering the other half would print "Rules: none
// published" over a directory we could not open — and the author of a rule
// that is not showing up would read that as "I never published it." Absent
// says "something is wrong here" instead.
func TestReadPublishedContent_UnreadableIsNotEmpty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	team := t.TempDir()

	// skills read fine ...
	writeTeamFile(t, team, "agents/skills/fork-scout/SKILL.md",
		"---\nname: fork-scout\ndescription: scouts forks\n---\nBody.\n")

	// ... while the rules root is a symlink to itself, so stat returns ELOOP.
	if err := os.Symlink("rules", filepath.Join(team, "agents", "rules")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if got := readPublishedContent(team); got != nil {
		t.Errorf("an unreadable rules root was rendered as published content: %+v", got)
	}
}

// TestWriteTeamShowJSON_PublishedContract pins the shape `ox team show --json`
// promises. Every published item carries every field, and a team-wide item
// says so with a flag — an empty repos array on its own reads as "reaches
// nothing", which is exactly backwards and exactly the misreading this
// section exists to prevent.
func TestWriteTeamShowJSON_PublishedContract(t *testing.T) {
	card := teamCard{
		teamID: "team_acme",
		name:   "Acme",
		path:   "/tmp/acme",
		published: &publishedContent{
			Rules:  []publishedItem{newPublishedItem("escalation-policy", "", nil)},
			Skills: []publishedItem{newPublishedItem("fork-scout", "scouts forks", []string{"acme/api", "acme/worker"})},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, writeTeamShowJSON(&buf, card, 3, true, "https://sageox.ai/team/team_acme"))

	assert.Contains(t, buf.String(), `"repos": []`, "a team-wide rule must carry an explicit empty repos list")
	assert.NotContains(t, buf.String(), `"repos": null`, "repos must never marshal as null")

	var env struct {
		Published *struct {
			Rules  []map[string]json.RawMessage `json:"rules"`
			Skills []map[string]json.RawMessage `json:"skills"`
		} `json:"published"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	require.NotNil(t, env.Published)
	require.Len(t, env.Published.Rules, 1)
	require.Len(t, env.Published.Skills, 1)

	for _, item := range []map[string]json.RawMessage{env.Published.Rules[0], env.Published.Skills[0]} {
		for _, field := range []string{"name", "description", "repos", "all_repos"} {
			assert.Contains(t, item, field, "every published item carries every field")
		}
	}

	rule := env.Published.Rules[0]
	assert.JSONEq(t, `""`, string(rule["description"]), "an empty description is emitted, not omitted")
	assert.JSONEq(t, `[]`, string(rule["repos"]))
	assert.JSONEq(t, `true`, string(rule["all_repos"]), "no repos: list means every repo on the team")

	skill := env.Published.Skills[0]
	assert.JSONEq(t, `["acme/api","acme/worker"]`, string(skill["repos"]))
	assert.JSONEq(t, `false`, string(skill["all_repos"]))
}

// TestWriteTeamShowJSON_OmitsPublishedWhenAbsent: an absent checkout must not
// produce an empty published object, which a consumer would read as "this
// team publishes nothing."
func TestWriteTeamShowJSON_OmitsPublishedWhenAbsent(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, writeTeamShowJSON(&buf, teamCard{teamID: "team_acme", name: "Acme"}, 0, false, ""))

	var env map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(buf.Bytes(), &env))
	assert.NotContains(t, env, "published")
}

// TestRenderPublished_SaysWhereEachReaches is the card's one new claim: which
// repos a rule or skill applies to. Empty repos: means EVERY repo on the team,
// which is what an author usually wants and often did not realize they got.
func TestRenderPublished_SaysWhereEachReaches(t *testing.T) {
	var sb strings.Builder
	renderPublished(&sb, &publishedContent{
		Rules:  []publishedItem{{Name: "escalation"}},
		Skills: []publishedItem{{Name: "deploy", Repos: []string{"acme/api", "acme/worker"}}},
	})
	out := sb.String()
	for _, want := range []string{"escalation", "all repos", "deploy", "acme/api, acme/worker"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	sb.Reset()
	renderPublished(&sb, &publishedContent{Rules: []publishedItem{}, Skills: []publishedItem{}})
	if !strings.Contains(sb.String(), "none published") {
		t.Errorf("an empty section did not say so:\n%s", sb.String())
	}

	sb.Reset()
	renderPublished(&sb, nil)
	if sb.String() != "" {
		t.Errorf("an absent checkout rendered a section:\n%q", sb.String())
	}
}
