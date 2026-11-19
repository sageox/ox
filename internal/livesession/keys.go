package livesession

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/fileutil"
)

// ErrNoKey means no signing key exists for the endpoint yet.
var ErrNoKey = errors.New("no live signing key for this endpoint")

// keyPath is <key dir>/<endpoint slug>.key. The slug is reduced to path-safe
// characters so an odd endpoint can never escape the key directory.
func (c Config) keyPath(ep string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		}
		return '_'
	}, endpoint.SanitizeForPath(ep))
	return filepath.Join(c.KeyDir, slug+".key")
}

// LoadKey reads the endpoint's signing key; it returns ErrNoKey when absent.
func LoadKey(cfg Config, ep string) (ed25519.PrivateKey, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(cfg.keyPath(ep))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoKey
	}
	if err != nil {
		return nil, fmt.Errorf("read live signing key: %w", err)
	}
	seed, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		// never overwrite: a damaged key file is a human decision, not ours.
		return nil, fmt.Errorf("live signing key file %s is damaged; not replacing it", cfg.keyPath(ep))
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// LoadOrCreateKey returns the endpoint's signing key, creating it (0600, under a
// file lock) on first use. The key lives in its own file, not auth.json, because
// token refresh rewrites auth.json wholesale.
func LoadOrCreateKey(ctx context.Context, cfg Config, ep string) (ed25519.PrivateKey, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return nil, err
	}
	if key, err := LoadKey(cfg, ep); !errors.Is(err, ErrNoKey) {
		return key, err
	}
	var key ed25519.PrivateKey
	path := cfg.keyPath(ep)
	err = fileutil.WithFileLock(ctx, path, func() error {
		var err error
		if key, err = LoadKey(cfg, ep); !errors.Is(err, ErrNoKey) {
			return err // another process created it while we waited
		}
		if err := fileutil.EnsurePrivateDir(cfg.KeyDir); err != nil {
			return fmt.Errorf("create live key dir: %w", err)
		}
		_, key, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return fmt.Errorf("generate live signing key: %w", err)
		}
		enc := base64.RawURLEncoding.EncodeToString(key.Seed()) + "\n"
		if err := fileutil.AtomicWriteBytes(path, []byte(enc), 0o600); err != nil {
			return fmt.Errorf("write live signing key: %w", err)
		}
		return nil
	})
	return key, err
}
