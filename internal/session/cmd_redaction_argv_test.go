package session

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommandRedactorNativeArgv(t *testing.T) {
	for _, tc := range []struct {
		input  string
		redact bool
	}{
		{`{"command":["aws","configure","export-credentials"]}`, true},
		{`{"cmd":["/bin/bash","-lc","aws configure export-credentials"]}`, true},
		{`{"command":["zsh","-c","gh auth token"]}`, true},
		{`{"cmd":["echo","gh auth token"]}`, false},
		{`{"command":["aws","sso","login-other"]}`, false},
		{`{"cmd":12,"command":"gh auth token"}`, true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			r := NewCommandRedactor()
			r.RedactEntry(&SessionEntry{Type: EntryTypeTool, CallID: "call", ToolInput: tc.input})
			output := &SessionEntry{Type: EntryTypeTool, CallID: "call", ToolOutput: "opaque secret", Content: "opaque secret"}
			require.Equal(t, tc.redact, r.RedactEntry(output))
			if tc.redact {
				require.NotContains(t, output.ToolOutput, "opaque secret")
				require.NotContains(t, output.Content, "opaque secret")
			}
		})
	}
}
