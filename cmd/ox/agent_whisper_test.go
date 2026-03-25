package main

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

func TestFormatWhispers_ReturnValue(t *testing.T) {


	tests := []struct {
		name    string
		entries []whisperstore.WhisperEntry
		want    bool
	}{
		{
			name:    "nil entries returns false",
			entries: nil,
			want:    false,
		},
		{
			name:    "empty slice returns false",
			entries: []whisperstore.WhisperEntry{},
			want:    false,
		},
		{
			name: "single entry returns true",
			entries: []whisperstore.WhisperEntry{
				{ID: "1", Topic: "test", Content: "hello", Importance: whisperstore.ImportanceNormal, Source: "test"},
			},
			want: true,
		},
		{
			name: "multiple entries returns true",
			entries: []whisperstore.WhisperEntry{
				{ID: "1", Topic: "a", Content: "first", Importance: whisperstore.ImportanceNormal, Source: "test"},
				{ID: "2", Topic: "b", Content: "second", Importance: whisperstore.ImportanceCritical, Source: "murmur"},
				{ID: "3", Topic: "c", Content: "third", Importance: whisperstore.ImportanceAmbient, Source: "activity-summary"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			got := formatWhispers(&buf, tt.entries)
			if got != tt.want {
				t.Errorf("formatWhispers() = %v, want %v", got, tt.want)
			}
			if !tt.want && buf.Len() > 0 {
				t.Errorf("expected no output when returning false, got: %q", buf.String())
			}
			if tt.want && buf.Len() == 0 {
				t.Error("expected output when returning true, got nothing")
			}
		})
	}
}

// xmlEntry mirrors the XML structure emitted by formatWhispers for round-trip parsing.
type xmlEntry struct {
	Importance string `xml:"importance,attr"`
	Topic      string `xml:"topic,attr"`
	Source     string `xml:"source,attr"`
	Content    string `xml:",chardata"`
}

type xmlNewContext struct {
	XMLName xml.Name   `xml:"new-context"`
	Entries []xmlEntry `xml:"entry"`
}

func TestFormatWhispers_XMLRoundTrip(t *testing.T) {


	entries := []whisperstore.WhisperEntry{
		{ID: "1", Topic: "lint", Content: "fix rule X", Importance: whisperstore.ImportanceNormal, Source: "lint-runner"},
		{ID: "2", Topic: "architecture", Content: "API v3 breaking change", Importance: whisperstore.ImportanceCritical, Source: "murmur"},
		{ID: "3", Topic: "ci", Content: "pipeline green", Importance: whisperstore.ImportanceAmbient, Source: "activity-summary"},
	}

	var buf bytes.Buffer
	wrote := formatWhispers(&buf, entries)
	if !wrote {
		t.Fatal("expected formatWhispers to return true")
	}

	// parse the XML back
	var parsed xmlNewContext
	if err := xml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("failed to parse XML output: %v\nraw output:\n%s", err, buf.String())
	}

	if len(parsed.Entries) != len(entries) {
		t.Fatalf("parsed %d entries, want %d", len(parsed.Entries), len(entries))
	}

	for i, want := range entries {
		got := parsed.Entries[i]
		if got.Content != want.Content {
			t.Errorf("entry[%d] content = %q, want %q", i, got.Content, want.Content)
		}
		if got.Topic != want.Topic {
			t.Errorf("entry[%d] topic = %q, want %q", i, got.Topic, want.Topic)
		}
		if got.Source != want.Source {
			t.Errorf("entry[%d] source = %q, want %q", i, got.Source, want.Source)
		}
		if got.Importance != string(want.Importance) {
			t.Errorf("entry[%d] importance = %q, want %q", i, got.Importance, string(want.Importance))
		}
	}
}

func TestFormatWhispers_SpecialCharacters(t *testing.T) {


	entries := []whisperstore.WhisperEntry{
		{
			ID:         "1",
			Topic:      "html-content",
			Content:    `value < 10 && value > 0`,
			Importance: whisperstore.ImportanceNormal,
			Source:     "test",
		},
		{
			ID:         "2",
			Topic:      "quotes",
			Content:    `she said "hello" & 'goodbye'`,
			Importance: whisperstore.ImportanceCritical,
			Source:     "murmur",
		},
		{
			ID:         "3",
			Topic:      "xml-like",
			Content:    `<div class="foo">bar & baz</div>`,
			Importance: whisperstore.ImportanceAmbient,
			Source:     "test",
		},
	}

	var buf bytes.Buffer
	wrote := formatWhispers(&buf, entries)
	if !wrote {
		t.Fatal("expected formatWhispers to return true")
	}

	// XML round-trip: parse back and verify content survives escaping
	var parsed xmlNewContext
	if err := xml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("failed to parse XML output: %v\nraw output:\n%s", err, buf.String())
	}

	if len(parsed.Entries) != len(entries) {
		t.Fatalf("parsed %d entries, want %d", len(parsed.Entries), len(entries))
	}

	for i, want := range entries {
		got := parsed.Entries[i]
		if got.Content != want.Content {
			t.Errorf("entry[%d] content = %q, want %q", i, got.Content, want.Content)
		}
		if got.Topic != want.Topic {
			t.Errorf("entry[%d] topic = %q, want %q", i, got.Topic, want.Topic)
		}
		if got.Source != want.Source {
			t.Errorf("entry[%d] source = %q, want %q", i, got.Source, want.Source)
		}
		if got.Importance != string(want.Importance) {
			t.Errorf("entry[%d] importance = %q, want %q", i, got.Importance, string(want.Importance))
		}
	}
}

func TestFormatWhispers_LargeContent(t *testing.T) {


	// 4096 bytes — max murmur size
	largeContent := strings.Repeat("x", 4096)

	entries := []whisperstore.WhisperEntry{
		{
			ID:         "1",
			Topic:      "large-murmur",
			Content:    largeContent,
			Importance: whisperstore.ImportanceNormal,
			Source:     "murmur",
		},
	}

	var buf bytes.Buffer
	wrote := formatWhispers(&buf, entries)
	if !wrote {
		t.Fatal("expected formatWhispers to return true")
	}

	output := buf.String()

	// verify the full content is present (no truncation)
	if !strings.Contains(output, largeContent) {
		t.Error("large content was truncated")
	}

	// verify XML wrapper still present around large content
	if !strings.Contains(output, "<new-context>") {
		t.Error("missing <new-context> opening tag")
	}
	if !strings.Contains(output, "</new-context>") {
		t.Error("missing </new-context> closing tag")
	}
	if !strings.Contains(output, `topic="large-murmur"`) {
		t.Error("missing topic attribute")
	}
}
