package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ossTeamID = "team_readestproj01"

// gitIn runs git in dir and fails the test on error.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

// contributorClone is a contributor's fork clone: their fork on origin, the
// project on upstream, full history.
func contributorClone(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "-c", "user.email=a@test.sageox.ai", "-c", "user.name=A", "commit", "-q", "--allow-empty", "-m", "root")
	gitIn(t, dir, "-c", "user.email=a@test.sageox.ai", "-c", "user.name=A", "commit", "-q", "--allow-empty", "-m", "second")
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/some-contributor/readest.git")
	gitIn(t, dir, "remote", "add", "upstream", "git@github.com:readest/readest.git")
	return dir
}

// stubPrimeByRepo answers like the server: the project's bundle for
// readest/readest (with the given viewer and team), 404 (nil) for any other repo.
func stubPrimeByRepo(t *testing.T, viewer string, team *api.PublicPrimeTeam, err error) *[]string {
	t.Helper()
	asked := &[]string{}
	orig := fetchPublicPrimeForRepo
	fetchPublicPrimeForRepo = func(_ context.Context, _, owner, name string) (*api.PublicPrimeResponse, error) {
		*asked = append(*asked, owner+"/"+name)
		if err != nil {
			return nil, err
		}
		if owner+"/"+name != "readest/readest" {
			return nil, nil
		}
		return &api.PublicPrimeResponse{Version: 1, Viewer: viewer, Team: team}, nil
	}
	t.Cleanup(func() { fetchPublicPrimeForRepo = orig })
	return asked
}

var contributorTeams = []api.TeamMembership{
	{ID: "team_personal01", Name: "Me", Role: "owner", Personal: true},
	{ID: ossTeamID, Name: "Readest", Role: "member"},
}

// TestSuggestOpenSourceTeam_ForkCloneFindsTheProjectOnUpstream — Failure
// prevented: a contributor who cloned their fork is offered nothing, picks
// their personal team, and their use never counts as the project's.
func TestSuggestOpenSourceTeam_ForkCloneFindsTheProjectOnUpstream(t *testing.T) {
	dir := contributorClone(t)
	asked := stubPrimeByRepo(t, api.PublicPrimeViewerMember, &api.PublicPrimeTeam{ID: ossTeamID, Slug: "readest"}, nil)

	assert.Equal(t, ossTeamID, suggestOpenSourceTeam(context.Background(), dir, contributorTeams))
	assert.Equal(t, []string{"some-contributor/readest", "readest/readest"}, *asked, "origin first, then upstream")
}

// TestSuggestOpenSourceTeam_NeverSuggestsWithoutMembership — Failure
// prevented: a team the caller isn't on is offered (or pre-selected) because
// the server named it.
func TestSuggestOpenSourceTeam_NeverSuggestsWithoutMembership(t *testing.T) {
	team := &api.PublicPrimeTeam{ID: ossTeamID}
	for _, tc := range []struct {
		name   string
		viewer string
		team   *api.PublicPrimeTeam
		err    error
		teams  []api.TeamMembership
	}{
		{"signed in, not a member (even if a team is sent)", api.PublicPrimeViewerSignedIn, team, nil, contributorTeams},
		{"anonymous", api.PublicPrimeViewerAnonymous, team, nil, contributorTeams},
		{"member, but the team isn't in my team list", api.PublicPrimeViewerMember, team, nil, contributorTeams[:1]},
		{"older server: member, no team field", api.PublicPrimeViewerMember, nil, nil, contributorTeams},
		{"offline", "", nil, errors.New("network down"), contributorTeams},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := contributorClone(t)
			stubPrimeByRepo(t, tc.viewer, tc.team, tc.err)
			assert.Empty(t, suggestOpenSourceTeam(context.Background(), dir, tc.teams))
		})
	}
}

// TestSuggestOpenSourceTeam_NotOnSageOxAsksNothingOfNoGitHubRepo — Failure
// prevented: a repo that isn't on GitHub costs a network call at init.
func TestSuggestOpenSourceTeam_NotOnSageOxAsksNothingOfNoGitHubRepo(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "remote", "add", "origin", "https://gitlab.example.internal/acme/app.git")
	asked := stubPrimeByRepo(t, api.PublicPrimeViewerMember, &api.PublicPrimeTeam{ID: ossTeamID}, nil)

	assert.Empty(t, suggestOpenSourceTeam(context.Background(), dir, contributorTeams))
	assert.Empty(t, *asked)
}

