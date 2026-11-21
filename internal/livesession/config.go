package livesession

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/sessionstream"
)

// ErrNotLoggedIn means no login token exists for the endpoint.
var ErrNotLoggedIn = errors.New("not logged in to the endpoint")

// tokenRefreshBufferSeconds refreshes a token that would expire mid-drain.
const tokenRefreshBufferSeconds = 60

// requestTimeout bounds one HTTP request; the caller's budget bounds the whole call.
const requestTimeout = 10 * time.Second

// Config carries everything a call needs from its environment. Only Endpoint is
// required; the rest default to the real login, config dirs and slog.
type Config struct {
	// Endpoint is the SageOx base URL the stream goes to (normalized by the caller).
	Endpoint string
	// HTTP is the client for API calls; nil uses a 10 s per-request timeout.
	HTTP *http.Client
	// Token returns the bearer token for an endpoint; nil uses the stored login
	// (auth.EnsureValidTokenForEndpointContext).
	Token func(ctx context.Context, endpoint string) (string, error)
	// KeyDir holds signing keys; empty means <config dir>/live-keys.
	KeyDir string
	// DataDir holds the pending-deletes queue; empty means the ox data dir.
	DataDir string
	// Logger receives single-line key=value events; nil means slog.Default.
	Logger *slog.Logger
	// AcceptsNotes reports, at each open, whether this machine takes teammate
	// notes (session.teammate_notes for the repo). nil means it does. Read per
	// open rather than once, so `ox config set` reaches the next layer opened
	// without a daemon restart.
	AcceptsNotes func() bool
}

func (c Config) withDefaults() (Config, error) {
	if c.Endpoint == "" {
		return c, errors.New("live session config: endpoint is required")
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: requestTimeout}
	}
	if c.Token == nil {
		c.Token = storedLoginToken
	}
	if c.KeyDir == "" {
		c.KeyDir = filepath.Join(paths.ConfigDir(), "live-keys")
	}
	if c.DataDir == "" {
		c.DataDir = paths.DataDir()
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c, nil
}

// acceptsNotes reads the teammate-notes switch; unset means notes are accepted.
func (c Config) acceptsNotes() bool { return c.AcceptsNotes == nil || c.AcceptsNotes() }

func storedLoginToken(ctx context.Context, endpoint string) (string, error) {
	tok, err := auth.EnsureValidTokenForEndpointContext(ctx, endpoint, tokenRefreshBufferSeconds)
	if err != nil {
		return "", fmt.Errorf("login token: %w", err)
	}
	if tok == nil {
		return "", ErrNotLoggedIn
	}
	return tok.AccessToken, nil
}

// newClient builds a signed client for endpoint; it never creates a key.
func (c Config) newClient(ctx context.Context, endpoint string) (*sessionstream.Client, error) {
	key, err := LoadKey(c, endpoint)
	if err != nil {
		return nil, err
	}
	return c.clientWithKey(ctx, endpoint, key)
}

func (c Config) clientWithKey(ctx context.Context, endpoint string, key ed25519.PrivateKey) (*sessionstream.Client, error) {
	token, err := c.Token(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return &sessionstream.Client{BaseURL: endpoint, Token: token, Key: key, HTTP: c.HTTP}, nil
}

// withLock runs fn holding the session's live.lock, waiting at most wait. The
// lock file itself lives where fileutil keeps advisory locks (OS tmpdir, keyed
// by this path) so it never lands in a git-tracked session folder.
func withLock(ctx context.Context, dir string, wait time.Duration, fn func() error) error {
	return fileutil.WithFileLockTimeout(ctx, LockTarget(dir), wait, fn)
}

// LockTarget is the path live.lock is keyed by for the session folder dir. The
// key is a hash of the path's spelling, so it uses the folder's resolved path
// (canonicalDir): a hook naming the folder through a symlinked cache and the
// daemon naming it directly must exclude each other, or a capture appends
// while a drain rewrites the spool and the entry is lost with no gap line.
func LockTarget(dir string) string {
	return filepath.Join(canonicalDir(dir), lockFileName)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
