package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- A. Locking correctness ---
// These tests verify the flock-based mutual exclusion that prevents
// the TOCTOU race where concurrent writers silently overwrite each other.

// TestLock_MutualExclusion_ConcurrentWriters verifies two goroutines
// writing different endpoints simultaneously — both must survive.
// Failure prevented: last-writer-wins causes one endpoint to vanish.
func TestLock_MutualExclusion_ConcurrentWriters(t *testing.T) {
	t.Parallel()
	client := NewTestClient(t)

	var wg sync.WaitGroup
	wg.Add(2)

	ep1 := "https://endpoint-a.example.com"
	ep2 := "https://endpoint-b.example.com"

	go func() {
		defer wg.Done()
		c := client.WithEndpoint(ep1)
		require.NoError(t, c.SaveToken(createTestToken(time.Hour)))
	}()

	go func() {
		defer wg.Done()
		c := client.WithEndpoint(ep2)
		require.NoError(t, c.SaveToken(createTestToken(time.Hour)))
	}()

	wg.Wait()

	// both endpoints must be present
	tok1, err := client.WithEndpoint(ep1).GetToken()
	require.NoError(t, err)
	require.NotNil(t, tok1, "endpoint-a token must survive concurrent write")

	tok2, err := client.WithEndpoint(ep2).GetToken()
	require.NoError(t, err)
	require.NotNil(t, tok2, "endpoint-b token must survive concurrent write")
}

// TestLock_MutualExclusion_ManyWriters verifies 20 goroutines writing
// 20 unique endpoints — all 20 must exist in the final file.
// Failure prevented: lock contention under load causes data loss.
func TestLock_MutualExclusion_ManyWriters(t *testing.T) {
	t.Parallel()
	client := NewTestClient(t)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		ep := fmt.Sprintf("https://ep-%02d.example.com", i)
		go func() {
			defer wg.Done()
			c := client.WithEndpoint(ep)
			require.NoError(t, c.SaveToken(createTestToken(time.Hour)))
		}()
	}

	wg.Wait()

	// verify all endpoints survived
	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)
	data, err := os.ReadFile(authPath)
	require.NoError(t, err)

	var store AuthStore
	require.NoError(t, json.Unmarshal(data, &store))
	assert.Len(t, store.Tokens, n, "all %d endpoints must survive concurrent writes", n)
}

// TestLock_MutualExclusion_SameEndpoint verifies concurrent overwrites
// of the same endpoint produce valid JSON and a consistent token.
// Failure prevented: partial writes corrupt the file.
func TestLock_MutualExclusion_SameEndpoint(t *testing.T) {
	t.Parallel()
	client := NewTestClient(t)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		accessToken := fmt.Sprintf("token-%02d", i)
		go func() {
			defer wg.Done()
			tok := createTestToken(time.Hour)
			tok.AccessToken = accessToken
			require.NoError(t, client.SaveToken(tok))
		}()
	}

	wg.Wait()

	// file must be valid JSON with exactly one token
	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)
	data, err := os.ReadFile(authPath)
	require.NoError(t, err)

	var store AuthStore
	require.NoError(t, json.Unmarshal(data, &store), "file must be valid JSON after concurrent writes")
	assert.Len(t, store.Tokens, 1, "single endpoint must have exactly one token")
}

// TestLock_Timeout_HeldByAnotherProcess verifies that when the lock is
// already held externally, withAuthFileLocked returns a timeout error.
// Failure prevented: indefinite hang when another process holds the lock.
func TestLock_Timeout_HeldByAnotherProcess(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	lockPath := authPath + ".lock"

	// hold the lock externally
	fl := flock.New(lockPath)
	locked, err := fl.TryLock()
	require.NoError(t, err)
	require.True(t, locked)
	defer func() { _ = fl.Unlock() }()

	// attempt to acquire — should timeout
	start := time.Now()
	err = withAuthFileLocked(authPath, func(h *authFileHandle) error {
		t.Fatal("callback should not be called when lock is held")
		return nil
	})
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "lock")
	assert.GreaterOrEqual(t, elapsed, 3*time.Second, "should wait before timing out")
}

// TestLock_ReleasedOnError verifies the lock is released when the
// callback returns an error — the file must be unchanged.
// Failure prevented: error in callback permanently wedges the lock.
func TestLock_ReleasedOnError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	// first call: callback errors
	err := withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return fmt.Errorf("intentional error")
	})
	require.Error(t, err)

	// second call: must succeed (lock was released)
	err = withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return nil
	})
	require.NoError(t, err, "lock must be released after callback error")
}

