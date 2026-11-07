package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestGetReposAndGitCredentials_CarriesClaudeOrgSync decodes the team list the
// daemon needs alongside the credentials, from ONE request, for a server that
// sends claude_org_synced_skills and one that predates it.
// Failure prevented: the field is silently dropped in decoding (a renamed JSON
// tag), and ox never stops duplicating org-delivered skills; or the combined
// call diverges from GetGitCredentials and credentials regress.
func TestGetReposAndGitCredentials_CarriesClaudeOrgSync(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		team string
		want []string
	}{
		{"server sends the field", `{"id":"team_a","name":"Acme","slug":"acme","role":"member","claude_org_synced_skills":["grill-me","notes"]}`, []string{"grill-me", "notes"}},
		{"older server omits it", `{"id":"team_a","name":"Acme","slug":"acme","role":"member"}`, nil},
		{"explicit empty list", `{"id":"team_a","name":"Acme","role":"member","claude_org_synced_skills":[]}`, []string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = w.Write([]byte(`{"token":"pat","server_url":"https://git.example","username":"u",` +
					`"expires_at":"2030-01-01T00:00:00Z",` +
					`"repos":{"acme":{"name":"acme","url":"https://git.example/acme.git","type":"team-context","team_id":"team_a"}},` +
					`"teams":[` + tt.team + `]}`))
			}))
			t.Cleanup(server.Close)
			client := &RepoClient{baseURL: server.URL, httpClient: &http.Client{Timeout: 5 * time.Second}, authToken: "bearer"}

			resp, creds, err := client.GetReposAndGitCredentials(context.Background())
			require.NoError(t, err)
			require.EqualValues(t, 1, requests.Load(), "the team list must come from the same request as the credentials")
			require.Len(t, resp.Teams, 1)
			require.Equal(t, tt.want, resp.Teams[0].ClaudeOrgSyncedSkills)

			want, err := client.GetGitCredentials(context.Background())
			require.NoError(t, err)
			require.Equal(t, want, creds, "the combined call bound different credentials")
			require.Equal(t, "pat", creds.Token)
			require.Contains(t, creds.Repos, "team_a")
		})
	}
}
