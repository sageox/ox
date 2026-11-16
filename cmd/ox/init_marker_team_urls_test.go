package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/gitserver"
)

// The .sageox/.repo_* marker is committed, so every collaborator on the repo
// reads it. A user's credentials list every team they belong to; none of those
// team names or git URLs may leak into the marker (#1336).
func TestUpdateMarkerWithCachedURLs_WritesNoTeamURLs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	const endpointURL = "https://api.sageox.ai"
	const ledgerURL = "https://git.sageox.ai/ledgers/repo.git"
	require.NoError(t, gitserver.SaveCredentialsForEndpoint(endpointURL, gitserver.GitCredentials{
		Token:     "pat",
		ServerURL: "https://git.sageox.ai",
		ExpiresAt: time.Now().Add(time.Hour),
		Repos: map[string]gitserver.RepoEntry{
			"ledger":    {Name: "ledger", Type: "ledger", URL: ledgerURL},
			"team_aaa1": {Name: "team-Acme Platform", Type: "team-context", TeamID: "team_aaa1", URL: "https://git.sageox.ai/teams/aaa1.git"},
			"team_bbb2": {Name: "team-Side Project", Type: "team-context", TeamID: "team_bbb2", URL: "https://git.sageox.ai/teams/bbb2.git"},
			"team_ccc3": {Name: "team-Secret Acquisition", Type: "team-context", TeamID: "team_ccc3", URL: "https://git.sageox.ai/teams/ccc3.git"},
		},
	}))

	sageoxDir := t.TempDir()
	const repoID = "repo_01936a4e-8f2b-7c3d-9e1a-123456789abc"
	markerPath := filepath.Join(sageoxDir, ".repo_"+extractUUIDSuffix(repoID))
	require.NoError(t, os.WriteFile(markerPath, []byte(`{"repo_id":"`+repoID+`","endpoint":"`+endpointURL+`"}`), 0o644))

	require.NoError(t, updateMarkerWithCachedURLs(sageoxDir, repoID, endpointURL))

	raw, err := os.ReadFile(markerPath)
	require.NoError(t, err)
	var marker map[string]any
	require.NoError(t, json.Unmarshal(raw, &marker))
	assert.NotContains(t, marker, "team_urls")
	for _, leaked := range []string{"Acme Platform", "Side Project", "Secret Acquisition", "aaa1", "bbb2", "ccc3", "/teams/"} {
		assert.NotContains(t, string(raw), leaked)
	}
	assert.Equal(t, ledgerURL, marker["ledger_url"])
}
