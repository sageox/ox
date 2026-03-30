package ledger

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// Init (ledger initialization via clone)
// --------------------------------------------------------------------------

func TestInit_NoRemoteURL_ReturnsError(t *testing.T) {
	// real-world failure prevented: calling Init("", "") would attempt to clone
	// from nowhere — must fail early with a clear error, not spawn a broken git process
	t.Parallel()

	l, err := Init(t.TempDir(), "")
	assert.Error(t, err)
	assert.Nil(t, l)
	assert.ErrorIs(t, err, ErrNoRemoteURL)
}

func TestInit_ValidBareRepo_CreatesLedger(t *testing.T) {
	// real-world failure prevented: the primary Init path must produce a valid
	// ledger with sparse checkout and sessions/ checked out — failures here
	// would block all session recording
	t.Parallel()

	bare := initBareRepo(t)
	dest := filepath.Join(t.TempDir(), "ledger")

	l, err := Init(dest, bare)
	require.NoError(t, err)
	assert.NotNil(t, l)
	assert.Equal(t, dest, l.Path)

	// must be a valid git repo
	assert.FileExists(t, filepath.Join(dest, ".git", "HEAD"))

	// sparse-checkout must include .sageox
	scFile := filepath.Join(dest, ".git", "info", "sparse-checkout")
	content, err := os.ReadFile(scFile)
	require.NoError(t, err)
	assert.Contains(t, string(content), ".sageox")
}

func TestInit_InvalidRemoteURL_ReturnsError(t *testing.T) {
	// real-world failure prevented: if the cloud returns a stale or malformed URL,
	// Init must fail with a clone error, not leave partial state
	t.Parallel()
	if testing.Short() {
		t.Skip("short: git clone")
	}

	dest := filepath.Join(t.TempDir(), "ledger")
	l, err := Init(dest, "https://invalid.example.com/no-such-repo.git")
	assert.Error(t, err)
	assert.Nil(t, l)
	assert.Contains(t, err.Error(), "clone")
}

func TestInit_Idempotent_SecondCallFails(t *testing.T) {
	// real-world failure prevented: if Init is called twice (e.g. race between
	// daemon and CLI), the second must fail safely without corrupting the first
	t.Parallel()

	bare := initBareRepo(t)
	dest := filepath.Join(t.TempDir(), "ledger")

	l1, err := Init(dest, bare)
	require.NoError(t, err)
	assert.NotNil(t, l1)

	// second Init to same non-empty dest should fail (git clone into non-empty dir)
	l2, err := Init(dest, bare)
	assert.Error(t, err, "second Init into existing ledger must fail")
	assert.Nil(t, l2)
}
