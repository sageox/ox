package useragent

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetRepoID_Validation guards the only untrusted input on this path.
// The repo ID comes from .sageox/config.json, which a user can hand-edit.
//
// Failure prevented: a corrupt or malicious config value reaching an HTTP
// header — CRLF would split the request, and unbounded length would let a
// config file dictate request size.
func TestSetRepoID_Validation(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"typical repo id", "repo_01hxyz9abc", "repo_01hxyz9abc"},
		{"uuid form", "3f2b8c10-4d5e-4a1b-9c8d-7e6f5a4b3c2d", "3f2b8c10-4d5e-4a1b-9c8d-7e6f5a4b3c2d"},
		{"dotted form", "acme.ox.1", "acme.ox.1"},

		{"empty is dropped", "", ""},
		{"CRLF injection is dropped", "repo_1\r\nX-Evil: 1", ""},
		{"bare newline is dropped", "repo_1\nX-Evil: 1", ""},
		{"space is dropped", "repo 1", ""},
		{"colon is dropped", "repo:1", ""},
		{"non-ascii is dropped", "repo_✓", ""},
		{"over-long is dropped", string(make([]byte, 0, 129)) + repeat("a", 129), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ResetForTesting()
			SetRepoID(tt.id)
			assert.Equal(t, tt.want, RepoID())
		})
	}
}

// TestSetRepoID_FirstWriteWins matches the package's existing setter
// semantics — one invocation works in one repo, so a later call must not
// retag requests already attributed to the first.
func TestSetRepoID_FirstWriteWins(t *testing.T) {
	ResetForTesting()
	SetRepoID("repo_first")
	SetRepoID("repo_second")
	assert.Equal(t, "repo_first", RepoID())
}

// TestSetHeaders_RepoID is the regression test for the NULL repo_id analytics
// gap: the server can only derive a repo from routes whose URL embeds one, so
// the CLI must put the repo it is working in on every request.
//
// Failure prevented: the header silently not being sent, which returns
// cli_activity.repo_id to being NULL on every route except
// GET /cli/repos/{repo_id}.
func TestSetHeaders_RepoID(t *testing.T) {
	t.Run("sent when set", func(t *testing.T) {
		ResetForTesting()
		SetRepoID("repo_01hxyz9abc")

		h := http.Header{}
		SetHeaders(h)

		assert.Equal(t, "repo_01hxyz9abc", h.Get(HeaderRepoID))
	})

	// Outside a project, and in the daemon, there is no single correct repo.
	// Omitting the header is correct; sending a wrong one would be worse than
	// sending none, which is the whole point of this change.
	t.Run("absent when unset", func(t *testing.T) {
		ResetForTesting()

		h := http.Header{}
		SetHeaders(h)

		_, present := h[http.CanonicalHeaderKey(HeaderRepoID)]
		assert.False(t, present, "header must be omitted, not sent empty")
	})

	// A rejected value must leave no trace — not an empty header, which would
	// still read as "this client claims a repo" on the wire.
	t.Run("absent when value was rejected", func(t *testing.T) {
		ResetForTesting()
		SetRepoID("repo_1\r\nX-Evil: 1")

		h := http.Header{}
		SetHeaders(h)

		_, present := h[http.CanonicalHeaderKey(HeaderRepoID)]
		assert.False(t, present)
	})
}

// TestNewRequest_CarriesRepoID proves the header rides the real request
// constructor every API client uses, not just SetHeaders in isolation.
func TestNewRequest_CarriesRepoID(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()
	SetRepoID("repo_01hxyz9abc")

	req, err := NewRequest(t.Context(), http.MethodGet, "https://sageox.ai/api/v1/cli/repos", nil)
	assert.NoError(t, err)
	assert.Equal(t, "repo_01hxyz9abc", req.Header.Get(HeaderRepoID))
}

// TestRepoID_SurvivesRoundTrip proves the header reaches a server over a real
// HTTP round trip, not just that it was placed on a header map. Go's client
// silently drops header values it considers invalid, so an in-memory
// assertion alone would not catch a value that never makes it onto the wire.
func TestRepoID_SurvivesRoundTrip(t *testing.T) {
	ResetForTesting()
	defer ResetForTesting()

	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer srv.Close()

	SetRepoID("repo_01hxyz9abc")

	req, err := NewRequest(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, "repo_01hxyz9abc", got.Get(HeaderRepoID))
	assert.Contains(t, got.Get("User-Agent"), "ox/", "existing headers must be unaffected")
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}
	return string(out)
}
