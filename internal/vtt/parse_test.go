package vtt

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantCues int
		wantErr  bool
	}{
		{
			name: "basic VTT with speakers",
			input: `WEBVTT

00:00:00.000 --> 00:00:05.000
<v Speaker 1>Hello everyone</v>

00:00:05.000 --> 00:00:10.000
<v Speaker 2>Hi there</v>
`,
			wantCues: 2,
		},
		{
			name: "VTT without speakers",
			input: `WEBVTT

00:00:00.000 --> 00:00:05.000
Hello everyone

00:00:05.000 --> 00:00:10.000
How are you
`,
			wantCues: 2,
		},
		{
			name:    "missing WEBVTT header",
			input:   "not a vtt file",
			wantErr: true,
		},
		{
			name: "empty VTT",
			input: `WEBVTT

`,
			wantCues: 0,
		},
		{
			name: "multi-line cue",
			input: `WEBVTT

00:00:00.000 --> 00:00:05.000
<v Speaker 1>First line</v>
<v Speaker 1>Second line</v>
`,
			wantCues: 1,
		},
		{
			name: "cue without closing v tag",
			input: `WEBVTT

00:00:00.000 --> 00:00:05.000
<v Speaker 1>Hello everyone
`,
			wantCues: 1,
		},
		{
			name: "VTT with cue identifiers",
			input: `WEBVTT

1
00:00:00.000 --> 00:00:05.000
<v Speaker 1>Hello</v>

2
00:00:05.000 --> 00:00:10.000
<v Speaker 2>World</v>
`,
			wantCues: 2,
		},
		{
			name: "no trailing newline",
			input: `WEBVTT

00:00:00.000 --> 00:00:03.000
<v Speaker 1>Last cue no newline</v>`,
			wantCues: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cues, err := Parse([]byte(tt.input))
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(cues) != tt.wantCues {
				t.Errorf("got %d cues, want %d", len(cues), tt.wantCues)
			}
		})
	}
}

// TestParseBodylessCueKeepsOrdinals verifies a cue with a timestamp line but
// no text body still emits (empty text, parsed interval) so Index tracks true
// file position. Failure prevented: a dropped body-less cue shifts every
// later cue's ordinal, so cue-range selectors (--cues N-M) and cue_ref
// citations authored against the file's numbering resolve to the wrong cue.
func TestParseBodylessCueKeepsOrdinals(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []struct {
			index int
			text  string
		}
	}{
		{
			name: "body-less cue mid-file",
			input: `WEBVTT

00:00:00.000 --> 00:00:01.000
Alpha

00:00:01.000 --> 00:00:02.000

00:00:02.000 --> 00:00:03.000
Gamma
`,
			want: []struct {
				index int
				text  string
			}{{1, "Alpha"}, {2, ""}, {3, "Gamma"}},
		},
		{
			name: "body-less final cue without trailing blank line",
			input: `WEBVTT

00:00:00.000 --> 00:00:01.000
Alpha

00:00:01.000 --> 00:00:02.000`,
			want: []struct {
				index int
				text  string
			}{{1, "Alpha"}, {2, ""}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cues, err := Parse([]byte(tt.input))
			if err != nil {
				t.Fatalf("Parse failed: %v", err)
			}
			if len(cues) != len(tt.want) {
				t.Fatalf("got %d cues, want %d: %+v", len(cues), len(tt.want), cues)
			}
			for i, w := range tt.want {
				if cues[i].Index != w.index || cues[i].Text != w.text {
					t.Errorf("cue %d = {Index:%d Text:%q}, want {Index:%d Text:%q}",
						i, cues[i].Index, cues[i].Text, w.index, w.text)
				}
			}
			// The body-less cue keeps its parsed interval so time-window
			// selectors still see it.
			if cues[1].Start == 0 && cues[1].End == 0 {
				t.Errorf("body-less cue lost its timing: %+v", cues[1])
			}
		})
	}
}

