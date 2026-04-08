package claude

import (
	"strings"
	"testing"
)

func TestBuildMergePrompt(t *testing.T) {
	existing := map[string][]byte{
		"MEMORY.md":    []byte("# Existing Memory\n- old pattern A"),
		"debugging.md": []byte("# Debug\n- old tip"),
	}
	newFiles := map[string][]byte{
		"MEMORY.md":   []byte("# Fresh Memory\n- new pattern B"),
		"patterns.md": []byte("# Patterns\n- discovered pattern"),
	}

	prompt := buildMergePrompt(existing, newFiles)

	// verify prompt structure
	checks := []struct {
		desc string
		want string
	}{
		{"has existing section", "EXISTING (currently in ledger):"},
		{"has new section", "NEW (from fresh local workspace):"},
		{"contains existing MEMORY.md", "# Existing Memory"},
		{"contains existing debugging.md", "# Debug"},
		{"contains new MEMORY.md", "# Fresh Memory"},
		{"contains new patterns.md", "# Patterns"},
		{"conflict resolution rule", "ADOPT the fresh Claude-generated memory (NEW)"},
		{"200-line budget", "Keep MEMORY.md under 200 lines"},
		{"output format instruction", "--- filename.md ---"},
		{"end marker instruction", "--- END ---"},
		{"preserve topic files", "Preserve topic file structure"},
	}

	for _, c := range checks {
		if !strings.Contains(prompt, c.want) {
			t.Errorf("%s: prompt missing %q", c.desc, c.want)
		}
	}

	// verify files are sorted (MEMORY.md before debugging.md in existing,
	// MEMORY.md before patterns.md in new)
	existingMemIdx := strings.Index(prompt, "# Existing Memory")
	existingDebugIdx := strings.Index(prompt, "# Debug")
	if existingMemIdx > existingDebugIdx {
		t.Error("existing files not sorted: MEMORY.md should come before debugging.md")
	}

	newMemIdx := strings.Index(prompt, "# Fresh Memory")
	newPatIdx := strings.Index(prompt, "# Patterns")
	if newMemIdx > newPatIdx {
		t.Error("new files not sorted: MEMORY.md should come before patterns.md")
	}
}

func TestParseMergeOutput(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr bool
	}{
		{
			name: "single file",
			input: `--- MEMORY.md ---
# Memory
Some content
--- END ---`,
			want: map[string]string{"MEMORY.md": "# Memory\nSome content"},
		},
		{
			name: "multiple files",
			input: `--- MEMORY.md ---
# Memory
--- END ---
--- debugging.md ---
# Debug
Tips here
--- END ---`,
			want: map[string]string{
				"MEMORY.md":    "# Memory",
				"debugging.md": "# Debug\nTips here",
			},
		},
		{
			name:    "empty output",
			input:   "",
			wantErr: true,
		},
		{
			name:    "no file markers",
			input:   "just some text without markers",
			wantErr: true,
		},
		{
			name: "missing END marker (trailing file)",
			input: `--- MEMORY.md ---
# Memory
Content without END`,
			want: map[string]string{"MEMORY.md": "# Memory\nContent without END"},
		},
		{
			name: "preamble text before first file marker",
			input: `Here is the merged output:

--- MEMORY.md ---
# Memory
--- END ---`,
			want: map[string]string{"MEMORY.md": "# Memory"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMergeOutput(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseMergeOutput() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			for name, wantContent := range tt.want {
				gotContent, ok := got[name]
				if !ok {
					t.Errorf("missing file %q", name)
					continue
				}
				if string(gotContent) != wantContent {
					t.Errorf("%s = %q, want %q", name, gotContent, wantContent)
				}
			}
		})
	}
}
