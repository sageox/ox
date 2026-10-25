package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// importPreparationFixture discovers one eligible session against an isolated
// real Ledger clone, retaining production preparation and fake summarization.
func importPreparationFixture(t *testing.T) (*importFixture, *importEnv, *importCandidate) {
	t.Helper()
	if testing.Short() {
		t.Skip("preparation failure tests create real Git remotes")
	}
	f := newImportFixture(t)
	f.add(t, pastSession{
		agent: nativeimport.AgentClaude, id: e2eClaudeA,
		start:  time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC),
		prompt: loginPrompt, reply: "Fixed the cookie.",
	})
	opts := importOptions{yes: true, jsonOut: true}
	env, _ := f.envFor(f.ledgerPath, opts)
	candidates, _, failure := planImport(context.Background(), opts, env)
	require.Nil(t, failure)
	require.Len(t, candidates, 1)
	require.Equal(t, stateReady, candidates[0].State)
	return f, env, candidates[0]
}

// requireImportPermissionChecks skips platforms or identities that bypass the
// Unix permission failures these two staging scenarios reproduce.
func requireImportPermissionChecks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("staging permission failures require a non-root Unix process")
	}
}

// TestImportPrepareFailuresLeaveNothingPublished checks real filesystem and
// cancellation failures at successive preparation boundaries. Incomplete
// artifacts are removed; an undeletable pre-existing directory stays intact.
func TestImportPrepareFailuresLeaveNothingPublished(t *testing.T) {
	for _, tc := range []struct {
		name           string
		configure      func(*testing.T, *importFixture, *importEnv, *importCandidate, context.CancelFunc) func()
		wantError      string
		canceled       bool
		wantCalls      int
		retainExisting bool
	}{
		{
			name: "canceled before preparation",
			configure: func(_ *testing.T, _ *importFixture, _ *importEnv, _ *importCandidate, cancel context.CancelFunc) func() {
				cancel()
				return nil
			},
			canceled: true,
		},
		{
			name: "existing staging cannot be removed",
			configure: func(t *testing.T, _ *importFixture, env *importEnv, c *importCandidate, _ context.CancelFunc) func() {
				requireImportPermissionChecks(t)
				staging := filepath.Join(env.stagingRoot, c.Name)
				require.NoError(t, os.MkdirAll(staging, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(staging, "previous-attempt"), []byte("keep"), 0o600))
				restore := func() { _ = os.Chmod(env.stagingRoot, 0o700) }
				t.Cleanup(restore)
				require.NoError(t, os.Chmod(env.stagingRoot, 0))
				return restore
			},
			wantError: "prepare staging", retainExisting: true,
		},
		{
			name: "staging parent cannot create a directory",
			configure: func(t *testing.T, _ *importFixture, env *importEnv, _ *importCandidate, _ context.CancelFunc) func() {
				requireImportPermissionChecks(t)
				require.NoError(t, os.MkdirAll(env.stagingRoot, 0o700))
				restore := func() { _ = os.Chmod(env.stagingRoot, 0o700) }
				t.Cleanup(restore)
				require.NoError(t, os.Chmod(env.stagingRoot, 0o500))
				return restore
			},
			wantError: "prepare staging",
		},
		{
			name: "conversion rejects invalid redaction policy",
			configure: func(t *testing.T, f *importFixture, _ *importEnv, _ *importCandidate, _ context.CancelFunc) func() {
				require.NoError(t, os.WriteFile(filepath.Join(f.projectRoot, ".sageox", "REDACT.md"), []byte("```redact\nregex \"ACME-[\" -> [X]\n```\n"), 0o600))
				return nil
			},
			wantError: "convert: invalid redaction policy",
		},
		{
			name: "interrupted after a valid summary",
			configure: func(_ *testing.T, f *importFixture, _ *importEnv, _ *importCandidate, cancel context.CancelFunc) func() {
				f.summarizer.reply = func(prompt string) string {
					cancel()
					return e2eSummaryFor(prompt)
				}
				return nil
			},
			canceled: true, wantCalls: 1,
		},
		{
			name: "interrupted before retrying a rejected summary",
			configure: func(_ *testing.T, f *importFixture, _ *importEnv, _ *importCandidate, cancel context.CancelFunc) func() {
				f.summarizer.reply = func(string) string {
					cancel()
					return "not a JSON summary"
				}
				return nil
			},
			canceled: true, wantCalls: 1,
		},
		{
			name: "summary artifact path is occupied by a directory",
			configure: func(t *testing.T, f *importFixture, env *importEnv, c *importCandidate, _ context.CancelFunc) func() {
				f.summarizer.before = func(string) {
					require.NoError(t, os.Mkdir(filepath.Join(env.stagingRoot, c.Name, "summary.json"), 0o700))
				}
				return nil
			},
			wantError: "write summary: write summary.json", wantCalls: 1,
		},
		{
			name: "staged metadata is corrupt",
			configure: func(t *testing.T, f *importFixture, env *importEnv, c *importCandidate, _ context.CancelFunc) func() {
				f.summarizer.before = func(string) {
					require.NoError(t, os.WriteFile(filepath.Join(env.stagingRoot, c.Name, "meta.json"), []byte("unfinished metadata"), 0o600))
				}
				return nil
			},
			wantError: "write meta.json: parse session meta", wantCalls: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, env, c := importPreparationFixture(t)
			head := runGit(t, f.ledgerPath, "rev-parse", "HEAD")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			restore := tc.configure(t, f, env, c, cancel)
			prepared, skip, err := prepareImport(ctx, env, c)
			if restore != nil {
				restore()
			}
			if tc.canceled {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, errImportHeld)
				assert.ErrorContains(t, err, tc.wantError)
			}
			assert.Nil(t, prepared, "failed preparation never transfers staging ownership")
			assert.Empty(t, skip, "failures must not be reported as intentional skips")
			staging := filepath.Join(env.stagingRoot, c.Name)
			if tc.retainExisting {
				content, readErr := os.ReadFile(filepath.Join(staging, "previous-attempt"))
				require.NoError(t, readErr)
				assert.Equal(t, "keep", string(content), "a failed removal never replaces pre-existing staging")
			} else {
				assert.NoDirExists(t, staging, "failed preparation cleans all incomplete artifacts")
			}
			assert.Equal(t, tc.wantCalls, f.summarizer.calls())
			assert.Zero(t, f.store.count(), "nothing leaves the machine after a preparation failure")
			assert.Equal(t, head, runGit(t, f.ledgerPath, "rev-parse", "HEAD"))
			assert.Equal(t, head, runGit(t, f.barePath, "rev-parse", "HEAD"))
			assert.Empty(t, remoteSessionDirs(t, f.barePath))
		})
	}
}

// TestImportPublishPreparedCanceledBeforePublication verifies that cancellation
// while a finished summary waits for the committer preserves the native source
// and sends neither LFS objects nor commits; the staging owner cleans up.
func TestImportPublishPreparedCanceledBeforePublication(t *testing.T) {
	f, env, c := importPreparationFixture(t)
	head := runGit(t, f.ledgerPath, "rev-parse", "HEAD")
	prepared, skip, err := prepareImport(context.Background(), env, c)
	require.NoError(t, err)
	require.Empty(t, skip)
	require.NotNil(t, prepared)
	t.Cleanup(prepared.cleanup)
	assert.FileExists(t, filepath.Join(prepared.staging, "summary.json"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	skip, err = publishPreparedImport(ctx, env, c, prepared)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, skip)
	assert.Zero(t, f.store.count())
	assert.Equal(t, head, runGit(t, f.ledgerPath, "rev-parse", "HEAD"))
	assert.Equal(t, head, runGit(t, f.barePath, "rev-parse", "HEAD"))
	assert.FileExists(t, c.Session.Path, "the coworker's native session remains available for retry")
	prepared.cleanup()
	assert.NoDirExists(t, prepared.staging)
}
