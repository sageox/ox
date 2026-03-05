package adapters

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenericJSONLAdapter_Name(t *testing.T) {
	adapter := &GenericJSONLAdapter{}
	assert.Equal(t, "generic", adapter.Name())
}

func TestGenericJSONLAdapter_Detect(t *testing.T) {
	adapter := &GenericJSONLAdapter{}
	assert.False(t, adapter.Detect(), "generic adapter should never auto-detect")
}

func TestGenericJSONLAdapter_FindSessionFile(t *testing.T) {
	adapter := &GenericJSONLAdapter{}
	_, err := adapter.FindSessionFile("agent-123", time.Now())
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestGenericJSONLAdapter_Read(t *testing.T) {
	adapter := &GenericJSONLAdapter{}

	tests := []struct {
		name          string
		content       string
		wantCount     int
		wantRoles     []string
		wantContents  []string
		wantToolNames []string
	}{
		{
			name: "valid JSONL with header, entries, and footer",
			content: `{"type":"header","metadata":{"agent_version":"1.2.0","model":"gpt-4o"}}
{"type":"user","content":"Hello world","timestamp":"2026-01-05T10:00:00Z"}
{"type":"assistant","content":"Hi there!","timestamp":"2026-01-05T10:00:01Z"}
{"type":"footer","summary":"session ended"}
`,
			wantCount:    2,
			wantRoles:    []string{"user", "assistant"},
			wantContents: []string{"Hello world", "Hi there!"},
		},
		{
			name: "entries-only file without header or footer",
			content: `{"type":"user","content":"First message","timestamp":"2026-01-05T10:00:00Z"}
{"type":"assistant","content":"Response","timestamp":"2026-01-05T10:00:01Z"}
{"type":"system","content":"Context loaded","timestamp":"2026-01-05T10:00:02Z"}
`,
			wantCount:    3,
			wantRoles:    []string{"user", "assistant", "system"},
			wantContents: []string{"First message", "Response", "Context loaded"},
		},
		{
			name: "malformed lines mixed with valid entries",
			content: `{"type":"user","content":"Before bad line","timestamp":"2026-01-05T10:00:00Z"}
this is not valid json at all
{"type":"assistant","content":"After bad line","timestamp":"2026-01-05T10:00:01Z"}
{broken json
{"type":"user","content":"Last entry","timestamp":"2026-01-05T10:00:02Z"}
`,
			wantCount:    3,
			wantRoles:    []string{"user", "assistant", "user"},
			wantContents: []string{"Before bad line", "After bad line", "Last entry"},
		},
		{
			name: "duplicate headers from retry concatenation",
			content: `{"type":"header","metadata":{"agent_version":"1.0.0"}}
{"type":"user","content":"First attempt","timestamp":"2026-01-05T10:00:00Z"}
{"type":"header","metadata":{"agent_version":"1.0.0"}}
{"type":"user","content":"Retry attempt","timestamp":"2026-01-05T10:01:00Z"}
{"type":"assistant","content":"Got it","timestamp":"2026-01-05T10:01:01Z"}
`,
			wantCount:    3,
			wantRoles:    []string{"user", "user", "assistant"},
			wantContents: []string{"First attempt", "Retry attempt", "Got it"},
		},
		{
			name:      "empty file",
			content:   "",
			wantCount: 0,
		},
		{
			name: "tool entries with tool_name and tool_input",
			content: `{"type":"tool","tool_name":"Read","tool_input":"/path/to/file.go","content":"file contents here","timestamp":"2026-01-05T10:00:00Z"}
{"type":"tool","tool_name":"Bash","tool_input":"ls -la","timestamp":"2026-01-05T10:00:01Z"}
`,
			wantCount:     2,
			wantRoles:     []string{"tool", "tool"},
			wantToolNames: []string{"Read", "Bash"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			sessionFile := filepath.Join(tmpDir, "session.jsonl")
			require.NoError(t, os.WriteFile(sessionFile, []byte(tt.content), 0644))

			entries, err := adapter.Read(sessionFile)
			require.NoError(t, err)
			require.Len(t, entries, tt.wantCount)

			for i, wantRole := range tt.wantRoles {
				assert.Equal(t, wantRole, entries[i].Role, "entry[%d].Role", i)
			}
			for i, wantContent := range tt.wantContents {
				assert.Equal(t, wantContent, entries[i].Content, "entry[%d].Content", i)
			}
			for i, wantToolName := range tt.wantToolNames {
				assert.Equal(t, wantToolName, entries[i].ToolName, "entry[%d].ToolName", i)
			}
		})
	}
}

func TestGenericJSONLAdapter_Read_FileNotFound(t *testing.T) {
	adapter := &GenericJSONLAdapter{}
	_, err := adapter.Read("/nonexistent/path/session.jsonl")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open session file")
}

func TestGenericJSONLAdapter_Read_Timestamps(t *testing.T) {
	adapter := &GenericJSONLAdapter{}
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "ts-session.jsonl")

	content := `{"type":"user","content":"test","timestamp":"2026-01-05T10:30:45Z"}
`
	require.NoError(t, os.WriteFile(sessionFile, []byte(content), 0644))

	entries, err := adapter.Read(sessionFile)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	expected := time.Date(2026, 1, 5, 10, 30, 45, 0, time.UTC)
	assert.True(t, entries[0].Timestamp.Equal(expected), "Timestamp = %v, want %v", entries[0].Timestamp, expected)
}