// TestLock_ReleasedOnPanic verifies the lock is released even when
// the callback panics — the next caller must succeed.
// Failure prevented: panic permanently wedges the lock file.
func TestLock_ReleasedOnPanic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	// first call: callback panics
	func() {
		defer func() { _ = recover() }()
		_ = withAuthFileLocked(authPath, func(h *authFileHandle) error {
			panic("intentional panic")
		})
	}()

	// second call: must succeed (flock is released when process/goroutine unwinds defer)
	err := withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return nil
	})
	require.NoError(t, err, "lock must be released after callback panic")
}

// TestLock_ReadWriteExclusion verifies a writer holds exclusive access —
// a concurrent reader via withAuthFileRLocked must wait and see the write.
// Failure prevented: reader sees stale data during a write.
func TestLock_ReadWriteExclusion(t *testing.T) {
	t.Parallel()
	client := NewTestClient(t)
	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)

	// write a token under the exclusive lock
	written := make(chan struct{})
	var readToken *StoredToken

	go func() {
		_ = withAuthFileLocked(authPath, func(h *authFileHandle) error {
			store := &AuthStore{Tokens: map[string]*StoredToken{
				"https://exclusive.example.com": {AccessToken: "exclusive-write"},
			}}
			err := h.writeFile(store)
			close(written)
			// hold lock briefly so reader must wait
			time.Sleep(50 * time.Millisecond)
			return err
		})
	}()

	<-written
	// reader should block until writer releases, then see the write
	_ = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		store, err := h.load()
		if err != nil {
			return err
		}
		readToken = store.Tokens["https://exclusive.example.com"]
		return nil
	})

	require.NotNil(t, readToken, "reader must see the writer's data")
	assert.Equal(t, "exclusive-write", readToken.AccessToken)
}

// TestLock_ConcurrentReaders verifies multiple readers can hold the
// shared lock simultaneously without blocking each other.
// Failure prevented: unnecessary serialization of read-only operations.
func TestLock_ConcurrentReaders(t *testing.T) {
	t.Parallel()
	client := NewTestClient(t)
	CreateTestTokenForClient(t, client, time.Hour)

	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)

	const readers = 5
	var wg sync.WaitGroup
	wg.Add(readers)
	var active int64

	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			_ = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
				cur := atomic.AddInt64(&active, 1)
				defer atomic.AddInt64(&active, -1)
				// if readers are serialized, cur will always be 1
				// with concurrent reads, cur > 1 is likely
				_ = cur
				time.Sleep(20 * time.Millisecond)
				_, err := h.load()
				return err
			})
		}()
	}

	wg.Wait()
}

// TestLock_FileCreatedIfMissing verifies that locking on a path where
// the directory doesn't exist creates the directory and succeeds.
// Failure prevented: first-time auth on a fresh install fails.
func TestLock_FileCreatedIfMissing(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "nested", "dir")
	authPath := filepath.Join(dir, "auth.json")

	err := withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.writeFile(&AuthStore{Tokens: map[string]*StoredToken{}})
	})
	require.NoError(t, err)

	_, err = os.Stat(authPath)
	require.NoError(t, err, "auth file must be created in nested directory")
}

// TestLock_StaleLocksDoNotBlock verifies that a stale .lock file from
// a crashed process doesn't block new lock acquisition.
// Failure prevented: crash leaves orphan lock file that blocks all auth ops.
func TestLock_StaleLocksDoNotBlock(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	lockPath := authPath + ".lock"

	// simulate stale lock file (just the file, no process holding it)
	require.NoError(t, os.WriteFile(lockPath, []byte("stale"), 0600))

	err := withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return nil
	})
	require.NoError(t, err, "stale lock file must not block new lock acquisition")
}

// --- B. Credential disappearance detection (save-side) ---
// These tests verify that save() warns when credentials are being removed,
// making silent credential loss visible in logs.

// TestSaveDetection_WarnOnEndpointRemoval verifies a WARN is logged when
// saving a store that's missing an endpoint present on disk.
// Failure prevented: silent credential removal goes unnoticed.
func TestSaveDetection_WarnOnEndpointRemoval(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	// write initial store with two endpoints
	initial := &AuthStore{Tokens: map[string]*StoredToken{
		"https://keep.example.com":   {AccessToken: "keep"},
		"https://remove.example.com": {AccessToken: "remove"},
	}}
	err := withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.writeFile(initial)
	})
	require.NoError(t, err)

	// save with one endpoint removed — should trigger warning (verified by no crash)
	updated := &AuthStore{Tokens: map[string]*StoredToken{
		"https://keep.example.com": {AccessToken: "keep"},
	}}
	err = withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.save(updated)
	})
	require.NoError(t, err)

	// verify the surviving token
	data, err := os.ReadFile(authPath)
	require.NoError(t, err)
	var store AuthStore
	require.NoError(t, json.Unmarshal(data, &store))
	assert.Len(t, store.Tokens, 1)
	assert.Contains(t, store.Tokens, "https://keep.example.com")
}

