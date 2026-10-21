package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAmpPromptOffset(t *testing.T) {
	const (
		meta    = `{"type":"session_meta","session_id":"T-1"}` + "\n"
		prompt1 = `{"type":"user","content":"first"}` + "\n"
		reply1  = `{"type":"assistant","content":"done"}` + "\n"
		prompt2 = `{"type":"user","content":"second"}` + "\n"
		tool    = `{"type":"tool_use","tool_name":"Bash","tool_input":"ox agent prime"}` + "\n"
	)
	for _, tc := range []struct {
		name    string
		sidecar string
		want    int // bytes before the recording's first line
	}{
		{"the thread's first prompt", meta + prompt1 + tool, len(meta)},
		{"the prompt in progress, not an earlier one", meta + prompt1 + reply1 + prompt2 + tool, len(meta + prompt1 + reply1)},
		{"no prompt yet: the end", meta, len(meta)},
		{"empty", "", 0},
		{"a partial last line is still being written", meta + prompt1 + `{"type":"user","con`, len(meta)},
		{"a line that is not JSON is skipped", meta + "not json\n" + prompt1, len(meta + "not json\n")},
		{"a prompt's text naming a type is not a prompt", meta + prompt1 + `{"type":"assistant","content":"{\"type\":\"user\"}"}` + "\n", len(meta)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "T-1.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(tc.sidecar), 0o600))
			got, err := ampPromptOffset(path)
			require.NoError(t, err)
			assert.EqualValues(t, tc.want, got)
			if strings.HasSuffix(tc.sidecar, "\n") && tc.want < len(tc.sidecar) {
				assert.Contains(t, tc.sidecar[tc.want:], `"type":"user"`)
			}
		})
	}

	_, err := ampPromptOffset(filepath.Join(t.TempDir(), "missing.jsonl"))
	assert.Error(t, err)
}
