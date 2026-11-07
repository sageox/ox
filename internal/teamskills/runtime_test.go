package teamskills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type runtimeVector struct {
	Name           string `json:"name"`
	Body           string `json:"body"`
	RunsOxCommands bool   `json:"runs_ox_commands"`
	ShellFence     bool   `json:"shell_fence"`
}

func loadRuntimeVectors(t *testing.T) []runtimeVector {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "runtime_signal_vectors.json"))
	require.NoError(t, err)
	var file struct {
		Vectors []runtimeVector `json:"vectors"`
	}
	require.NoError(t, json.Unmarshal(data, &file))
	require.NotEmpty(t, file.Vectors, "the shared vector file decoded to nothing; every case below would pass vacuously")
	return file.Vectors
}

// TestBodyRuntimeSignals_SharedVectors pins the coding-agent inference to the
// vector file the SageOx server is checked against too.
// Failure prevented: the CLI warns at publish about a skill the server calls
// portable (or the reverse), so the author is told one thing and the console
// shows another.
func TestBodyRuntimeSignals_SharedVectors(t *testing.T) {
	t.Parallel()
	for _, v := range loadRuntimeVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			var runsOx, shellFence bool
			for _, s := range BodyRuntimeSignals(v.Body) {
				switch s.Code {
				case SignalRunsOxCommands:
					runsOx = true
				case SignalShellFence:
					shellFence = true
				default:
					t.Fatalf("unknown signal code %q", s.Code)
				}
				require.NotEmpty(t, s.Evidence, "a signal with no evidence gives the author nothing to find")
			}
			require.Equal(t, v.RunsOxCommands, runsOx, "runs-ox-commands for %q", v.Body)
			require.Equal(t, v.ShellFence, shellFence, "shell-fence for %q", v.Body)
		})
	}
}

// TestBodyRuntimeSignals_EvidenceNamesTheLine checks the evidence is the text a
// human would search for, not the regex's raw match.
// Failure prevented: a warning quoting "`ox s" or "$ ox sync" sends the author
// hunting for a string that is not in their file the way it was printed.
func TestBodyRuntimeSignals_EvidenceNamesTheLine(t *testing.T) {
	t.Parallel()
	got := BodyRuntimeSignals("Use `ox plan save` here.\n\n```bash\n$ ox sync\n```\n")
	require.Equal(t, []RuntimeSignal{
		{Code: SignalRunsOxCommands, Evidence: "ox plan"},
		{Code: SignalShellFence, Evidence: "```bash"},
	}, got)

	got = BodyRuntimeSignals("```\n  $ ox agent prime\n```\n")
	require.Equal(t, []RuntimeSignal{{Code: SignalRunsOxCommands, Evidence: "ox agent"}}, got)
}

// TestBodyRuntimeSignals_HugeLineIsClipped bounds what a hostile manifest can
// push into a terminal warning.
// Failure prevented: a 64 KiB fence line buries the rest of the publish output.
func TestBodyRuntimeSignals_HugeLineIsClipped(t *testing.T) {
	t.Parallel()
	long := "```bash " + strings.Repeat("x", 10_000) + "\nls\n```\n"
	got := BodyRuntimeSignals(long)
	require.Len(t, got, 1)
	require.LessOrEqual(t, len([]rune(got[0].Evidence)), 121)
}
