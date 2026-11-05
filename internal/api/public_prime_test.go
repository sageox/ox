package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readestKey is sha256("github.com/readest/readest"), computed outside Go
// (printf | shasum -a 256). api-go's opensource.PublicRepoKey test pins the
// same vector, so the two sides cannot drift without a red test.
const readestKey = "948faf8132e02fda1774738168cc08164107b1193a39214e013bddc0b9cbaebb"

// publicPrimeGolden is a byte copy of api-go's
// internal/handlers/opensource/testdata/public_prime_wire.golden.json: what
// the server actually writes.
func publicPrimeGolden(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "public_prime_wire.golden.json"))
	require.NoError(t, err)
	return raw
}

func wantGoldenBundle() *PublicPrimeResponse {
	return &PublicPrimeResponse{
		Version: 1,
		Project: PublicPrimeProject{Name: "Readest", URL: "https://sageox.test/team/team_readestproj01"},
		AsOf:    time.Date(2026, 10, 19, 18, 0, 0, 0, time.UTC),
		Viewer:  PublicPrimeViewerAnonymous,
		Plans: []PublicPrimePlan{
			{Stance: "decided", Title: "Merge-safe sync"},
			{Stance: "decided", Title: "Reflow width set aside Fake forged row"},
			{Stance: "proposed", Title: "Annotation export"},
			{Stance: "set-aside", Title: "Dolt as the only remote"},
		},
		Activity: []PublicPrimeWindow{
			{StartUTC: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC), EndUTC: time.Date(2026, 10, 19, 18, 0, 0, 0, time.UTC), Headline: "Annotation export plan published."},
			{StartUTC: time.Date(2026, 10, 19, 6, 0, 0, 0, time.UTC), EndUTC: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC), Headline: "Reflow rendering work; two PRs on CSS width."},
		},
		Murmurs: []PublicPrimeMurmur{
			{Kind: "pr", Text: "PR #6901 opened: Respect max-width in reflowed books", URL: "https://github.com/readest/readest/pull/6901", At: time.Date(2026, 10, 19, 17, 56, 0, 0, time.UTC)},
			{Kind: "work", Text: "Tracing the reflow renderer for #6857", At: time.Date(2026, 10, 19, 17, 38, 0, 0, time.UTC)},
			{Kind: "pr", Text: "14 more PRs opened this hour", URL: "https://github.com/readest/readest/pulls", At: time.Date(2026, 10, 19, 16, 0, 0, 0, time.UTC)},
		},
	}
}

// TestPublicRepoKey_MatchesTheServerVector — Failure prevented: ox and the
// server hash a repo differently, so no clone ever finds its project.
func TestPublicRepoKey_MatchesTheServerVector(t *testing.T) {
	assert.Equal(t, readestKey, PublicRepoKey("readest", "readest"))
	assert.Equal(t, readestKey, PublicRepoKey("Readest", "READEST"))
	assert.Equal(t, "b6599b84efbfe6234125d0112f0086c4a6e8555afbe64598fe0e1dedd8dc979b", PublicRepoKey("gastownhall", "beads"))
}

// TestDecodePublicPrime_ServerGolden — Failure prevented: a field renamed on
// one side decodes as empty and the prime silently loses its plans.
func TestDecodePublicPrime_ServerGolden(t *testing.T) {
	got, err := decodePublicPrime(publicPrimeGolden(t))
	require.NoError(t, err)
	assert.Equal(t, wantGoldenBundle(), got)

	// strict decode of the golden: every server field is one this ox models
	dec := json.NewDecoder(bytes.NewReader(publicPrimeGolden(t)))
	// strict-reader-ok: asserts the server golden holds only fields this ox models
	dec.DisallowUnknownFields()
	var strict PublicPrimeResponse
	require.NoError(t, dec.Decode(&strict))
}

// TestDecodePublicPrime_NewerServerAddsFields — Failure prevented: a server
// that adds an optional field breaks every older ox's prime (the
// tolerant-readers rule). Unknown fields at every level must decode to the
// same bundle.
func TestDecodePublicPrime_NewerServerAddsFields(t *testing.T) {
	var doc map[string]any
	require.NoError(t, json.Unmarshal(publicPrimeGolden(t), &doc))
	doc["edition"] = 7
	doc["project"].(map[string]any)["tagline"] = "added later"
	for _, key := range []string{"plans", "activity", "murmurs"} {
		for _, item := range doc[key].([]any) {
			item.(map[string]any)["added_later"] = true
		}
	}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)

	got, err := decodePublicPrime(raw)
	require.NoError(t, err)
	assert.Equal(t, wantGoldenBundle(), got)
}

// TestDecodePublicPrime_RefusesAMeaningChange — Failure prevented: a server
// that changed what a field means (version bump) is read with the old
// meaning instead of being refused.
func TestDecodePublicPrime_RefusesAMeaningChange(t *testing.T) {
	raw := bytes.Replace(publicPrimeGolden(t), []byte(`"version": 1`), []byte(`"version": 2`), 1)
	_, err := decodePublicPrime(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version 2 not supported")

	_, err = decodePublicPrime([]byte(`{"version":1,"project":{"name":"  "}}`))
	require.Error(t, err, "a bundle with no project cannot print its header")
}

// TestGetPublicPrime_Wire — Failure prevented: the CLI sends the repo's name
// (a private repo would leak), forgets the token for a signed-in user, or
// reads "not on SageOx" as an error.
func TestGetPublicPrime_Wire(t *testing.T) {
	var gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery, gotAuth = r.URL.RawQuery, r.Header.Get("Authorization")
		if r.URL.Path != "/api/v1/public/prime" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("repo_sha256") != readestKey {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"NOT_FOUND"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(publicPrimeGolden(t))
	}))
	defer srv.Close()
	ctx := context.Background()

	got, err := NewRepoClientWithEndpoint(srv.URL).WithAuthToken("tok-123").GetPublicPrime(ctx, readestKey)
	require.NoError(t, err)
	assert.Equal(t, wantGoldenBundle(), got)
	assert.Equal(t, "repo_sha256="+readestKey, gotQuery, "only the key is sent")
	assert.NotContains(t, gotQuery, "readest/readest")
	assert.Equal(t, "Bearer tok-123", gotAuth)

	got, err = NewRepoClientWithEndpoint(srv.URL).GetPublicPrime(ctx, PublicRepoKey("someone", "private-thing"))
	require.NoError(t, err, "not on SageOx is not an error")
	assert.Nil(t, got)
	assert.Empty(t, gotAuth, "anonymous when signed out")
}

// TestGetPublicPrime_ServerTrouble — Failure prevented: an outage or a slow
// server turns into a hung or noisy session start.
func TestGetPublicPrime_ServerTrouble(t *testing.T) {
	t.Run("5xx is an error, not a bundle", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()
		got, err := NewRepoClientWithEndpoint(srv.URL).GetPublicPrime(context.Background(), readestKey)
		require.Error(t, err)
		assert.Nil(t, got)
	})
	t.Run("a slow server is cut off by the caller's deadline", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()
		defer close(release)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := NewRepoClientWithEndpoint(srv.URL).GetPublicPrime(ctx, readestKey)
		require.Error(t, err)
	})
	t.Run("an oversized body is refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"version":1,"project":{"name":"x"},"pad":"` + strings.Repeat("a", publicPrimeMaxBytes) + `"}`))
		}))
		defer srv.Close()
		_, err := NewRepoClientWithEndpoint(srv.URL).GetPublicPrime(context.Background(), readestKey)
		require.Error(t, err)
	})
}
