package agentwork

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCodexRunner_Run_PromptViaStdin verifies the prompt is delivered to the
// codex subprocess via stdin and never appears as an argv element (security
// finding #10 — argv is world-readable to same-UID processes).
func TestCodexRunner_Run_PromptViaStdin(t *testing.T) {
	tmp := t.TempDir()
	argsFile := filepath.Join(tmp, "args.txt")
	stdinFile := filepath.Join(tmp, "stdin.txt")
	script := filepath.Join(tmp, "codex")
	body := `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a" >> "` + argsFile + `"; done
cat > "` + stdinFile + `"
sleep 0.1
`
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))

	r := &CodexRunner{binaryPath: script, logger: slog.Default()}

	const secret = "SENSITIVE-TRANSCRIPT-CONTENT-do-not-leak"
	_, err := r.Run(context.Background(), RunRequest{
		Prompt: secret, TimeoutOverride: 5 * time.Second,
	})
	require.NoError(t, err)

	argv, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	stdin, err := os.ReadFile(stdinFile)
	require.NoError(t, err)

	assert.NotContains(t, string(argv), secret, "prompt leaked into argv")
	assert.Equal(t, secret, string(stdin), "prompt not delivered via stdin")
	// `-` positional must remain so codex reads the prompt from stdin
	assert.Contains(t, strings.Split(strings.TrimSpace(string(argv)), "\n"), "-")
}