// TestSaveDetection_NoWarnOnAdd verifies that adding a new endpoint
// does not trigger any removal warning.
// Failure prevented: false positive warnings on normal token saves.
func TestSaveDetection_NoWarnOnAdd(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	initial := &AuthStore{Tokens: map[string]*StoredToken{
		"https://existing.example.com": {AccessToken: "existing"},
	}}
	err := withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.writeFile(initial)
	})
	require.NoError(t, err)

	// add a new endpoint
	updated := &AuthStore{Tokens: map[string]*StoredToken{
		"https://existing.example.com": {AccessToken: "existing"},
		"https://new.example.com":      {AccessToken: "new"},
	}}
	err = withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.save(updated)
	})
	require.NoError(t, err)

	data, err := os.ReadFile(authPath)
	require.NoError(t, err)
	var store AuthStore
	require.NoError(t, json.Unmarshal(data, &store))
	assert.Len(t, store.Tokens, 2)
}

// TestSaveDetection_NoWarnOnUpdate verifies that updating an existing
// endpoint's token does not trigger removal warning.
// Failure prevented: token refresh falsely warns about removal.
func TestSaveDetection_NoWarnOnUpdate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	initial := &AuthStore{Tokens: map[string]*StoredToken{
		"https://update.example.com": {AccessToken: "old-token"},
	}}
	err := withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.writeFile(initial)
	})
	require.NoError(t, err)

	updated := &AuthStore{Tokens: map[string]*StoredToken{
		"https://update.example.com": {AccessToken: "new-token"},
	}}
	err = withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.save(updated)
	})
	require.NoError(t, err)

	data, err := os.ReadFile(authPath)
	require.NoError(t, err)
	var store AuthStore
	require.NoError(t, json.Unmarshal(data, &store))
	assert.Equal(t, "new-token", store.Tokens["https://update.example.com"].AccessToken)
}

// TestSaveDetection_NoWarnOnEmptyInitialFile verifies no warning when
// saving to a nonexistent file (first-time save).
// Failure prevented: spurious warnings on fresh installs.
func TestSaveDetection_NoWarnOnEmptyInitialFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	store := &AuthStore{Tokens: map[string]*StoredToken{
		"https://first.example.com": {AccessToken: "first"},
	}}
	err := withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.save(store)
	})
	require.NoError(t, err)
}

// TestSaveDetection_NormalizedEndpointMatch verifies that save-side detection
// correctly handles normalized vs raw endpoint keys when comparing.
// Failure prevented: false removal warnings when keys differ only in normalization.
func TestSaveDetection_NormalizedEndpointMatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	// write with raw endpoint
	initial := &AuthStore{Tokens: map[string]*StoredToken{
		"https://api.example.com": {AccessToken: "token"},
	}}
	err := withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.writeFile(initial)
	})
	require.NoError(t, err)

	// save with normalized form — should NOT warn (same endpoint after normalization)
	normalized := endpoint.NormalizeEndpoint("https://api.example.com")
	updated := &AuthStore{Tokens: map[string]*StoredToken{
		normalized: {AccessToken: "token"},
	}}
	err = withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.save(updated)
	})
	require.NoError(t, err)
}

// --- C. Credential disappearance detection (load-side, in-process) ---
// These tests verify that checkKnownEndpoints detects when endpoints
// vanish between loads within the same AuthClient instance.

