// Package teamaccess answers one question before ox shows anything from a
// team's local Team Context checkout: is the person running ox signed in, and
// does the server still say they belong to that team?
//
// The checkout on disk is not proof of either. It survives `ox logout`, an
// expired login, and removal from the team until the daemon's next discovery
// pass, and anyone who can paste a recording link or id can point a command
// at it. So reads that surface team content ask here first.
//
// The rules:
//
//   - No usable credential for the endpoint: NotSignedIn / Expired. Nothing is
//     cached and nothing is asked of the server.
//   - A confirmation of membership from the server is cached for CacheTTL,
//     keyed by endpoint + a SHA-256 fingerprint of the token + team id. A
//     different account or a fresh login never reuses another principal's
//     answer, and the token itself never touches the cache file.
//   - Only "allowed" is cached. A refusal is asked again every time, so a
//     re-granted membership works on the next command.
//   - Offline, a fresh cached "allowed" still admits; otherwise the answer is
//     Unverified (retryable), never a silent pass.
package teamaccess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
)

// CacheTTL is how long a server confirmation of membership is trusted.
const CacheTTL = time.Hour

// CacheFileName is the basename of the cache inside paths.CacheDir(), next
// to auth's token_meta.json.
const CacheFileName = "team_access.json"

// refreshBufferSeconds matches the proactive-refresh window other
// authenticated commands use.
const refreshBufferSeconds = 300

// membershipTimeout bounds the one membership call a cache miss costs.
// A variable so tests can shorten it.
var membershipTimeout = 3 * time.Second

// now is the clock; tests replace it to age cache entries.
var now = time.Now

// Status is the outcome of Check.
type Status int

const (
	// Allowed: signed in and confirmed a member (by the server, or by a
	// confirmation under CacheTTL old).
	Allowed Status = iota
	// NotSignedIn: no credential for the endpoint.
	NotSignedIn
	// Expired: a credential exists but is expired, rejected by the server,
	// or could not be refreshed.
	Expired
	// EnvTokenMalformed: SAGEOX_TOKEN is set for this endpoint but failed its
	// local format check. `ox login` cannot fix this (the env token wins), so
	// it gets its own status and message.
	EnvTokenMalformed
	// NoAccess: the server answered, and this account is not a member of the
	// team.
	NoAccess
	// Unverified: membership could not be confirmed (offline, timeout, server
	// error) and no fresh confirmation is cached. Retryable.
	Unverified
)

// Verdict is Check's answer. Detail carries a short, safe reason for the
// Unverified case (never a token, never a response body).
type Verdict struct {
	Status   Status
	Endpoint string
	TeamID   string
	// FromCache is true when Allowed came from a cached confirmation.
	FromCache bool
	Detail    string
}

// Allowed reports whether the caller may read the team's content.
func (v Verdict) Allowed() bool { return v.Status == Allowed }

// Message is the user-facing sentence for a refusal. teamLabel is the team's
// display name (or id); an empty label falls back to the team id.
func (v Verdict) Message(teamLabel string) string {
	if teamLabel == "" {
		teamLabel = v.TeamID
	}
	host := endpoint.NormalizeSlug(v.Endpoint)
	switch v.Status {
	case Allowed:
		return ""
	case NotSignedIn:
		return fmt.Sprintf("you are not signed in to %s, so %s's conversations stay closed. Sign in with `ox login` and retry", host, teamLabel)
	case Expired:
		return fmt.Sprintf("your sign-in for %s has expired or was rejected, so %s's conversations stay closed. Sign in with `ox login` and retry", host, teamLabel)
	case EnvTokenMalformed:
		return fmt.Sprintf("SAGEOX_TOKEN is set but its value failed a local format check, so it was refused for %s. Re-copy the token, or unset SAGEOX_TOKEN to use a stored login", host)
	case NoAccess:
		return fmt.Sprintf("your account doesn't have access to %s on %s. Ask a team admin for an invite, or sign in with the account that belongs to it", teamLabel, host)
	case Unverified:
		msg := fmt.Sprintf("couldn't confirm your access to %s with %s", teamLabel, host)
		if v.Detail != "" {
			msg += " (" + v.Detail + ")"
		}
		return msg + "; connect and retry"
	default:
		return "access to this team could not be checked"
	}
}

