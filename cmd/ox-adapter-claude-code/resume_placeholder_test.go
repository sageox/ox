package main

import "testing"

// Failure prevented: a session resumed with `claude --continue` showed
// teammates a stray "No response requested." turn between its two parts.
// Claude Code writes that line itself (model "<synthetic>"); it is not a turn.
// Any other synthetic line, and any real answer with that text, is kept.
func TestParseLineDropsOnlyTheResumePlaceholder(t *testing.T) {
	tests := []struct {
		name string
		line string
		want int
	}{
		{
			name: "the resume placeholder is dropped",
			line: `{"type":"assistant","timestamp":"2026-11-21T05:49:01Z","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"No response requested."}]}}`,
			want: 0,
		},
		{
			name: "a synthetic API error is kept",
			line: `{"type":"assistant","timestamp":"2026-11-21T05:49:01Z","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"API Error: 529 overloaded"}]}}`,
			want: 1,
		},
		{
			name: "a real model saying the same words is kept",
			line: `{"type":"assistant","timestamp":"2026-11-21T05:49:01Z","message":{"model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"No response requested."}]}}`,
			want: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, err := parseLine([]byte(tt.line))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != tt.want {
				t.Fatalf("entries = %d, want %d", len(entries), tt.want)
			}
		})
	}
}