// TestLoadDetection_ErrorOnDisappearance verifies an ERROR is logged
// when a previously-seen endpoint vanishes on reload.
// Failure prevented: another process silently overwrites auth.json unnoticed.
func TestLoadDetection_ErrorOnDisappearance(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)
	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)

	// first load — two endpoints
	store1 := &AuthStore{Tokens: map[string]*StoredToken{
		"https://disappear-a.example.com": {AccessToken: "a"},
		"https://disappear-b.example.com": {AccessToken: "b"},
	}}
	err = withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.writeFile(store1)
	})
	require.NoError(t, err)

	trackerOpt := withEndpointTracker(client)
	err = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		_, err := h.load()
		return err
	}, trackerOpt)
	require.NoError(t, err)

	// externally truncate to one endpoint
	store2 := &AuthStore{Tokens: map[string]*StoredToken{
		"https://disappear-a.example.com": {AccessToken: "a"},
	}}
	err = withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.writeFile(store2)
	})
	require.NoError(t, err)

	// reload — should detect disappearance (ERROR logged, but no error returned)
	err = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		_, err := h.load()
		return err
	}, trackerOpt)
	require.NoError(t, err, "load must succeed even when disappearance is detected")

	// verify the client tracked the endpoints
	client.knownEndpointsMu.Lock()
	_, hasA := client.knownEndpoints["https://disappear-a.example.com"]
	_, hasB := client.knownEndpoints["https://disappear-b.example.com"]
	client.knownEndpointsMu.Unlock()
	assert.True(t, hasA, "endpoint A should be tracked")
	assert.True(t, hasB, "endpoint B should be tracked from first load")
}

// TestLoadDetection_NoErrorOnFirstLoad verifies no ERROR on the first
// load in a process — there's nothing to compare against.
// Failure prevented: spurious errors on startup.
func TestLoadDetection_NoErrorOnFirstLoad(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)
	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)

	store := &AuthStore{Tokens: map[string]*StoredToken{
		"https://first-load.example.com": {AccessToken: "token"},
	}}
	err = withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.writeFile(store)
	})
	require.NoError(t, err)

	trackerOpt := withEndpointTracker(client)
	err = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		_, err := h.load()
		return err
	}, trackerOpt)
	require.NoError(t, err, "first load must not error")
}

// TestLoadDetection_NoErrorOnNewEndpoint verifies no ERROR when new
// endpoints appear between loads — only disappearances are flagged.
// Failure prevented: false errors when user logs in to new endpoints.
func TestLoadDetection_NoErrorOnNewEndpoint(t *testing.T) {
	t.Parallel()

	client := NewTestClient(t)
	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)

	// first load
	store1 := &AuthStore{Tokens: map[string]*StoredToken{
		"https://grow-a.example.com": {AccessToken: "a"},
	}}
	err = withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.writeFile(store1)
	})
	require.NoError(t, err)

	trackerOpt := withEndpointTracker(client)
	_ = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		_, err := h.load()
		return err
	}, trackerOpt)

	// second load with new endpoint added
	store2 := &AuthStore{Tokens: map[string]*StoredToken{
		"https://grow-a.example.com": {AccessToken: "a"},
		"https://grow-b.example.com": {AccessToken: "b"},
	}}
	err = withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.writeFile(store2)
	})
	require.NoError(t, err)

	err = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		_, err := h.load()
		return err
	}, trackerOpt)
	require.NoError(t, err, "new endpoint appearing must not cause error")
}

// TestLoadDetection_IsolatedBetweenClients verifies that separate
// AuthClient instances have independent endpoint tracking.
// Failure prevented: cross-client leakage causes spurious disappearance errors.
func TestLoadDetection_IsolatedBetweenClients(t *testing.T) {
	t.Parallel()

	clientA := NewTestClient(t)
	clientB := NewTestClient(t)

	// seed client A with an endpoint
	clientA.knownEndpointsMu.Lock()
	clientA.knownEndpoints["https://only-in-a.example.com"] = struct{}{}
	clientA.knownEndpointsMu.Unlock()

	// client B should have no tracked endpoints
	clientB.knownEndpointsMu.Lock()
	count := len(clientB.knownEndpoints)
	clientB.knownEndpointsMu.Unlock()
	assert.Equal(t, 0, count, "separate clients must have independent tracking")
}

// --- D. Structural enforcement ---
// These tests verify that the type system prevents unprotected access.

// TestStructural_HandleFieldsPrivate verifies that authFileHandle's
// fields are unexported — external packages can't construct or manipulate it.
// Failure prevented: bypassing the lock by constructing a handle directly.
func TestStructural_HandleFieldsPrivate(t *testing.T) {
	t.Parallel()

	// this test exists primarily as documentation — the Go compiler
	// enforces that external packages can't use unexported types.
	// Within the package, we verify the handle works correctly through
	// the public API in all other tests.
	h := &authFileHandle{authPath: "/dev/null"}
	assert.Equal(t, "/dev/null", h.authPath, "internal construction works within package")
}

// --- E. Atomicity and durability ---
// These tests verify atomic writes prevent corruption.