// FindMembership returns the membership row for teamID in teams, if any. It
// is the single membership test: `ox doctor`'s team-visibility check and the
// read gate both use it.
func FindMembership(teams []api.TeamMembership, teamID string) (api.TeamMembership, bool) {
	if teamID == "" {
		return api.TeamMembership{}, false
	}
	for _, t := range teams {
		if t.ID == teamID {
			return t, true
		}
	}
	return api.TeamMembership{}, false
}

// Check decides whether the signed-in principal for ep may read teamID's
// content. It never returns Allowed without either a server confirmation in
// this call or a cached one under CacheTTL old for the same token.
func Check(ctx context.Context, ep, teamID string) Verdict {
	ep = endpoint.NormalizeEndpoint(ep)
	v := Verdict{Endpoint: ep, TeamID: teamID}
	if ep == "" || teamID == "" {
		// Nothing to check against: fail closed rather than guess.
		v.Status = Unverified
		v.Detail = "no endpoint or team configured for this repo"
		return v
	}

	tok, status, detail := currentToken(ep)
	if status != Allowed {
		v.Status = status
		v.Detail = detail
		return v
	}

	if cachedAllowed(ep, tok.AccessToken, teamID) {
		v.Status = Allowed
		v.FromCache = true
		return v
	}

	if err := ctx.Err(); err != nil {
		v.Status = Unverified
		v.Detail = "canceled"
		return v
	}

	var confirmed string
	v.Status, v.Detail, confirmed = askServer(ep, tok, teamID)
	switch v.Status {
	case Allowed:
		// Keyed by the token the server actually confirmed: after a 401
		// refresh that is the new one.
		recordAllowed(ep, confirmed, teamID)
	case NoAccess, Expired:
		forget(ep, tok.AccessToken, teamID)
	}
	return v
}

// currentToken loads a usable credential for ep, refreshing it when it is
// about to expire. A refresh that fails for lack of network is Unverified,
// not Expired: the login may be fine, it just could not be renewed.
func currentToken(ep string) (*auth.StoredToken, Status, string) {
	tok, err := auth.EnsureValidTokenForEndpoint(ep, refreshBufferSeconds)
	if err != nil {
		if errors.Is(err, auth.ErrEnvTokenMalformed) {
			return nil, EnvTokenMalformed, ""
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return nil, Unverified, "could not reach the server to renew your sign-in"
		}
		if raw, _ := auth.GetTokenForEndpoint(ep); raw != nil {
			return nil, Expired, ""
		}
		return nil, NotSignedIn, ""
	}
	if tok == nil || tok.AccessToken == "" {
		return nil, NotSignedIn, ""
	}
	return tok, Allowed, ""
}

// askServer confirms membership online. Team service tokens (oxt_) name
// their team through introspection; personal credentials list memberships
// through GET /api/v1/cli/repos.
//
// It returns the access token the answer is about (the refreshed one after a
// 401), so the cache is keyed by the credential that was actually confirmed.
func askServer(ep string, tok *auth.StoredToken, teamID string) (Status, string, string) {
	accessToken := tok.AccessToken
	if strings.HasPrefix(accessToken, auth.TeamTokenPrefix) {
		status, detail := askIntrospect(ep, accessToken, teamID)
		return status, detail, accessToken
	}

	teams, err := fetchMemberships(ep, accessToken)
	if errors.Is(err, api.ErrUnauthorized) {
		// One reactive refresh, like other authenticated commands: a token
		// revoked or rotated server-side since the local expiry check.
		refreshed, rerr := auth.Handle401ErrorForEndpoint(tok, ep)
		if rerr != nil || refreshed == nil || refreshed.AccessToken == "" {
			return Expired, "", accessToken
		}
		accessToken = refreshed.AccessToken
		teams, err = fetchMemberships(ep, accessToken)
		if errors.Is(err, api.ErrUnauthorized) {
			return Expired, "", accessToken
		}
	}
	if err != nil {
		return Unverified, unverifiedReason(err), accessToken
	}
	if _, ok := FindMembership(teams, teamID); ok {
		return Allowed, "", accessToken
	}
	return NoAccess, "", accessToken
}

// fetchMemberships returns the server's membership list. Older servers omit
// the teams array; the list derived from team-context repos is a subset of
// real memberships, which can only err toward refusing.
func fetchMemberships(ep, accessToken string) ([]api.TeamMembership, error) {
	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(accessToken).WithTimeout(membershipTimeout)
	resp, err := client.GetRepos()
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("empty response")
	}
	return resp.TeamMembershipsFromRepos(), nil
}

