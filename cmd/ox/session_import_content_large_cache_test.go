//go:build !slow

// Kept out of the slow tier, which re-runs every cmd/ox test serially under
// -race with a 20m package budget: since #1434 this test spends ~6 minutes
// there in regex redaction of its 7 MB prompts. The full tier and CI still run
// it under -race. Move it back into session_import_content_lifecycle_test.go
// once sageox/ox#1457 makes redaction single-pass.

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/session/nativeimport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cache eviction bounds retained conversation memory while preserving the
// immutable review pin and allowing evicted sessions to be read again.
func TestImportContent_LargeSessionCacheEvictsAndReloads(t *testing.T) {
	if testing.Short() {
		t.Skip("large retained-session cache exercise exceeds the short-test budget")
	}
	f := newImportFixture(t)
	start := time.Date(2026, 9, 16, 9, 0, 0, 0, time.UTC)
	for i, id := range []string{e2eClaudeA, e2eClaudeB} {
		f.add(t, pastSession{agent: nativeimport.AgentClaude, id: id, start: start.Add(time.Duration(i) * time.Hour), prompt: strings.Repeat("review this request ", 350000), reply: id})
	}
	env, _ := f.envFor(f.ledgerPath, importOptions{})
	cands, _, failure := planImport(context.Background(), importOptions{}, env)
	require.Nil(t, failure)
	load := newImportPreviewLoader(env, cands)
	first, err := load(context.Background(), e2eClaudeA)
	require.NoError(t, err)
	second, err := load(context.Background(), e2eClaudeB)
	require.NoError(t, err)
	require.Greater(t, importPreviewBytes(first)+importPreviewBytes(second), 32<<20)
	require.Less(t, importPreviewBytes(first), 32<<20)
	cached, err := load(context.Background(), e2eClaudeB)
	require.NoError(t, err)
	assert.Same(t, second, cached)
	reloaded, err := load(context.Background(), e2eClaudeA)
	require.NoError(t, err)
	assert.NotSame(t, first, reloaded)
	assert.Equal(t, first, reloaded)
	assert.Equal(t, 3, f.readCount())
	assert.Zero(t, importPreviewBytes(nil))
}