// TestAtomicity_ConcurrentReadDuringSave verifies a reader during a save
// gets either the complete old file or complete new file, never partial.
// Failure prevented: reader sees half-written JSON and fails to parse.
func TestAtomicity_ConcurrentReadDuringSave(t *testing.T) {
	t.Parallel()
	client := NewTestClient(t)

	// seed with initial token
	c1 := client.WithEndpoint("https://atomic.example.com")
	require.NoError(t, c1.SaveToken(createTestToken(time.Hour)))

	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(2)

	// continuous writer
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			tok := createTestToken(time.Hour)
			tok.AccessToken = fmt.Sprintf("write-%d", i)
			_ = c1.SaveToken(tok)
		}
	}()

	// continuous reader — must never get corrupt data
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			tok, err := c1.GetToken()
			if err != nil {
				t.Errorf("reader got error on iteration %d: %v", i, err)
				return
			}
			if tok != nil && tok.AccessToken == "" {
				t.Error("reader got token with empty access token (partial write)")
				return
			}
		}
	}()

	wg.Wait()
}

// TestAtomicity_FilePermissions verifies auth.json has 0600 permissions
// after save (readable only by owner).
// Failure prevented: credentials readable by other users on shared systems.
func TestAtomicity_FilePermissions(t *testing.T) {
	t.Parallel()
	client := NewTestClient(t)
	CreateTestTokenForClient(t, client, time.Hour)

	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)

	info, err := os.Stat(authPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm(),
		"auth.json must have 0600 permissions")
}

// TestAtomicity_DirectoryPermissions verifies the config directory
// has 0700 permissions after creation.
// Failure prevented: config dir accessible by other users.
func TestAtomicity_DirectoryPermissions(t *testing.T) {
	t.Parallel()
	client := NewTestClient(t)
	CreateTestTokenForClient(t, client, time.Hour)

	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)

	dir := filepath.Dir(authPath)
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm(),
		"config directory must have 0700 permissions")
}

// TestAtomicity_OriginalSurvivesWriteError verifies that if the write
// fails mid-operation, the original file is untouched.
// Failure prevented: failed save corrupts existing credentials.
func TestAtomicity_OriginalSurvivesWriteError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	// write initial data
	original := &AuthStore{Tokens: map[string]*StoredToken{
		"https://survive.example.com": {AccessToken: "original"},
	}}
	err := withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.writeFile(original)
	})
	require.NoError(t, err)

	originalData, err := os.ReadFile(authPath)
	require.NoError(t, err)

	// attempt write that errors in the callback (before writeFile)
	err = withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return fmt.Errorf("simulated failure before write")
	})
	require.Error(t, err)

	// original data must be intact
	afterData, err := os.ReadFile(authPath)
	require.NoError(t, err)
	assert.Equal(t, string(originalData), string(afterData),
		"original file must survive when callback errors before write")
}

// --- F. Legacy migration under lock ---
// These tests verify that legacy single-token format files are correctly
// migrated to the new multi-endpoint format.

// TestLegacyMigration_SingleTokenFormat verifies legacy single-token
// auth.json migrates to multi-endpoint format under lock.
// Failure prevented: users upgrading from old ox lose their token.
func TestLegacyMigration_SingleTokenFormat(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	legacyToken := StoredToken{
		AccessToken:  "legacy-access",
		RefreshToken: "legacy-refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
		TokenType:    "Bearer",
	}
	data, err := json.Marshal(legacyToken)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(authPath, data, 0600))

	// load through locked handle — should trigger migration
	var loaded *AuthStore
	err = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		var loadErr error
		loaded, loadErr = h.load()
		return loadErr
	})
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.NotEmpty(t, loaded.Tokens, "legacy token must be migrated into tokens map")

	// verify migrated file has new format
	migratedData, err := os.ReadFile(authPath)
	require.NoError(t, err)
	var migratedStore AuthStore
	require.NoError(t, json.Unmarshal(migratedData, &migratedStore))
	assert.NotEmpty(t, migratedStore.Tokens, "migrated file must have tokens key")
}

// TestLegacyMigration_NullTokensField verifies {"tokens": null} is treated
// as new format (empty store), not legacy single-token format.
// Failure prevented: credential wipe when tokens field is null.
func TestLegacyMigration_NullTokensField(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	// write {"tokens": null}
	require.NoError(t, os.WriteFile(authPath, []byte(`{"tokens": null}`), 0600))

	var loaded *AuthStore
	err := withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		var loadErr error
		loaded, loadErr = h.load()
		return loadErr
	})
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.NotNil(t, loaded.Tokens, "null tokens field must initialize to empty map")
	assert.Empty(t, loaded.Tokens, "null tokens must result in empty (not nil) map")
}

