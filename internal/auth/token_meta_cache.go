package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/useragent"
)

// tokenMetaCacheTTL is how long /api/v1/auth/me responses stay fresh on disk
// before the next interactive command re-fetches. Cheap enough to refresh
// hourly, long enough to amortize over typical CLI invocation rhythms.
const tokenMetaCacheTTL = 1 * time.Hour

// tokenMetaCacheFile is the file basename inside CacheDir() for the
// serialized cache. The directory is created lazily with 0700 perms.
const tokenMetaCacheFile = "token_meta.json"

// TokenMeta is the subset of /api/v1/auth/me fields ox needs to drive the
// expiry-warning UX. ExpiresAt is a pointer so "never expires" (server
// returns null) round-trips as nil — IsZero on time.Time is ambiguous with
// the zero-time sentinel used elsewhere in storage.go.
type TokenMeta struct {
	// ExpiresAt is the real server-side expiry, or nil for never-expires
	// tokens. NEVER use StoredToken.ExpiresAt for env-supplied tokens — that
	// value is a 24h rolling synthetic stamp (see env_token.go).
	ExpiresAt *time.Time `json:"expires_at"`

	// CreatedAt enables the percentage-of-lifetime threshold calc. Server
	// fills it. Zero when the server does not surface it.
	CreatedAt time.Time `json:"created_at,omitempty"`

	// TokenPrefix is the first few chars of the token (e.g., "oxp_NjkH")
	// for display in warnings. Never the full token.
	TokenPrefix string `json:"token_prefix,omitempty"`

	// Name is the human-friendly label the user gave the token.
	Name string `json:"name,omitempty"`

	// FetchedAt is when this entry was last refreshed from the server.
	FetchedAt time.Time `json:"fetched_at"`
}

// tokenMetaCacheFileFormat is the on-disk JSON shape — a map keyed by a
// hash of the token so multiple tokens for one user can coexist without
// the cache file leaking secrets.
type tokenMetaCacheFileFormat struct {
	Entries map[string]TokenMeta `json:"entries"`
}

// tokenMetaCacheMu guards on-disk reads/writes against the small race
// between concurrent CLI invocations sharing the same file.
var tokenMetaCacheMu sync.Mutex

// tokenHashKey returns the hex-encoded SHA-256 of the token. Used as the
// stable cache key so the on-disk file never contains plaintext tokens.
func tokenHashKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// tokenMetaCachePath returns the on-disk cache file location:
// $XDG_CACHE_HOME/sageox/token_meta.json (or legacy ~/.sageox/cache/...).
func tokenMetaCachePath() string {
	return filepath.Join(paths.CacheDir(), tokenMetaCacheFile)
}

// loadTokenMetaCache reads and parses the cache file. Returns an empty
// (non-nil) struct when the file does not exist — the empty value is a
// valid starting state for a fresh write.
func loadTokenMetaCache() (*tokenMetaCacheFileFormat, error) {
	path := tokenMetaCachePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &tokenMetaCacheFileFormat{Entries: map[string]TokenMeta{}}, nil
		}
		return nil, fmt.Errorf("read token meta cache: %w", err)
	}
	var out tokenMetaCacheFileFormat
	if err := json.Unmarshal(data, &out); err != nil {
		// corrupt file — treat as empty rather than wedge the warning path.
		return &tokenMetaCacheFileFormat{Entries: map[string]TokenMeta{}}, nil
	}
	if out.Entries == nil {
		out.Entries = map[string]TokenMeta{}
	}
	return &out, nil
}

// saveTokenMetaCache writes the cache atomically with 0600 perms. The
// cache file holds non-sensitive metadata (expiry, name, prefix) but we
// keep it user-private anyway — defense in depth and matches the auth
// store convention.
func saveTokenMetaCache(c *tokenMetaCacheFileFormat) error {
	if c == nil {
		return nil
	}
	path := tokenMetaCachePath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal token meta cache: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write tmp token meta cache: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename token meta cache: %w", err)
	}
	// Best-effort perm fix in case umask widened the file.
	_ = os.Chmod(path, 0o600)
	return nil
}

// FetchTokenMetaCached returns token metadata for the given (endpoint,
// token) pair. Cache TTL is tokenMetaCacheTTL; on stale or missing entry
// it fetches fresh from GET <ep>/api/v1/auth/me, persists, and returns.
//
// Returns (nil, nil) — and never an error — when the network call would
// be wasteful or fails non-fatally. The caller is a warning emitter, not
// a critical path; we never want to surface auth-me failures to the user.
// Errors are only returned for genuinely malformed inputs (empty token).
func FetchTokenMetaCached(ctx context.Context, ep, token string) (*TokenMeta, error) {
	if token == "" {
		return nil, fmt.Errorf("empty token")
	}
	ep = endpoint.NormalizeEndpoint(ep)
	key := tokenHashKey(token)

	tokenMetaCacheMu.Lock()
	defer tokenMetaCacheMu.Unlock()

	cache, err := loadTokenMetaCache()
	if err != nil {
		// soft-fail: start with empty cache, try to refresh.
		cache = &tokenMetaCacheFileFormat{Entries: map[string]TokenMeta{}}
	}

	if entry, ok := cache.Entries[key]; ok {
		if time.Since(entry.FetchedAt) < tokenMetaCacheTTL {
			result := entry
			return &result, nil
		}
	}

	fresh, err := fetchTokenMetaFromServer(ctx, ep, token)
	if err != nil {
		// network/server problem — fall back to the stale entry if we
		// have one rather than blocking the caller. The warning is
		// best-effort; we never want to escalate.
		if entry, ok := cache.Entries[key]; ok {
			result := entry
			return &result, nil
		}
		return nil, nil
	}
	fresh.FetchedAt = time.Now().UTC()
	cache.Entries[key] = *fresh
	if writeErr := saveTokenMetaCache(cache); writeErr != nil {
		// non-fatal: return the fresh value even if persistence failed.
		_ = writeErr
	}
	return fresh, nil
}

// authMeResponse is the subset of /api/v1/auth/me used by the warning
// path. Unknown fields are tolerated. expires_at may be null (never-
// expires) or an RFC3339 string.
type authMeResponse struct {
	ExpiresAt   *time.Time `json:"expires_at"`
	CreatedAt   time.Time  `json:"created_at"`
	TokenPrefix string     `json:"token_prefix"`
	Name        string     `json:"name"`
}

// fetchTokenMetaFromServer GETs /api/v1/auth/me and maps the response
// to TokenMeta. Network/parse failures surface as errors; callers
// downgrade to a soft no-op.
func fetchTokenMetaFromServer(ctx context.Context, ep, token string) (*TokenMeta, error) {
	u := strings.TrimRight(ep, "/") + "/api/v1/auth/me"
	req, err := useragent.NewRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth/me: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var parsed authMeResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	return &TokenMeta{
		ExpiresAt:   parsed.ExpiresAt,
		CreatedAt:   parsed.CreatedAt,
		TokenPrefix: parsed.TokenPrefix,
		Name:        parsed.Name,
	}, nil
}
