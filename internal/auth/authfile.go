package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gofrs/flock"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/fileutil"
)

// authFileHandle provides load/save access to auth.json.
// It can only be obtained through withAuthFileLocked, which holds
// an exclusive flock for the duration of the callback.
// This makes unprotected concurrent access structurally impossible.
type authFileHandle struct {
	authPath string
	// legacyMigrationEndpoint overrides the endpoint used when migrating
	// legacy single-token auth files. If empty, uses endpoint.Get().
	legacyMigrationEndpoint string
	// endpointTracker is the owning AuthClient, used for per-instance
	// disappearance detection. Nil for package-level functions (no tracking).
	endpointTracker *AuthClient
}

const (
	authLockTimeout = 5 * time.Second
	authLockPoll    = 50 * time.Millisecond
)

// authFileOption configures an authFileHandle.
type authFileOption func(*authFileHandle)

// withLegacyMigrationEndpoint sets the endpoint used when migrating
// legacy single-token auth files. Used by AuthClient methods that have
// a client-specific endpoint rather than the global default.
func withLegacyMigrationEndpoint(ep string) authFileOption {
	return func(h *authFileHandle) {
		h.legacyMigrationEndpoint = ep
	}
}

// withEndpointTracker sets the AuthClient used for disappearance detection.
func withEndpointTracker(c *AuthClient) authFileOption {
	return func(h *authFileHandle) {
		h.endpointTracker = c
	}
}

// withAuthFileLocked acquires an exclusive flock on auth.json.lock,
// then calls fn with a handle that provides load/save access.
// The lock is released when fn returns.
func withAuthFileLocked(authPath string, fn func(h *authFileHandle) error, opts ...authFileOption) error {
	dir := filepath.Dir(authPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	fl := flock.New(authPath + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), authLockTimeout)
	defer cancel()

	locked, err := fl.TryLockContext(ctx, authLockPoll)
	if err != nil {
		return fmt.Errorf("acquire auth file lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("auth file lock timeout (5s) — another process may be stuck")
	}
	defer func() { _ = fl.Unlock() }()

	h := &authFileHandle{authPath: authPath}
	for _, opt := range opts {
		opt(h)
	}
	return fn(h)
}

// withAuthFileRLocked acquires a shared (read) flock on auth.json.lock,
// then calls fn with a handle that provides read access.
// Multiple readers can hold the lock concurrently; exclusive with writers.
func withAuthFileRLocked(authPath string, fn func(h *authFileHandle) error, opts ...authFileOption) error {
	dir := filepath.Dir(authPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	fl := flock.New(authPath + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), authLockTimeout)
	defer cancel()

	locked, err := fl.TryRLockContext(ctx, authLockPoll)
	if err != nil {
		return fmt.Errorf("acquire auth file read lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("auth file read lock timeout (5s) — a writer may be stuck")
	}
	defer func() { _ = fl.Unlock() }()

	h := &authFileHandle{authPath: authPath}
	for _, opt := range opts {
		opt(h)
	}
	return fn(h)
}

// load reads and parses the auth store from disk.
// Handles legacy single-token migration and endpoint normalization.
func (h *authFileHandle) load() (*AuthStore, error) {
	store, err := h.loadFromDisk()
	if err != nil {
		return nil, err
	}

	if h.endpointTracker != nil {
		h.endpointTracker.checkKnownEndpoints(store)
	}
	return store, nil
}

// loadFromDisk reads the auth file and handles format detection/migration.
func (h *authFileHandle) loadFromDisk() (*AuthStore, error) {
	data, err := os.ReadFile(h.authPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &AuthStore{Tokens: make(map[string]*StoredToken)}, nil
		}
		return nil, fmt.Errorf("failed to read auth file: %w", err)
	}

	// Distinguish new multi-endpoint format from legacy single-token format by probing
	// for the "tokens" key. This is necessary because both formats produce
	// store.Tokens == nil after a bare json.Unmarshal (legacy has no "tokens" key;
	// new format may have "tokens": null). We must not fall through to legacy
	// migration for new-format files — doing so would overwrite the file with an
	// empty zero-value StoredToken, silently wiping all credentials.
	var probe struct {
		Tokens json.RawMessage `json:"tokens"`
	}
	if json.Unmarshal(data, &probe) == nil && probe.Tokens != nil {
		// "tokens" key is present — this is the new multi-endpoint format.
		var store AuthStore
		if err := json.Unmarshal(data, &store); err != nil {
			return nil, fmt.Errorf("failed to parse auth file: %w", err)
		}
		if store.Tokens == nil {
			store.Tokens = make(map[string]*StoredToken)
		}
		normalizeTokenKeys(&store)
		return &store, nil
	}

	// No "tokens" key — migrate from legacy single-token format.
	var legacyToken StoredToken
	if err := json.Unmarshal(data, &legacyToken); err != nil {
		return nil, fmt.Errorf("failed to parse auth file: %w", err)
	}

	ep := h.legacyMigrationEndpoint
	if ep == "" {
		ep = endpoint.Get()
	}
	store := AuthStore{
		Tokens: map[string]*StoredToken{
			ep: &legacyToken,
		},
	}

	// save migrated format (best effort) — lock is already held
	_ = h.writeFile(&store)

	return &store, nil
}

// save writes the auth store to disk with credential removal detection.
// Before writing, it compares against the current file to detect and log
// any endpoints being removed.
func (h *authFileHandle) save(store *AuthStore) error {
	h.detectRemovedCredentials(store)
	return h.writeFile(store)
}

// writeFile performs the actual atomic write to disk.
func (h *authFileHandle) writeFile(store *AuthStore) error {
	configDir := filepath.Dir(h.authPath)
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	if err := fileutil.AtomicWriteJSON(h.authPath, store, 0600); err != nil {
		return fmt.Errorf("failed to save auth store: %w", err)
	}

	return nil
}

// detectRemovedCredentials reads the current auth.json from disk and warns
// if any endpoints present on disk are missing from the store being saved.
func (h *authFileHandle) detectRemovedCredentials(newStore *AuthStore) {
	existing, err := h.readRaw()
	if err != nil || existing == nil {
		return // no existing file or read error — nothing to compare
	}

	for ep := range existing.Tokens {
		normalized := endpoint.NormalizeEndpoint(ep)
		if _, ok := newStore.Tokens[normalized]; !ok {
			if _, okRaw := newStore.Tokens[ep]; !okRaw {
				slog.Warn("auth: credential removed from store",
					"endpoint", ep,
					"remaining_endpoints", len(newStore.Tokens),
					"caller", callerInfo(4))
			}
		}
	}
}

// readRaw reads the current auth.json without normalization or migration.
// Used for diffing against the store being saved.
func (h *authFileHandle) readRaw() (*AuthStore, error) {
	data, err := os.ReadFile(h.authPath)
	if err != nil {
		return nil, err
	}

	var store AuthStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}

	return &store, nil
}


// callerInfo returns the file:line of the caller at the given skip depth.
func callerInfo(skip int) string {
	_, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "unknown"
	}
	return fmt.Sprintf("%s:%d", filepath.Base(file), line)
}