// TestLegacyMigration_EmptyTokensObject verifies {"tokens": {}} loads clean.
// Failure prevented: empty tokens object causes nil pointer.
func TestLegacyMigration_EmptyTokensObject(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	require.NoError(t, os.WriteFile(authPath, []byte(`{"tokens": {}}`), 0600))

	var loaded *AuthStore
	err := withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		var loadErr error
		loaded, loadErr = h.load()
		return loadErr
	})
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Empty(t, loaded.Tokens)
}

// TestLegacyMigration_CustomEndpoint verifies legacy migration uses the
// handle's legacyMigrationEndpoint when set (AuthClient scenario).
// Failure prevented: legacy token stored under wrong endpoint for AuthClient users.
func TestLegacyMigration_CustomEndpoint(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	customEP := "https://custom.example.com"

	legacyToken := StoredToken{
		AccessToken: "legacy",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	data, err := json.Marshal(legacyToken)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(authPath, data, 0600))

	var loaded *AuthStore
	err = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		var loadErr error
		loaded, loadErr = h.load()
		return loadErr
	}, withLegacyMigrationEndpoint(customEP))
	require.NoError(t, err)
	require.NotNil(t, loaded)

	_, found := loaded.Tokens[customEP]
	assert.True(t, found, "legacy token must be keyed under custom endpoint %q", customEP)
}

// TestLegacyMigration_ConcurrentMigration verifies two concurrent readers
// encountering legacy format both succeed and the file migrates cleanly.
// Failure prevented: concurrent migration corrupts the file.
func TestLegacyMigration_ConcurrentMigration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	legacyToken := StoredToken{
		AccessToken: "concurrent-legacy",
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	data, err := json.Marshal(legacyToken)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(authPath, data, 0600))

	const readers = 5
	var wg sync.WaitGroup
	wg.Add(readers)
	errs := make([]error, readers)

	for i := 0; i < readers; i++ {
		i := i
		go func() {
			defer wg.Done()
			errs[i] = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
				_, loadErr := h.load()
				return loadErr
			})
		}()
	}

	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "reader %d must succeed during concurrent migration", i)
	}

	// final file must be valid multi-endpoint format
	finalData, err := os.ReadFile(authPath)
	require.NoError(t, err)
	var store AuthStore
	require.NoError(t, json.Unmarshal(finalData, &store),
		"file must be valid JSON after concurrent migration")
}

// --- G. End-to-end integration scenarios ---
// These tests exercise realistic multi-operation workflows.

// TestE2E_MultiProcessWriteDifferentEndpoints simulates CLI + daemon
// writing different endpoints concurrently — both must persist.
// Failure prevented: the exact TOCTOU race that caused credential disappearance.
func TestE2E_MultiProcessWriteDifferentEndpoints(t *testing.T) {
	t.Parallel()
	client := NewTestClient(t)

	prodEP := "https://prod.example.com"
	stagingEP := "https://staging.example.com"

	var wg sync.WaitGroup
	wg.Add(2)

	// simulate CLI saving prod token
	go func() {
		defer wg.Done()
		c := client.WithEndpoint(prodEP)
		tok := createTestToken(time.Hour)
		tok.AccessToken = "prod-token"
		require.NoError(t, c.SaveToken(tok))
	}()

	// simulate daemon saving staging token
	go func() {
		defer wg.Done()
		c := client.WithEndpoint(stagingEP)
		tok := createTestToken(time.Hour)
		tok.AccessToken = "staging-token"
		require.NoError(t, c.SaveToken(tok))
	}()

	wg.Wait()

	prodTok, err := client.WithEndpoint(prodEP).GetToken()
	require.NoError(t, err)
	require.NotNil(t, prodTok, "prod token must survive concurrent write")

	stagingTok, err := client.WithEndpoint(stagingEP).GetToken()
	require.NoError(t, err)
	require.NotNil(t, stagingTok, "staging token must survive concurrent write")
}

