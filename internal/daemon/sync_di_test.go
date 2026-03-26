package daemon

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockGitRunner records calls for verification.
type mockGitRunner struct {
	calls    atomic.Int64
	lastArgs []string
	output   string
	err      error
}

func (m *mockGitRunner) RunGit(_ context.Context, _ string, args ...string) (string, error) {
	m.calls.Add(1)
	m.lastArgs = args
	return m.output, m.err
}

var _ gitutil.GitRunner = (*mockGitRunner)(nil)

// mockCredentialProvider records calls for verification.
type mockCredentialProvider struct {
	calls atomic.Int64
	creds *gitserver.GitCredentials
	err   error
}

func (m *mockCredentialProvider) LoadCredentialsForEndpoint(_ string) (*gitserver.GitCredentials, error) {
	m.calls.Add(1)
	return m.creds, m.err
}

var _ CredentialProvider = (*mockCredentialProvider)(nil)

func TestDI_DefaultsToProductionImplementations(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ProjectRoot = t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	s := NewSyncScheduler(cfg, logger)

	require.NotNil(t, s.git, "git runner should default to production implementation")
	require.NotNil(t, s.creds, "credential provider should default to production implementation")
}

func TestDI_WithGitRunnerOption(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ProjectRoot = t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mock := &mockGitRunner{}
	s := NewSyncScheduler(cfg, logger, WithGitRunner(mock))

	assert.Same(t, mock, s.git, "should use injected git runner")
}

func TestDI_WithCredentialProviderOption(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ProjectRoot = t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mock := &mockCredentialProvider{}
	s := NewSyncScheduler(cfg, logger, WithCredentialProvider(mock))

	assert.Same(t, mock, s.creds, "should use injected credential provider")
}

func TestDI_PushMurmurCommitsUsesInjectedGitRunner(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultConfig()
	cfg.ProjectRoot = tmpDir
	cfg.LedgerPath = tmpDir
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	mock := &mockGitRunner{output: ""} // empty = no unpushed commits
	s := NewSyncScheduler(cfg, logger, WithGitRunner(mock))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s.pushMurmurCommits(ctx, tmpDir)

	assert.Equal(t, int64(1), mock.calls.Load(), "should call injected git runner for murmur commit check")
	assert.Contains(t, mock.lastArgs, "log", "should pass log subcommand")
	assert.Contains(t, mock.lastArgs, "data/murmurs/", "should filter to murmur path")
}

func TestDI_BackwardCompatibility(t *testing.T) {
	// existing callers pass only (cfg, logger) — must still work
	cfg := DefaultConfig()
	cfg.ProjectRoot = t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	s := NewSyncScheduler(cfg, logger)
	require.NotNil(t, s)
	require.NotNil(t, s.git)
	require.NotNil(t, s.creds)
}
