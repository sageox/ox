package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// appendClaudeEntries appends raw Claude Code JSONL lines to a source file.
// Untagged so -short builds of the source-validation and quarantine tests compile.
func appendClaudeEntries(t *testing.T, path string, _ time.Time, lines ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var header struct {
		Cwd string `json:"cwd"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.SplitN(string(data), "\n", 2)[0]), &header))
	require.NotEmpty(t, header.Cwd)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	require.NoError(t, err)
	defer f.Close()
	for _, line := range lines {
		// Native Claude turns include both fields, even when the fixture only
		// cares about content. Keep capture tests faithful to that boundary.
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		record["sessionId"] = strings.TrimSuffix(filepath.Base(path), ".jsonl")
		record["cwd"] = header.Cwd
		encoded, err := json.Marshal(record)
		require.NoError(t, err)
		_, err = f.Write(append(encoded, '\n'))
		require.NoError(t, err)
	}
}