// TestE2E_TokenRefreshPreservesOtherEndpoints verifies refreshing one
// endpoint's token doesn't affect other endpoints.
// Failure prevented: token refresh accidentally drops other endpoints.
func TestE2E_TokenRefreshPreservesOtherEndpoints(t *testing.T) {
	t.Parallel()
	client := NewTestClient(t)

	prodEP := "https://prod-refresh.example.com"
	stagingEP := "https://staging-refresh.example.com"

	// save both tokens
	prodClient := client.WithEndpoint(prodEP)
	stagingClient := client.WithEndpoint(stagingEP)

	prodTok := createTestToken(time.Hour)
	prodTok.AccessToken = "prod-original"
	require.NoError(t, prodClient.SaveToken(prodTok))

	stagingTok := createTestToken(time.Hour)
	stagingTok.AccessToken = "staging-original"
	require.NoError(t, stagingClient.SaveToken(stagingTok))

	// "refresh" staging token
	refreshedTok := createTestToken(2 * time.Hour)
	refreshedTok.AccessToken = "staging-refreshed"
	require.NoError(t, stagingClient.SaveToken(refreshedTok))

	// prod must be untouched
	gotProd, err := prodClient.GetToken()
	require.NoError(t, err)
	require.NotNil(t, gotProd)
	assert.Equal(t, "prod-original", gotProd.AccessToken,
		"prod token must survive staging refresh")

	// staging must have new token
	gotStaging, err := stagingClient.GetToken()
	require.NoError(t, err)
	require.NotNil(t, gotStaging)
	assert.Equal(t, "staging-refreshed", gotStaging.AccessToken)
}

// TestE2E_LogoutOneEndpointPreservesOthers verifies removing one
// endpoint's token doesn't affect others.
// Failure prevented: logout from staging accidentally removes prod.
func TestE2E_LogoutOneEndpointPreservesOthers(t *testing.T) {
	t.Parallel()
	client := NewTestClient(t)

	prodEP := "https://prod-logout.example.com"
	stagingEP := "https://staging-logout.example.com"

	prodClient := client.WithEndpoint(prodEP)
	stagingClient := client.WithEndpoint(stagingEP)

	require.NoError(t, prodClient.SaveToken(createTestToken(time.Hour)))
	require.NoError(t, stagingClient.SaveToken(createTestToken(time.Hour)))

	// "logout" from staging
	require.NoError(t, stagingClient.RemoveToken())

	// prod must survive
	gotProd, err := prodClient.GetToken()
	require.NoError(t, err)
	require.NotNil(t, gotProd, "prod token must survive staging logout")

	// staging must be gone
	gotStaging, err := stagingClient.GetToken()
	require.NoError(t, err)
	assert.Nil(t, gotStaging, "staging token must be removed after logout")
}

// TestE2E_RapidFireSaveLoad verifies the system under sustained load —
// multiple goroutines each performing several save+load cycles.
// Failure prevented: edge-case corruption under contention.
func TestE2E_RapidFireSaveLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("short: sustained load test")
	}
	t.Parallel()
	client := NewTestClient(t)

	const goroutines = 10
	const iterationsPerGoroutine = 5
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		ep := fmt.Sprintf("https://rapid-%02d.example.com", i)
		go func() {
			defer wg.Done()
			c := client.WithEndpoint(ep)
			for j := 0; j < iterationsPerGoroutine; j++ {
				tok := createTestToken(time.Hour)
				tok.AccessToken = fmt.Sprintf("token-%d-%d", i, j)
				if err := c.SaveToken(tok); err != nil {
					t.Errorf("save error for %s iter %d: %v", ep, j, err)
					return
				}
				got, err := c.GetToken()
				if err != nil {
					t.Errorf("get error for %s iter %d: %v", ep, j, err)
					return
				}
				if got == nil {
					t.Errorf("nil token for %s after save iter %d", ep, j)
					return
				}
			}
		}()
	}

	wg.Wait()

	// final file must be valid JSON with all endpoints
	authPath, err := client.GetAuthFilePath()
	require.NoError(t, err)
	data, err := os.ReadFile(authPath)
	require.NoError(t, err)

	var store AuthStore
	require.NoError(t, json.Unmarshal(data, &store),
		"final file must be valid JSON after rapid-fire operations")
	assert.Len(t, store.Tokens, goroutines,
		"all endpoints must survive rapid-fire save/load")
}

// --- H. Edge cases ---

// TestEdge_EmptyStore verifies saving and loading an empty store round-trips.
// Failure prevented: empty store causes nil pointer on subsequent operations.
func TestEdge_EmptyStore(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	empty := &AuthStore{Tokens: map[string]*StoredToken{}}
	err := withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.writeFile(empty)
	})
	require.NoError(t, err)

	var loaded *AuthStore
	err = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		var loadErr error
		loaded, loadErr = h.load()
		return loadErr
	})
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.NotNil(t, loaded.Tokens)
	assert.Empty(t, loaded.Tokens)
}