// TestSuggestOpenSourceTeam_ShallowCloneIsNotOffered — Failure prevented: a
// shallow clone's hashes are salted with the shallow boundary, so the server
// can't match the project's repo; offering the team would mint a duplicate
// repo inside it.
func TestSuggestOpenSourceTeam_ShallowCloneIsNotOffered(t *testing.T) {
	full := contributorClone(t)
	shallow := filepath.Join(t.TempDir(), "shallow")
	out, err := exec.Command("git", "clone", "-q", "--depth", "1", "file://"+full, shallow).CombinedOutput()
	require.NoError(t, err, string(out))
	gitIn(t, shallow, "remote", "set-url", "origin", "https://github.com/readest/readest.git")
	stubPrimeByRepo(t, api.PublicPrimeViewerMember, &api.PublicPrimeTeam{ID: ossTeamID}, nil)

	assert.Empty(t, suggestOpenSourceTeam(context.Background(), shallow, contributorTeams))
}

// TestSelectTeam_PreSelectsTheProjectsTeam — Failure prevented: the picker
// defaults to whatever sorts first (here the personal team) in a fresh clone
// of a project the person is on.
func TestSelectTeam_PreSelectsTheProjectsTeam(t *testing.T) {
	var selectedID string
	var err error
	out := captureRealStdout(t, func() {
		withStdin(t, "\n", func() {
			selectedID, _, err = selectTeamWithSuggestion(contributorTeams, "", ossTeamID)
		})
	})
	require.NoError(t, err)
	assert.Equal(t, ossTeamID, selectedID, "Enter accepts the project's team")
	assert.Contains(t, string(out), "(this project's team)")
}

// TestSelectTeam_CurrentTeamBeatsTheSuggestion — Failure prevented: a clone
// already bound to a team is silently rebound by pressing Enter.
func TestSelectTeam_CurrentTeamBeatsTheSuggestion(t *testing.T) {
	var selectedID string
	var err error
	out := captureRealStdout(t, func() {
		withStdin(t, "\n", func() {
			selectedID, _, err = selectTeamWithSuggestion(contributorTeams, "team_personal01", ossTeamID)
		})
	})
	require.NoError(t, err)
	assert.Equal(t, "team_personal01", selectedID)
	assert.NotContains(t, string(out), "(this project's team)")
}

