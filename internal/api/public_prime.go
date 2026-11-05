package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sageox/ox/internal/logger"
	"github.com/sageox/ox/internal/useragent"
)

// publicPrimePath is the cloud endpoint that primes an AI coworker from a
// public open-source project on SageOx, for anyone, member or not. 404 means
// SageOx has no public project for the repo (or the endpoint is not deployed):
// callers treat it as "nothing to add".
const publicPrimePath = "/api/v1/public/prime"

// PublicPrimeVersion is the bundle version this ox understands. The server
// bumps it only when a field changes meaning; a newer version is refused, an
// added field is simply ignored.
const PublicPrimeVersion = 1

// publicPrimeMaxBytes bounds the response read. A bundle is a few kilobytes.
const publicPrimeMaxBytes = 256 << 10

// PublicPrimeResponse is the bundle GET /api/v1/public/prime returns.
// Decoded tolerantly: a newer server may add fields at any level.
type PublicPrimeResponse struct {
	Version  int                 `json:"version"`
	Project  PublicPrimeProject  `json:"project"`
	AsOf     time.Time           `json:"as_of"`
	Viewer   string              `json:"viewer"`
	Plans    []PublicPrimePlan   `json:"plans"`
	Activity []PublicPrimeWindow `json:"activity"`
	Murmurs  []PublicPrimeMurmur `json:"murmurs"`
}

// PublicPrimeProject names the project and its public SageOx page.
type PublicPrimeProject struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// PublicPrimePlan is one plan title and how the project settled it
// ("decided", "proposed", "set-aside"). URL is set only for a signed-in caller.
type PublicPrimePlan struct {
	Stance string `json:"stance"`
	Title  string `json:"title"`
	URL    string `json:"url,omitempty"`
}

// PublicPrimeWindow is one public Activity headline.
type PublicPrimeWindow struct {
	StartUTC time.Time `json:"start_utc"`
	EndUTC   time.Time `json:"end_utc"`
	Headline string    `json:"headline"`
}

// PublicPrimeMurmur is one author-less public murmur ("work" or "pr").
type PublicPrimeMurmur struct {
	Kind string    `json:"kind"`
	Text string    `json:"text"`
	URL  string    `json:"url,omitempty"`
	At   time.Time `json:"at"`
}

// Viewer values the server sends.
const (
	PublicPrimeViewerAnonymous = "anonymous"
	PublicPrimeViewerSignedIn  = "signed-in"
	PublicPrimeViewerMember    = "member"
)

// PublicRepoKey names a GitHub repo to the public prime endpoint: lowercase
// hex SHA-256 of "github.com/{owner}/{name}", lowercased. The repo's name is
// never sent, so priming from a clone of a private repo tells SageOx nothing
// it could read back. Must stay byte-identical to the server's
// opensource.PublicRepoKey (api-go), or no clone ever matches.
func PublicRepoKey(owner, name string) string {
	sum := sha256.Sum256([]byte(strings.ToLower("github.com/" + owner + "/" + name)))
	return hex.EncodeToString(sum[:])
}

// GetPublicPrime fetches the public prime bundle for the repo repoKey names
// (see PublicRepoKey). Returns (nil, nil) when SageOx has no public project
// for it. Sends the client's auth token when set: a signed-in caller also gets
// plan links, but the endpoint answers anonymous callers too.
func (c *RepoClient) GetPublicPrime(ctx context.Context, repoKey string) (*PublicPrimeResponse, error) {
	reqURL := strings.TrimSuffix(c.baseURL, "/") + publicPrimePath + "?repo_sha256=" + url.QueryEscape(repoKey)

	logger.LogHTTPRequest("GET", reqURL)
	start := time.Now()

	httpReq, err := useragent.NewRequest(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if c.authToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	resp, err := c.httpClient.Do(httpReq)
	duration := time.Since(start)
	if err != nil {
		logger.LogHTTPError("GET", reqURL, err, duration)
		return nil, fmt.Errorf("network error: %w", err)
	}
	defer resp.Body.Close()
	logger.LogHTTPResponse("GET", reqURL, resp.StatusCode, duration)

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("public prime fetch failed: status=%d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, publicPrimeMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	if len(body) > publicPrimeMaxBytes {
		return nil, fmt.Errorf("public prime response over %d bytes", publicPrimeMaxBytes)
	}
	return decodePublicPrime(body)
}

// decodePublicPrime reads a bundle tolerantly (unknown fields ignored) and
// refuses one whose version this ox does not understand.
func decodePublicPrime(body []byte) (*PublicPrimeResponse, error) {
	var out PublicPrimeResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("failed to decode public prime: %w", err)
	}
	if out.Version != PublicPrimeVersion {
		return nil, fmt.Errorf("public prime version %d not supported by this ox (want %d): upgrade ox", out.Version, PublicPrimeVersion)
	}
	if strings.TrimSpace(out.Project.Name) == "" {
		return nil, fmt.Errorf("public prime has no project name")
	}
	return &out, nil
}
