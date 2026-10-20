//go:build slow

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A long recording is many modest lines, not one huge one. Session files past
// 8 MiB used to be rejected (and before that, skipped), which either stranded a
// long session on every retry or let it publish unscanned. Race-instrumented
// secret regexes make a multi-megabyte pass too slow for the routine gate.
//
// Stop and doctor retry both reach this scan through prepareSessionUpload, so
// scanning the file here covers both. A credential on the last line proves the
// file is read to the end, which also proves it was not refused.
//
// Failure prevented: a credential late in a recording longer than 8 MiB is never
// looked at, or a long recording is refused on every retry.
func TestScanPaths_StreamsRecordingsPastFormerSizeCap(t *testing.T) {
	const canary = "AKIAIOSFODNN7EXAMPLE"
	line := strings.Repeat("x", 1023) + "\n"
	body := strings.Repeat(line, (8*1024*1024)/len(line)+2) + canary + "\n"

	ledger := t.TempDir()
	path := filepath.Join(ledger, "sessions", "long", "raw.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	require.Greater(t, len(body), 8*1024*1024)

	result, err := scanPaths(ledger, []string{"sessions/long/raw.jsonl"})
	require.NoError(t, err, "a long recording must be scanned, not refused")
	assert.Equal(t, 1, result.FilesScanned)
	require.NotEmpty(t, result.Findings, "the credential on the last line must be found")
	assert.Equal(t, strings.Count(body, "\n"), result.Findings[0].Line)
}