// TestSelectTeam_NoInputNamesTheProjectsTeam — Failure prevented: an AI
// teammate running ox init without a terminal binds nothing silently, and is
// told the exact --team that links the clone.
func TestSelectTeam_NoInputNamesTheProjectsTeam(t *testing.T) {
	var err error
	withStdin(t, "", func() {
		_, _, err = selectTeamWithSuggestion(contributorTeams, "", ossTeamID)
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--team "+ossTeamID)
}

// TestSuggestOpenSourceTeam_OriginErrorStillTriesUpstream — Failure
// prevented: a 503 or timeout on the contributor's fork (origin) hides the
// project on upstream, and they're offered nothing.
func TestSuggestOpenSourceTeam_OriginErrorStillTriesUpstream(t *testing.T) {
	dir := contributorClone(t)
	var asked []string
	orig := fetchPublicPrimeForRepo
	fetchPublicPrimeForRepo = func(_ context.Context, _, owner, name string) (*api.PublicPrimeResponse, error) {
		asked = append(asked, owner+"/"+name)
		if owner == "some-contributor" {
			return nil, errors.New("public prime fetch failed: status=503")
		}
		return &api.PublicPrimeResponse{Version: 1, Viewer: api.PublicPrimeViewerMember, Team: &api.PublicPrimeTeam{ID: ossTeamID}}, nil
	}
	t.Cleanup(func() { fetchPublicPrimeForRepo = orig })

	assert.Equal(t, ossTeamID, suggestOpenSourceTeam(context.Background(), dir, contributorTeams))
	assert.Equal(t, []string{"some-contributor/readest", "readest/readest"}, asked)
}

// TestShallowCloneTeamWarning — Failure prevented: `ox init --team <oss>` in
// a --depth clone (an agent sandbox, CI) silently registers a duplicate repo
// in the project's team. --team is still honored; the person is warned.
func TestShallowTeamFlagWarning(t *testing.T) {
	full := contributorClone(t)
	assert.Empty(t, shallowTeamFlagWarning(full), "a full clone gets no warning")

	shallow := filepath.Join(t.TempDir(), "shallow")
	out, err := exec.Command("git", "clone", "-q", "--depth", "1", "file://"+full, shallow).CombinedOutput()
	require.NoError(t, err, string(out))
	warning := shallowTeamFlagWarning(shallow)
	assert.Contains(t, warning, "this run registers it as a separate repo")
	assert.Contains(t, warning, "git fetch --unshallow")
}

// TestSuggestOpenSourceTeam_SlowOriginLeavesUpstreamItsOwnBudget — Failure
// prevented: an origin that times out spends the whole lookup budget, so
// upstream (where a fork clone's project lives) is asked with a dead context.
func TestSuggestOpenSourceTeam_SlowOriginLeavesUpstreamItsOwnBudget(t *testing.T) {
	dir := contributorClone(t)
	origBudget := openSourceSuggestPerRemote
	openSourceSuggestPerRemote = 50 * time.Millisecond
	t.Cleanup(func() { openSourceSuggestPerRemote = origBudget })

	orig := fetchPublicPrimeForRepo
	fetchPublicPrimeForRepo = func(ctx context.Context, _, owner, _ string) (*api.PublicPrimeResponse, error) {
		if owner == "some-contributor" {
			<-ctx.Done() // a slow origin: answers only by timing out
			return nil, ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return nil, err // upstream asked with a spent budget
		}
		return &api.PublicPrimeResponse{Version: 1, Viewer: api.PublicPrimeViewerMember, Team: &api.PublicPrimeTeam{ID: ossTeamID}}, nil
	}
	t.Cleanup(func() { fetchPublicPrimeForRepo = orig })

	assert.Equal(t, ossTeamID, suggestOpenSourceTeam(context.Background(), dir, contributorTeams))
}

// TestSuggestOpenSourceTeam_OriginTeamImNotOnDoesNotHideUpstream — Failure
// prevented: origin names a team I'm not on, the loop stops there, and the
// project team on upstream (which I am on) is never offered.
func TestSuggestOpenSourceTeam_OriginTeamImNotOnDoesNotHideUpstream(t *testing.T) {
	dir := contributorClone(t)
	orig := fetchPublicPrimeForRepo
	fetchPublicPrimeForRepo = func(_ context.Context, _, owner, _ string) (*api.PublicPrimeResponse, error) {
		id := ossTeamID
		if owner == "some-contributor" {
			id = "team_someoneelse01"
		}
		return &api.PublicPrimeResponse{Version: 1, Viewer: api.PublicPrimeViewerMember, Team: &api.PublicPrimeTeam{ID: id}}, nil
	}
	t.Cleanup(func() { fetchPublicPrimeForRepo = orig })

	assert.Equal(t, ossTeamID, suggestOpenSourceTeam(context.Background(), dir, contributorTeams))
}

// TestShallowTeamFlagWarning_CloneWithARepoIDIsStillWarned — Failure
// prevented: a shallow clone holding a repo id that was never registered (a
// skipped registration still saves it) gets no warning and silently
// registers a duplicate. A clone with an id is warned that it may happen.
func TestShallowTeamFlagWarning_CloneWithARepoIDIsStillWarned(t *testing.T) {
	full := contributorClone(t)
	shallow := filepath.Join(t.TempDir(), "shallow")
	out, err := exec.Command("git", "clone", "-q", "--depth", "1", "file://"+full, shallow).CombinedOutput()
	require.NoError(t, err, string(out))

	for _, dir := range []string{full, shallow} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".sageox"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".sageox", "config.json"),
			[]byte(`{"repo_id":"repo_019a0000-0000-7000-8000-000000000001"}`), 0o600))
	}
	assert.Empty(t, shallowTeamFlagWarning(full), "a full clone stays silent")
	warning := shallowTeamFlagWarning(shallow)
	assert.Contains(t, warning, "If this clone's repo id isn't registered yet, this run registers it as a separate repo")
	assert.Contains(t, warning, "git fetch --unshallow")
}
