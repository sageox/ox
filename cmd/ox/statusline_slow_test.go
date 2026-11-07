//go:build slow

package main

import (
	"bytes"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/oxactivity"
	"github.com/sageox/ox/internal/testguard"
)

// TestStatuslineBinary_ColorReachesClaudeCode runs the real binary the way
// Claude Code does: JSON on stdin, stdout a pipe. main's init() strips ANSI
// from piped stdout and sets NO_COLOR, so this is the only test that proves
// the green actually reaches the status bar — and that a coworker's own
// NO_COLOR still turns it off. It also reports end-to-end latency.
// Failure prevented: a colorless (or error-filled) status bar in production
// while every in-process test passes.
func TestStatuslineBinary_ColorReachesClaudeCode(t *testing.T) {
	oxBin := testguard.BuildOxBinary(t, slowTestProjectRoot(t))
	f := newStatuslineFixture(t)
	f.seed(t, oxactivity.Counts{Discussions: 3, Plans: 1, Murmurs: 1})

	run := func(extra ...string) (string, time.Duration) {
		env := append([]string{
			config.EnvUserConfig + "=" + f.userConfig,
			"CLAUDE_CONFIG_DIR=" + f.claudeDir,
			"CLAUDECODE=1", // inside Claude Code: ox's agent detection must not force no-color
		}, extra...)
		cmd := testguard.OxCmd(t, oxBin, f.root, env, "statusline")
		cmd.Stdin = bytes.NewReader(f.stdin())
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		start := time.Now()
		require.NoError(t, cmd.Run(), "stderr: %s", stderr.String())
		return stdout.String(), time.Since(start)
	}

	colored, _ := run()
	assert.Contains(t, colored, "\x1b[", "the status line must carry color through the piped stdout")
	assert.Contains(t, colored, "discussions")

	plain, _ := run("NO_COLOR=1")
	assert.NotContains(t, plain, "\x1b")
	assert.Equal(t, "● ox  3 discussions · 1 plan · 1 murmur\n", plain)

	var times []time.Duration
	for range 20 {
		_, d := run()
		times = append(times, d)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	t.Logf("ox statusline end-to-end (binary, cache warm): median %v, p90 %v", times[10], times[18])
	if os.Getenv("CI") == "" {
		assert.Less(t, times[10], 100*time.Millisecond, "median refresh")
	}
	assert.False(t, strings.Contains(colored, "Error"), colored)
}
