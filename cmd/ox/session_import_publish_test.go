package main

import (
	"testing"

	"github.com/sageox/ox/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failure prevented: a credential in meta.json or summary.json, which are
// indented and so never decode line by line, committed past the residual
// scan; and an artifact that cannot be decoded published unscanned.
func TestContainsSecretReadsWholeDocuments(t *testing.T) {
	const key = "AKIAIOSFODNN7EXAMPLE" // AWS's published example key
	r := session.NewRedactor()
	tests := []struct {
		name, file, data string
		want             bool
		wantErr          string
	}{
		{"an indented JSON document", "summary.json", "{\n  \"title\": \"Deploy\",\n  \"score_reason\": \"used " + key + "\"\n}\n", true, ""},
		{"a clean indented JSON document", "meta.json", "{\n  \"title\": \"Deploy\"\n}\n", false, ""},
		{"a JSONL record after a blank line", "raw.jsonl", "{\"type\":\"header\"}\n\n{\"content\":\"" + key + "\"}\n", true, ""},
		{"markdown", "summary.md", "# Summary\n\nused " + key + "\n", true, ""},
		{"a JSON document that does not decode", "summary.json", "{\n  \"title\": \"Deploy\",\n", false, "cannot scan summary.json"},
		{"a JSONL record that does not decode", "raw.jsonl", "{\"type\":\"header\"}\n{\"content\":\n", false, "cannot scan raw.jsonl line 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found, err := containsSecret(r, tt.file, []byte(tt.data))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, found)
		})
	}
}
