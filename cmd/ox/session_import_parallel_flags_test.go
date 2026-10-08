package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: a zero-sized pool silently uploading nothing, or a
// printed retry losing the coworker's chosen limit and starting more CLIs.
func TestImportParallelOptionsSurvivePrintedCommands(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
		bad  bool
	}{
		{"default", nil, 3, false},
		{"sequential", []string{"--parallel", "1"}, 1, false},
		{"larger pool", []string{"--parallel", "7"}, 7, false},
		{"zero", []string{"--parallel", "0"}, 0, true},
		{"negative", []string{"--parallel", "-2"}, -2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "import"}
			cmd.PersistentFlags().Bool("json", true, "")
			addSessionImportFlags(cmd.Flags())
			require.NoError(t, cmd.ParseFlags(tc.args))
			opts, failure := parseImportOptions(cmd)
			if tc.bad {
				require.NotNil(t, failure)
				assert.Equal(t, importErrBadFlag, failure.Code)
				assert.Contains(t, failure.Message, "--parallel must be at least 1")
				return
			}
			require.Nil(t, failure)
			assert.Equal(t, tc.want, opts.parallel)
			c := &importCandidate{}
			c.Session.NativeID = e2eCodexA
			for name, printed := range map[string]string{
				"upload": importUploadCommand(opts, []*importCandidate{c}),
				"retry":  importRetryCommand(opts, c),
			} {
				assert.Equal(t, tc.want, optionsFromCommand(t, printed).parallel, name)
			}
		})
	}
}
