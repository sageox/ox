package main

import (
	"bytes"
	"context"
	"encoding/json"
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