func TestGenericJSONLAdapter_Read_RawField(t *testing.T) {
	adapter := &GenericJSONLAdapter{}
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "raw-session.jsonl")

	content := `{"type":"user","content":"hello","custom_field":"preserved"}
`
	require.NoError(t, os.WriteFile(sessionFile, []byte(content), 0644))

	entries, err := adapter.Read(sessionFile)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	assert.NotNil(t, entries[0].Raw, "Raw field should be populated")
	assert.Contains(t, string(entries[0].Raw), "custom_field")
	assert.Contains(t, string(entries[0].Raw), "preserved")
}

func TestGenericJSONLAdapter_Read_ToolInput(t *testing.T) {
	adapter := &GenericJSONLAdapter{}
	tmpDir := t.TempDir()
	sessionFile := filepath.Join(tmpDir, "tool-session.jsonl")

	content := `{"type":"tool","tool_name":"Read","tool_input":"/src/main.go","content":"package main","timestamp":"2026-01-05T10:00:00Z"}
`
	require.NoError(t, os.WriteFile(sessionFile, []byte(content), 0644))

	entries, err := adapter.Read(sessionFile)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	assert.Equal(t, "tool", entries[0].Role)
	assert.Equal(t, "Read", entries[0].ToolName)
	assert.Equal(t, "/src/main.go", entries[0].ToolInput)
	assert.Equal(t, "package main", entries[0].Content)
}