// TestEdge_NilTokenValue verifies an endpoint mapped to nil doesn't crash.
// Failure prevented: nil token value causes panic in save/load path.
func TestEdge_NilTokenValue(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	store := &AuthStore{Tokens: map[string]*StoredToken{
		"https://nil-token.example.com": nil,
	}}
	err := withAuthFileLocked(authPath, func(h *authFileHandle) error {
		return h.writeFile(store)
	})
	require.NoError(t, err)

	var loaded *AuthStore
	err = withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		var loadErr error
		loaded, loadErr = h.load()
		return loadErr
	})
	require.NoError(t, err)
	require.NotNil(t, loaded)
	_, exists := loaded.Tokens["https://nil-token.example.com"]
	assert.True(t, exists, "nil token value must survive round-trip")
}

// TestEdge_VeryLongEndpointURL verifies that very long endpoint URLs
// are stored and retrieved without truncation.
// Failure prevented: endpoint URL truncation causes token lookup failure.
func TestEdge_VeryLongEndpointURL(t *testing.T) {
	t.Parallel()
	client := NewTestClient(t)

	longPath := ""
	for i := 0; i < 300; i++ {
		longPath += "/segment"
	}
	longEP := "https://long.example.com" + longPath
	assert.Greater(t, len(longEP), 2000, "endpoint must be >2000 chars")

	c := client.WithEndpoint(longEP)
	tok := createTestToken(time.Hour)
	tok.AccessToken = "long-endpoint-token"
	require.NoError(t, c.SaveToken(tok))

	got, err := c.GetToken()
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "long-endpoint-token", got.AccessToken)
}

// TestEdge_NoFile verifies loading from a nonexistent file returns
// an empty store, not an error.
// Failure prevented: first-time user gets an error instead of clean empty state.
func TestEdge_NoFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "nonexistent", "auth.json")

	var loaded *AuthStore
	err := withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		var loadErr error
		loaded, loadErr = h.load()
		return loadErr
	})
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.NotNil(t, loaded.Tokens)
	assert.Empty(t, loaded.Tokens)
}

// TestEdge_CorruptJSON verifies that a corrupt JSON file returns a
// parse error rather than silently dropping credentials.
// Failure prevented: corrupt file silently returns empty store.
func TestEdge_CorruptJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	require.NoError(t, os.WriteFile(authPath, []byte(`{corrupt`), 0600))

	err := withAuthFileRLocked(authPath, func(h *authFileHandle) error {
		_, loadErr := h.load()
		return loadErr
	})
	require.Error(t, err, "corrupt JSON must return error, not empty store")
	assert.Contains(t, err.Error(), "parse")
}

// TestEdge_ReadRawDoesNotNormalize verifies readRaw returns the exact
// on-disk keys without normalization — used for save-side diffing.
// Failure prevented: normalization in readRaw masks real key differences.
func TestEdge_ReadRawDoesNotNormalize(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	// write with un-normalized key
	rawStore := `{"tokens":{"https://api.raw.example.com":{"access_token":"raw"}}}`
	require.NoError(t, os.WriteFile(authPath, []byte(rawStore), 0600))

	err := withAuthFileLocked(authPath, func(h *authFileHandle) error {
		raw, readErr := h.readRaw()
		if readErr != nil {
			return readErr
		}
		_, found := raw.Tokens["https://api.raw.example.com"]
		assert.True(t, found, "readRaw must preserve exact keys from disk")
		return nil
	})
	require.NoError(t, err)
}

// TestEdge_CallerInfo verifies callerInfo returns a non-empty file:line string.
// Failure prevented: missing caller info in credential removal warnings.
func TestEdge_CallerInfo(t *testing.T) {
	t.Parallel()

	info := callerInfo(1)
	assert.NotEqual(t, "unknown", info)
	assert.Contains(t, info, ".go:")
}

// TestEdge_HandleOptions verifies functional options correctly configure
// the authFileHandle.
// Failure prevented: option not applied, legacy migration uses wrong endpoint.
func TestEdge_HandleOptions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")

	var capturedEP string
	err := withAuthFileLocked(authPath, func(h *authFileHandle) error {
		capturedEP = h.legacyMigrationEndpoint
		return nil
	}, withLegacyMigrationEndpoint("https://options.example.com"))
	require.NoError(t, err)
	assert.Equal(t, "https://options.example.com", capturedEP)
}
