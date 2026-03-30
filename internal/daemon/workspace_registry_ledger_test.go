package daemon

import (
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// InitializeLedger
// --------------------------------------------------------------------------

func TestInitializeLedger_FirstInit_RegistersCorrectly(t *testing.T) {
	// real-world failure prevented: if InitializeLedger doesn't set all fields
	// correctly, the daemon won't sync the ledger — sessions won't be pushed
	t.Parallel()

	reg := NewWorkspaceRegistry(t.TempDir(), "test-repo")
	reg.repoID = "repo-123"
	reg.endpoint = "test.sageox.ai"

	reg.InitializeLedger("https://git.example.com/ledger.git", "/some/project")

	ledger := reg.GetLedger()
	require.NotNil(t, ledger)
	assert.Equal(t, "ledger", ledger.ID)
	assert.Equal(t, WorkspaceTypeLedger, ledger.Type)
	assert.Equal(t, "https://git.example.com/ledger.git", ledger.CloneURL)
	assert.Equal(t, "test.sageox.ai", ledger.Endpoint)
	assert.Equal(t, config.DefaultLedgerPath("repo-123", "test.sageox.ai"), ledger.Path,
		"path must use canonical DefaultLedgerPath")
}

func TestInitializeLedger_Idempotent(t *testing.T) {
	// real-world failure prevented: if ox init or daemon startup races and calls
	// InitializeLedger twice, it must not create duplicate entries
	t.Parallel()

	reg := NewWorkspaceRegistry(t.TempDir(), "test-repo")
	reg.repoID = "repo-123"
	reg.endpoint = "test.sageox.ai"

	reg.InitializeLedger("https://git.example.com/ledger.git", "/project")
	reg.InitializeLedger("https://git.example.com/ledger.git", "/project")

	count := 0
	for _, ws := range reg.workspaces {
		if ws.Type == WorkspaceTypeLedger {
			count++
		}
	}
	assert.Equal(t, 1, count, "re-init must not create duplicate ledger")
}

func TestInitializeLedger_UpdatesCloneURL(t *testing.T) {
	// real-world failure prevented: after repo migration, the API returns a new
	// clone URL — re-init must update it so the daemon clones from the right place
	t.Parallel()

	reg := NewWorkspaceRegistry(t.TempDir(), "test-repo")
	reg.repoID = "repo-456"
	reg.endpoint = "test.sageox.ai"

	reg.InitializeLedger("https://git.example.com/old.git", "/project")
	reg.InitializeLedger("https://git.example.com/new.git", "/project")

	assert.Equal(t, "https://git.example.com/new.git", reg.GetLedger().CloneURL)
}

func TestInitializeLedger_FillsEmptyPath(t *testing.T) {
	// real-world failure prevented: a ledger loaded from config with empty path
	// would cause "ox status" to show "not cloned" even though it is cloned
	t.Parallel()

	reg := NewWorkspaceRegistry(t.TempDir(), "test-repo")
	reg.repoID = "repo-fix"
	reg.endpoint = "test.sageox.ai"

	// simulate broken state: ledger exists but has empty path
	reg.ledger = &WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		CloneURL: "https://git.example.com/old.git",
		Path:     "",
	}
	reg.workspaces["ledger"] = reg.ledger

	reg.InitializeLedger("https://git.example.com/new.git", "/project")

	assert.Equal(t, config.DefaultLedgerPath("repo-fix", "test.sageox.ai"), reg.GetLedger().Path,
		"re-init must fill empty path")
}

func TestInitializeLedger_PreservesExistingPath(t *testing.T) {
	// real-world failure prevented: a working ledger path being overwritten by
	// re-init would disconnect the daemon from the actual clone
	t.Parallel()

	reg := NewWorkspaceRegistry(t.TempDir(), "test-repo")
	reg.repoID = "repo-keep"
	reg.endpoint = "test.sageox.ai"

	existing := "/already/set/ledger/path"
	reg.ledger = &WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		CloneURL: "https://git.example.com/old.git",
		Path:     existing,
	}
	reg.workspaces["ledger"] = reg.ledger

	reg.InitializeLedger("https://git.example.com/new.git", "/project")

	assert.Equal(t, existing, reg.GetLedger().Path,
		"re-init must not overwrite existing non-empty path")
}

func TestInitializeLedger_ReVerifiesExists(t *testing.T) {
	// real-world failure prevented: if the ledger directory is deleted between
	// daemon restarts, re-calling InitializeLedger would keep stale Exists=true
	// because it only checked Exists when path was empty. Now it always re-verifies.
	t.Parallel()

	reg := NewWorkspaceRegistry(t.TempDir(), "test-repo")
	reg.repoID = "repo-reverify"
	reg.endpoint = "test.sageox.ai"

	reg.ledger = &WorkspaceState{
		ID:       "ledger",
		Type:     WorkspaceTypeLedger,
		CloneURL: "https://git.example.com/old.git",
		Path:     "/nonexistent/deleted/ledger",
		Exists:   true, // stale — directory was deleted
	}
	reg.workspaces["ledger"] = reg.ledger

	reg.InitializeLedger("https://git.example.com/new.git", "/project")

	assert.False(t, reg.GetLedger().Exists,
		"re-init must re-verify Exists; stale Exists=true causes daemon to pull into missing dir")
}

func TestGetLedger_NilBeforeInit(t *testing.T) {
	// real-world failure prevented: callers assuming GetLedger always returns
	// non-nil would panic on first daemon startup before ledger provisioning
	t.Parallel()

	reg := NewWorkspaceRegistry(t.TempDir(), "test-repo")
	assert.Nil(t, reg.GetLedger())
}