func TestGenericJSONLAdapter_ReadMetadata(t *testing.T) {
	adapter := &GenericJSONLAdapter{}

	t.Run("header with metadata extracts version and model", func(t *testing.T) {
		tmpDir := t.TempDir()
		sessionFile := filepath.Join(tmpDir, "meta-session.jsonl")
		content := `{"type":"header","metadata":{"agent_version":"2.1.0","model":"gpt-4o"}}
{"type":"user","content":"hello","timestamp":"2026-01-05T10:00:00Z"}
`
		require.NoError(t, os.WriteFile(sessionFile, []byte(content), 0644))

		meta, err := adapter.ReadMetadata(sessionFile)
		require.NoError(t, err)
		require.NotNil(t, meta)
		assert.Equal(t, "2.1.0", meta.AgentVersion)
		assert.Equal(t, "gpt-4o", meta.Model)
	})

	t.Run("header with partial metadata", func(t *testing.T) {
		tmpDir := t.TempDir()
		sessionFile := filepath.Join(tmpDir, "partial-meta.jsonl")
		content := `{"type":"header","metadata":{"agent_version":"1.0.0"}}
`
		require.NoError(t, os.WriteFile(sessionFile, []byte(content), 0644))

		meta, err := adapter.ReadMetadata(sessionFile)
		require.NoError(t, err)
		require.NotNil(t, meta)
		assert.Equal(t, "1.0.0", meta.AgentVersion)
		assert.Empty(t, meta.Model)
	})

	t.Run("no header returns nil", func(t *testing.T) {
		tmpDir := t.TempDir()
		sessionFile := filepath.Join(tmpDir, "no-header.jsonl")
		content := `{"type":"user","content":"no header here","timestamp":"2026-01-05T10:00:00Z"}
`
		require.NoError(t, os.WriteFile(sessionFile, []byte(content), 0644))

		meta, err := adapter.ReadMetadata(sessionFile)
		require.NoError(t, err)
		assert.Nil(t, meta)
	})

	t.Run("empty file returns nil", func(t *testing.T) {
		tmpDir := t.TempDir()
		sessionFile := filepath.Join(tmpDir, "empty.jsonl")
		require.NoError(t, os.WriteFile(sessionFile, []byte(""), 0644))

		meta, err := adapter.ReadMetadata(sessionFile)
		require.NoError(t, err)
		assert.Nil(t, meta)
	})

	t.Run("header without metadata object returns nil", func(t *testing.T) {
		tmpDir := t.TempDir()
		sessionFile := filepath.Join(tmpDir, "bare-header.jsonl")
		content := `{"type":"header"}
`
		require.NoError(t, os.WriteFile(sessionFile, []byte(content), 0644))

		meta, err := adapter.ReadMetadata(sessionFile)
		require.NoError(t, err)
		assert.Nil(t, meta)
	})

	t.Run("file not found returns error", func(t *testing.T) {
		_, err := adapter.ReadMetadata("/nonexistent/path/session.jsonl")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to open session file")
	})
}

func TestGenericJSONLAdapter_Watch(t *testing.T) {
	adapter := &GenericJSONLAdapter{}
	ctx := context.Background()
	_, err := adapter.Watch(ctx, "/any/path")
	assert.ErrorIs(t, err, ErrWatchNotSupported)
}

func TestGenericJSONLAdapter_AliasResolution(t *testing.T) {
	ResetRegistry()

	// register both adapters so aliases can resolve
	Register(&GenericJSONLAdapter{})
	Register(&mockAdapter{name: "claude-code", detect: true})

	tests := []struct {
		name     string
		lookup   string
		wantName string
		wantErr  bool
	}{
		{"codex resolves to generic", "codex", "generic", false},
		{"Codex case-insensitive", "Codex", "generic", false},
		{"amp resolves to generic", "amp", "generic", false},
		{"Amp case-insensitive", "AMP", "generic", false},
		{"cursor resolves to generic", "cursor", "generic", false},
		{"windsurf resolves to generic", "windsurf", "generic", false},
		{"copilot resolves to generic", "copilot", "generic", false},
		{"aider resolves to generic", "aider", "generic", false},
		{"cody resolves to generic", "cody", "generic", false},
		{"continue resolves to generic", "continue", "generic", false},
		{"cline resolves to generic", "cline", "generic", false},
		{"goose resolves to generic", "goose", "generic", false},
		{"kiro resolves to generic", "kiro", "generic", false},
		{"opencode resolves to generic", "opencode", "generic", false},
		{"droid resolves to generic", "droid", "generic", false},
		{"claude resolves to claude-code", "claude", "claude-code", false},
		{"exact generic match", "generic", "generic", false},
		{"unknown returns error", "unknown-agent", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetAdapter(tt.lookup)
			if tt.wantErr {
				assert.Error(t, err)
				assert.ErrorIs(t, err, ErrAdapterNotFound)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantName, got.Name())
			}
		})
	}
}

func TestErrWatchNotSupported(t *testing.T) {
	require.NotNil(t, ErrWatchNotSupported)
	assert.Equal(t, "watch not supported for this adapter", ErrWatchNotSupported.Error())
}