// askIntrospect confirms a team service token belongs to teamID.
func askIntrospect(ep, accessToken, teamID string) (Status, string) {
	res, err := auth.Introspect(ep, accessToken)
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrTokenRejected):
		return Expired, ""
	case errors.Is(err, auth.ErrEndpointUnreachable):
		return Unverified, "the server could not be reached"
	default:
		return Unverified, "the server could not confirm the token"
	}
	if res != nil && res.Team != nil && res.Team.TeamID == teamID {
		return Allowed, ""
	}
	return NoAccess, ""
}

// unverifiedReason names why a membership lookup failed, without echoing
// server text.
func unverifiedReason(err error) string {
	var urlErr *url.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &urlErr) && urlErr.Timeout():
		return "the server did not answer in time"
	case errors.As(err, &urlErr):
		return "the server could not be reached"
	case errors.Is(err, api.ErrVersionUnsupported):
		return "this ox version is no longer supported; run `ox upgrade`"
	default:
		return "the server did not confirm membership"
	}
}

// --- cache ---

// cacheEntry is one confirmed membership. The token appears only as part of
// the SHA-256 key; Endpoint and TeamID are kept for readability and pruning.
type cacheEntry struct {
	Endpoint  string    `json:"endpoint"`
	TeamID    string    `json:"team_id"`
	CheckedAt time.Time `json:"checked_at"`
}

type cacheFile struct {
	Entries map[string]cacheEntry `json:"entries"`
}

var cacheMu sync.Mutex

// cacheKey scopes an entry to (endpoint, token, team). "\n" cannot occur in
// any of the three.
func cacheKey(ep, token, teamID string) string {
	sum := sha256.Sum256([]byte(ep + "\n" + token + "\n" + teamID))
	return hex.EncodeToString(sum[:])
}

func cachePath(cacheDir string) string {
	return filepath.Join(cacheDir, CacheFileName)
}

func fresh(e cacheEntry, at time.Time) bool {
	age := at.Sub(e.CheckedAt)
	// A future timestamp (clock moved back, or a hand-edited file) is not a
	// confirmation.
	return age >= 0 && age < CacheTTL
}

func loadCache(path string) cacheFile {
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheFile{Entries: map[string]cacheEntry{}}
	}
	var c cacheFile
	if json.Unmarshal(data, &c) != nil || c.Entries == nil {
		return cacheFile{Entries: map[string]cacheEntry{}}
	}
	return c
}

func saveCache(path string, c cacheFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), CacheFileName+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func cachedAllowed(ep, token, teamID string) bool {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	c := loadCache(cachePath(paths.CacheDir()))
	e, ok := c.Entries[cacheKey(ep, token, teamID)]
	return ok && e.Endpoint == ep && e.TeamID == teamID && fresh(e, now())
}

// mutateCache applies fn to the cache and prunes expired entries. Failures
// are ignored: the cache only saves a network call.
func mutateCache(fn func(c *cacheFile)) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	path := cachePath(paths.CacheDir())
	c := loadCache(path)
	fn(&c)
	at := now()
	for k, e := range c.Entries {
		if !fresh(e, at) {
			delete(c.Entries, k)
		}
	}
	_ = saveCache(path, c)
}

func recordAllowed(ep, token, teamID string) {
	mutateCache(func(c *cacheFile) {
		c.Entries[cacheKey(ep, token, teamID)] = cacheEntry{Endpoint: ep, TeamID: teamID, CheckedAt: now().UTC()}
	})
}

func forget(ep, token, teamID string) {
	mutateCache(func(c *cacheFile) {
		delete(c.Entries, cacheKey(ep, token, teamID))
	})
}

// SeedAllowed writes a confirmation into the cache under cacheDir as if the
// server had confirmed it at checkedAt. It exists for hermetic end-to-end
// harnesses that run ox as a subprocess with no server to ask; production
// code never calls it.
func SeedAllowed(cacheDir, ep, token, teamID string, checkedAt time.Time) error {
	ep = endpoint.NormalizeEndpoint(ep)
	cacheMu.Lock()
	defer cacheMu.Unlock()
	path := cachePath(cacheDir)
	c := loadCache(path)
	c.Entries[cacheKey(ep, token, teamID)] = cacheEntry{Endpoint: ep, TeamID: teamID, CheckedAt: checkedAt.UTC()}
	return saveCache(path, c)
}