func TestParseSpeakerExtraction(t *testing.T) {
	input := `WEBVTT

00:00:00.000 --> 00:00:05.000
<v Alice>Hello everyone</v>

00:00:05.000 --> 00:00:10.000
<v Bob>Hi Alice</v>
`

	cues, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("expected 2 cues, got %d", len(cues))
	}
	if cues[0].Speaker != "Alice" {
		t.Errorf("cue[0] speaker = %q, want %q", cues[0].Speaker, "Alice")
	}
	if cues[0].Text != "Hello everyone" {
		t.Errorf("cue[0] text = %q, want %q", cues[0].Text, "Hello everyone")
	}
	if cues[1].Speaker != "Bob" {
		t.Errorf("cue[1] speaker = %q, want %q", cues[1].Speaker, "Bob")
	}
}

func TestExtractVoiceTag(t *testing.T) {
	tests := []struct {
		line        string
		wantSpeaker string
		wantText    string
	}{
		{"<v Speaker 1>Hello</v>", "Speaker 1", "Hello"},
		{"<v Bob>Hi there</v>", "Bob", "Hi there"},
		{"<v Speaker 1>No closing tag", "Speaker 1", "No closing tag"},
		{"Plain text", "", "Plain text"},
		{"<b>Bold text</b>", "", "Bold text"},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			speaker, text := extractVoiceTag(tt.line)
			if speaker != tt.wantSpeaker {
				t.Errorf("speaker = %q, want %q", speaker, tt.wantSpeaker)
			}
			if text != tt.wantText {
				t.Errorf("text = %q, want %q", text, tt.wantText)
			}
		})
	}
}

func TestFormatAsText(t *testing.T) {
	tests := []struct {
		name string
		cues []Cue
		want string
	}{
		{
			name: "basic formatting",
			cues: []Cue{
				{Speaker: "Alice", Text: "Hello"},
				{Speaker: "Bob", Text: "Hi there"},
			},
			want: "Alice: Hello\nBob: Hi there",
		},
		{
			name: "same speaker merges",
			cues: []Cue{
				{Speaker: "Alice", Text: "Hello"},
				{Speaker: "Alice", Text: "How are you?"},
				{Speaker: "Bob", Text: "Good thanks"},
			},
			want: "Alice: Hello How are you?\nBob: Good thanks",
		},
		{
			name: "no speakers",
			cues: []Cue{
				{Text: "First line"},
				{Text: "Second line"},
			},
			want: "First line\nSecond line",
		},
		{
			name: "empty cues",
			cues: []Cue{},
			want: "",
		},
		{
			name: "skip empty text",
			cues: []Cue{
				{Speaker: "Alice", Text: "Hello"},
				{Speaker: "Alice", Text: ""},
				{Speaker: "Bob", Text: "Hi"},
			},
			want: "Alice: Hello\nBob: Hi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatAsText(tt.cues)
			if got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestStripTags(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"plain text", "plain text"},
		{"<b>bold</b>", "bold"},
		{"<v Speaker>text</v>", "text"}, // v tags are stripped like any other tag
		{"no <tags> here <at> all", "no  here  all"},
	}

	for _, tt := range tests {
		got := stripTags(tt.input)
		if got != tt.want {
			t.Errorf("stripTags(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatAsTextLargeOutput(t *testing.T) {
	// Verify formatting doesn't break with many cues
	var cues []Cue
	for i := 0; i < 100; i++ {
		speaker := "Speaker 1"
		if i%3 == 0 {
			speaker = "Speaker 2"
		}
		cues = append(cues, Cue{Speaker: speaker, Text: "Some discussion content"})
	}

	result := FormatAsText(cues)
	if result == "" {
		t.Error("expected non-empty output for 100 cues")
	}
	// should contain both speakers
	if !strings.Contains(result, "Speaker 1:") {
		t.Error("output should contain Speaker 1")
	}
	if !strings.Contains(result, "Speaker 2:") {
		t.Error("output should contain Speaker 2")
	}
}
