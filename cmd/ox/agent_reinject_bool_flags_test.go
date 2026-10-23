package main

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

// TestReinjectBoolFlags covers handing cobra-parsed presence flags back to the
// agent session verbs' manual parsers.
// Failure prevented: cobra strips registered flags before the dispatcher runs,
// so a dropped `--stdin` makes `session log --stdin` read nothing and an
// unset or unknown flag must never be invented, or a verb is told to read a
// stream the caller never offered.
func TestReinjectBoolFlags(t *testing.T) {
	t.Parallel()

	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{Use: "agent"}
		cmd.Flags().Bool("stdin", false, "")
		cmd.Flags().Bool("local", false, "")
		cmd.Flags().String("title", "", "")
		return cmd
	}

	tests := []struct {
		name      string
		cmd       func() *cobra.Command
		set       []string
		args      []string
		flagNames []string
		want      []string
	}{
		{
			name:      "nil command returns args unchanged",
			cmd:       func() *cobra.Command { return nil },
			args:      []string{"entry"},
			flagNames: []string{"stdin"},
			want:      []string{"entry"},
		},
		{
			name:      "set flag is appended as a presence flag",
			cmd:       newCmd,
			set:       []string{"stdin"},
			args:      []string{"entry"},
			flagNames: []string{"stdin"},
			want:      []string{"entry", "--stdin"},
		},
		{
			name:      "unset flag is not injected",
			cmd:       newCmd,
			args:      []string{"entry"},
			flagNames: []string{"stdin"},
			want:      []string{"entry"},
		},
		{
			name:      "only the requested flags are injected",
			cmd:       newCmd,
			set:       []string{"stdin", "local"},
			args:      nil,
			flagNames: []string{"stdin"},
			want:      []string{"--stdin"},
		},
		{
			name:      "several set flags keep the requested order",
			cmd:       newCmd,
			set:       []string{"stdin", "local"},
			args:      nil,
			flagNames: []string{"local", "stdin"},
			want:      []string{"--local", "--stdin"},
		},
		{
			name:      "unregistered flag name is ignored",
			cmd:       newCmd,
			set:       []string{"stdin"},
			args:      []string{"entry"},
			flagNames: []string{"does-not-exist"},
			want:      []string{"entry"},
		},
		{
			name:      "string flag is not mistaken for a presence flag",
			cmd:       newCmd,
			args:      []string{"entry"},
			flagNames: []string{"title"},
			want:      []string{"entry"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd := tt.cmd()
			for _, name := range tt.set {
				assert.NoError(t, cmd.Flags().Set(name, "true"))
			}
			got := reinjectBoolFlags(cmd, append([]string(nil), tt.args...), tt.flagNames...)
			assert.Equal(t, tt.want, got)
		})
	}
}
