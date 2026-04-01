package main

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// --- XML structure types for round-trip parsing ---

// xmlEntry mirrors the XML structure emitted by formatWhispers for round-trip parsing.
type xmlEntry struct {
	Importance string `xml:"importance,attr"`
	Topic      string `xml:"topic,attr"`
	Source     string `xml:"source,attr"`
	Agent      string `xml:"agent,attr,omitempty"`
	Files      string `xml:"files,attr,omitempty"`
	Content    string `xml:",chardata"`
}

type xmlMurmurTopic struct {
	Topic   string `xml:"topic,attr"`
	Content string `xml:",chardata"`
}

type xmlSystemReminder struct {
	XMLName       xml.Name         `xml:"system-reminder"`
	MurmurContext string           `xml:"murmur-context,omitempty"`
	MurmurTopics  []xmlMurmurTopic `xml:"murmur-topic"`
	Entries       []xmlEntry       `xml:"entry"`
}

// --- A. Core rendering ---

func TestFormatWhispers_ReturnValue(t *testing.T) {
	tests := []struct {
		name    string
		entries []whisperstore.WhisperEntry
		want    bool
	}{
		{"nil entries returns false", nil, false},
		{"empty slice returns false", []whisperstore.WhisperEntry{}, false},
		{"single entry returns true", []whisperstore.WhisperEntry{{ID: "1", Topic: "t", Content: "c", Importance: whisperstore.ImportanceNormal, Source: "test"}}, true},
		{"multiple entries returns true", []whisperstore.WhisperEntry{
			{ID: "1", Topic: "t", Content: "c", Importance: whisperstore.ImportanceNormal, Source: "test"},
			{ID: "2", Topic: "t", Content: "c", Importance: whisperstore.ImportanceCritical, Source: "test"},
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			got := formatWhispers(&buf, tt.entries)
			if got != tt.want {
				t.Errorf("formatWhispers() = %v, want %v", got, tt.want)
			}
		})
	}
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

	var parsed xmlSystemReminder
	if err := xml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("XML round-trip failed: %v\nraw:\n%s", err, buf.String())
	}

	if len(parsed.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(parsed.Entries))
	}
	if parsed.Entries[0].Topic != "lint" || parsed.Entries[0].Importance != "normal" {
		t.Errorf("entry[0] mismatch: %+v", parsed.Entries[0])
	}
	if parsed.Entries[1].Topic != "architecture" || parsed.Entries[1].Importance != "critical" {
		t.Errorf("entry[1] mismatch: %+v", parsed.Entries[1])
	}
	if parsed.Entries[2].Topic != "ci" || parsed.Entries[2].Importance != "ambient" {
		t.Errorf("entry[2] mismatch: %+v", parsed.Entries[2])
	}
}

func TestFormatWhispers_SpecialCharacters(t *testing.T) {
	entries := []whisperstore.WhisperEntry{
		{
			ID:         "1",
			Topic:      "xml-test",
			Content:    `<script>alert("xss")</script> & "quotes" < > ]]>`,
			Importance: whisperstore.ImportanceNormal,
			Source:     "test",
		},
	}

	var buf bytes.Buffer
	wrote := formatWhispers(&buf, entries)
	if !wrote {
		t.Fatal("expected formatWhispers to return true")
	}

	output := buf.String()

	// verify raw special chars are escaped
	if strings.Contains(output, "<script>") {
		t.Error("unescaped <script> tag found")
	}
	if strings.Contains(output, `"xss"`) && !strings.Contains(output, "&amp;") && !strings.Contains(output, "&#34;") {
		t.Error("unescaped special characters found")
	}

	// verify it's still valid XML
	var parsed xmlSystemReminder
	if err := xml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("XML with special chars failed to parse: %v", err)
	}
	if parsed.Entries[0].Content != `<script>alert("xss")</script> & "quotes" < > ]]>` {
		t.Errorf("content not preserved through XML round-trip: got %q", parsed.Entries[0].Content)
	}
}

func TestFormatWhispers_XMLStructure(t *testing.T) {
	entries := []whisperstore.WhisperEntry{
		{ID: "1", Topic: "lint", Content: "fix rule X", Importance: whisperstore.ImportanceNormal, Source: "test"},
		{ID: "2", Topic: "conflict", Content: "auth middleware", Importance: whisperstore.ImportanceCritical, Source: "murmur"},
		{ID: "3", Topic: "info", Content: "CI passed", Importance: whisperstore.ImportanceAmbient, Source: "activity-summary"},
	}

	var buf bytes.Buffer
	wrote := formatWhispers(&buf, entries)
	if !wrote {
		t.Fatal("expected formatWhispers to return true")
	}

	output := buf.String()

	if !strings.Contains(output, "<system-reminder>") {
		t.Error("missing <system-reminder> opening tag")
	}
	if !strings.Contains(output, "</system-reminder>") {
		t.Error("missing </system-reminder> closing tag")
	}
	if !strings.Contains(output, `importance="critical"`) {
		t.Error("missing importance=critical attribute")
	}
	if !strings.Contains(output, `importance="normal"`) {
		t.Error("missing importance=normal attribute")
	}
	if !strings.Contains(output, `importance="ambient"`) {
		t.Error("missing importance=ambient attribute")
	}
	if !strings.Contains(output, `topic="lint"`) {
		t.Error("missing topic=lint attribute")
	}
	if !strings.Contains(output, `source="murmur"`) {
		t.Error("missing source=murmur attribute")
	}
	if !strings.Contains(output, ">fix rule X</entry>") {
		t.Error("missing entry content for 'fix rule X'")
	}
}

func TestFormatWhispers_AllWhisperTypes(t *testing.T) {
	entries := []whisperstore.WhisperEntry{
		{ID: "1", Type: whisperstore.WhisperTrigger, Topic: "murmur", Content: "fixing auth", Importance: whisperstore.ImportanceNormal, Source: "murmur"},
		{ID: "2", Type: whisperstore.WhisperStructural, Topic: "team-context", Content: "SOUL.md updated", Importance: whisperstore.ImportanceNormal, Source: "team-context"},
		{ID: "3", Type: whisperstore.WhisperTimeBased, Topic: "reminder", Content: "CI check", Importance: whisperstore.ImportanceNormal, Source: "activity-summary"},
	}

	var buf bytes.Buffer
	wrote := formatWhispers(&buf, entries)
	if !wrote {
		t.Fatal("expected formatWhispers to return true")
	}

	output := buf.String()
	if !strings.Contains(output, "fixing auth") {
		t.Error("trigger type whisper not surfaced")
	}
	if !strings.Contains(output, "SOUL.md updated") {
		t.Error("structural type whisper not surfaced")
	}
	if !strings.Contains(output, "CI check") {
		t.Error("time-based type whisper not surfaced")
	}
}

func TestFormatWhispers_MixedTypesAndImportances(t *testing.T) {
	types := []whisperstore.WhisperType{
		whisperstore.WhisperTrigger,
		whisperstore.WhisperStructural,
		whisperstore.WhisperTimeBased,
	}
	importances := []whisperstore.Importance{
		whisperstore.ImportanceCritical,
		whisperstore.ImportanceNormal,
		whisperstore.ImportanceAmbient,
	}

	var entries []whisperstore.WhisperEntry
	id := 0
	for _, typ := range types {
		for _, imp := range importances {
			id++
			entries = append(entries, whisperstore.WhisperEntry{
				ID: string(rune('0' + id)), Type: typ, Topic: string(typ),
				Content: string(imp), Importance: imp, Source: "test",
			})
		}
	}

	var buf bytes.Buffer
	wrote := formatWhispers(&buf, entries)
	if !wrote {
		t.Fatal("expected formatWhispers to return true")
	}

	output := buf.String()
	for _, e := range entries {
		if !strings.Contains(output, ">"+e.Content+"</entry>") {
			t.Errorf("missing entry content: %s", e.Content)
		}
	}
	if !strings.Contains(output, "<system-reminder>") || !strings.Contains(output, "</system-reminder>") {
		t.Error("missing system-reminder wrapper")
	}
}

func TestFormatWhispers_NoWhispers_NoOutput(t *testing.T) {
	var buf bytes.Buffer
	wrote := formatWhispers(&buf, nil)
	if wrote {
		t.Error("expected false for nil entries")
	}
	if buf.Len() > 0 {
		t.Errorf("expected no output, got: %q", buf.String())
	}

	wrote = formatWhispers(&buf, []whisperstore.WhisperEntry{})
	if wrote {
		t.Error("expected false for empty entries")
	}
}

func TestFormatWhispers_SingleImportanceOnly(t *testing.T) {
	tests := []struct {
		name       string
		importance whisperstore.Importance
		wantAttr   string
		noAttrs    []string
	}{
		{"critical only", whisperstore.ImportanceCritical, `importance="critical"`, []string{`importance="normal"`, `importance="ambient"`}},
		{"normal only", whisperstore.ImportanceNormal, `importance="normal"`, []string{`importance="critical"`, `importance="ambient"`}},
		{"ambient only", whisperstore.ImportanceAmbient, `importance="ambient"`, []string{`importance="critical"`, `importance="normal"`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := []whisperstore.WhisperEntry{
				{ID: "1", Topic: "test", Content: "msg", Importance: tt.importance, Source: "test"},
			}
			var buf bytes.Buffer
			formatWhispers(&buf, entries)
			output := buf.String()

			if !strings.Contains(output, tt.wantAttr) {
				t.Errorf("missing expected attribute %q", tt.wantAttr)
			}
			for _, a := range tt.noAttrs {
				if strings.Contains(output, a) {
					t.Errorf("unexpected attribute %q", a)
				}
			}
		})
	}
}

func TestFormatWhispers_TopicAndContentPreserved(t *testing.T) {
	entries := []whisperstore.WhisperEntry{
		{ID: "1", Topic: "lint/eslint", Content: "rule no-unused-vars failing in src/auth/", Importance: whisperstore.ImportanceNormal, Source: "lint"},
		{ID: "2", Topic: "architecture", Content: "API contract v3 rolling out — breaking change in /users endpoint", Importance: whisperstore.ImportanceCritical, Source: "murmur"},
	}

	var buf bytes.Buffer
	formatWhispers(&buf, entries)
	output := buf.String()

	for _, e := range entries {
		if !strings.Contains(output, e.Content) {
			t.Errorf("missing content: %s", e.Content)
		}
		if !strings.Contains(output, `topic="`+e.Topic+`"`) {
			t.Errorf("missing topic attribute: %s", e.Topic)
		}
	}
}

func TestFormatWhispers_LargeContent(t *testing.T) {
	largeContent := strings.Repeat("x", 500) // max murmur size

	entries := []whisperstore.WhisperEntry{
		{ID: "1", Topic: "large-murmur", Content: largeContent, Importance: whisperstore.ImportanceNormal, Source: "murmur"},
	}

	var buf bytes.Buffer
	wrote := formatWhispers(&buf, entries)
	if !wrote {
		t.Fatal("expected formatWhispers to return true")
	}

	output := buf.String()
	if !strings.Contains(output, largeContent) {
		t.Error("large content was truncated")
	}
	if !strings.Contains(output, "<system-reminder>") {
		t.Error("missing system-reminder wrapper around large content")
	}
}

// --- B. Murmur framing and topic hints ---

func TestMurmurTopicHint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		topic string
		want  bool // true if non-empty hint expected
	}{
		{"wip", true},
		{"conflict", true},
		{"architecture", true},
		{"lint", true},
		{"ci", true},
		{"unknown-topic", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.topic, func(t *testing.T) {
			hint := murmurTopicHint(tt.topic)
			if tt.want && hint == "" {
				t.Errorf("expected non-empty hint for topic %q", tt.topic)
			}
			if !tt.want && hint != "" {
				t.Errorf("expected empty hint for topic %q, got %q", tt.topic, hint)
			}
		})
	}
}

func TestFormatWhispers_MurmurFraming(t *testing.T) {
	t.Parallel()

	t.Run("murmur context appears with murmurs", func(t *testing.T) {
		entries := []whisperstore.WhisperEntry{
			{ID: "1", Source: "murmur", Topic: "wip", Content: "working on auth", Importance: whisperstore.ImportanceNormal, AgentID: "OxA7b3"},
		}
		var buf bytes.Buffer
		formatWhispers(&buf, entries)
		output := buf.String()

		if !strings.Contains(output, "<murmur-context>") {
			t.Error("expected <murmur-context> tag when murmurs present")
		}
		if !strings.Contains(output, "ambient awareness") {
			t.Error("expected murmur context framing text")
		}
		if !strings.Contains(output, "CRITICAL entries") {
			t.Error("expected critical importance guidance in murmur context")
		}
	})

	t.Run("murmur context absent without murmurs", func(t *testing.T) {
		entries := []whisperstore.WhisperEntry{
			{ID: "1", Source: "auto-murmur", Topic: "nudge", Content: "nudge content", Importance: whisperstore.ImportanceNormal},
			{ID: "2", Source: "activity-summary", Topic: "activity", Content: "active", Importance: whisperstore.ImportanceNormal},
		}
		var buf bytes.Buffer
		formatWhispers(&buf, entries)
		output := buf.String()

		if strings.Contains(output, "<murmur-context>") {
			t.Error("murmur-context should not appear without murmur entries")
		}
		if strings.Contains(output, "<murmur-topic") {
			t.Error("murmur-topic should not appear without murmur entries")
		}
	})

	t.Run("murmur topic hints for known topics", func(t *testing.T) {
		entries := []whisperstore.WhisperEntry{
			{ID: "1", Source: "murmur", Topic: "wip", Content: "work", Importance: whisperstore.ImportanceNormal},
			{ID: "2", Source: "murmur", Topic: "conflict", Content: "overlap", Importance: whisperstore.ImportanceNormal},
		}
		var buf bytes.Buffer
		formatWhispers(&buf, entries)
		output := buf.String()

		if !strings.Contains(output, `<murmur-topic topic="wip">`) {
			t.Error("expected murmur-topic for wip")
		}
		if !strings.Contains(output, `<murmur-topic topic="conflict">`) {
			t.Error("expected murmur-topic for conflict")
		}
	})

	t.Run("no murmur topic hint for unknown topics", func(t *testing.T) {
		entries := []whisperstore.WhisperEntry{
			{ID: "1", Source: "murmur", Topic: "custom-topic", Content: "custom", Importance: whisperstore.ImportanceNormal},
		}
		var buf bytes.Buffer
		formatWhispers(&buf, entries)
		output := buf.String()

		if !strings.Contains(output, "<murmur-context>") {
			t.Error("expected murmur-context even for unknown topic")
		}
		if strings.Contains(output, "<murmur-topic") {
			t.Error("murmur-topic should not appear for unknown topics")
		}
	})

	t.Run("agent attribute on murmur entries", func(t *testing.T) {
		entries := []whisperstore.WhisperEntry{
			{ID: "1", Source: "murmur", Topic: "wip", Content: "work", Importance: whisperstore.ImportanceNormal, AgentID: "OxA7b3"},
		}
		var buf bytes.Buffer
		formatWhispers(&buf, entries)
		if !strings.Contains(buf.String(), `agent="OxA7b3"`) {
			t.Error("expected agent attribute on murmur entry")
		}
	})

	t.Run("no agent attribute when agent ID empty", func(t *testing.T) {
		entries := []whisperstore.WhisperEntry{
			{ID: "1", Source: "murmur", Topic: "wip", Content: "work", Importance: whisperstore.ImportanceNormal},
		}
		var buf bytes.Buffer
		formatWhispers(&buf, entries)
		if strings.Contains(buf.String(), "agent=") {
			t.Error("agent attribute should be absent when agent ID is empty")
		}
	})

	t.Run("XML round-trip with murmur framing", func(t *testing.T) {
		entries := []whisperstore.WhisperEntry{
			{ID: "1", Source: "murmur", Topic: "wip", Content: "murmur content", Importance: whisperstore.ImportanceNormal, AgentID: "OxA7b3"},
			{ID: "2", Source: "auto-murmur", Topic: "nudge", Content: "nudge", Importance: whisperstore.ImportanceNormal},
		}
		var buf bytes.Buffer
		formatWhispers(&buf, entries)

		var parsed xmlSystemReminder
		if err := xml.Unmarshal(buf.Bytes(), &parsed); err != nil {
			t.Fatalf("failed to parse XML: %v\nraw:\n%s", err, buf.String())
		}

		if parsed.MurmurContext == "" {
			t.Error("expected non-empty murmur-context")
		}
		if len(parsed.MurmurTopics) != 1 {
			t.Errorf("expected 1 murmur-topic, got %d", len(parsed.MurmurTopics))
		} else if parsed.MurmurTopics[0].Topic != "wip" {
			t.Errorf("expected murmur-topic for wip, got %q", parsed.MurmurTopics[0].Topic)
		}
		if len(parsed.Entries) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(parsed.Entries))
		}
		if parsed.Entries[0].Agent != "OxA7b3" {
			t.Errorf("expected agent OxA7b3 on murmur entry, got %q", parsed.Entries[0].Agent)
		}
		if parsed.Entries[1].Agent != "" {
			t.Errorf("expected no agent on non-murmur entry, got %q", parsed.Entries[1].Agent)
		}
	})
}

// --- C. Metadata file surfacing ---
// These tests verify that agents can see which files a murmur affects,
// enabling them to cross-reference against their own active work.

// TestFormatWhispers_MetadataFiles_Rendered verifies file paths from murmur metadata
// appear as an XML attribute agents can read.
// Failure prevented: agents can't identify file-overlap with their own work.
func TestFormatWhispers_MetadataFiles_Rendered(t *testing.T) {
	t.Parallel()

	entries := []whisperstore.WhisperEntry{
		{
			ID: "1", Source: "murmur", Topic: "conflict", Content: "refactoring auth",
			Importance: whisperstore.ImportanceCritical, AgentID: "OxA",
			Metadata: map[string]string{"files": "src/auth/middleware.go,src/auth/validate.go"},
		},
	}
	var buf bytes.Buffer
	formatWhispers(&buf, entries)

	var parsed xmlSystemReminder
	if err := xml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("XML parse failed: %v\nraw:\n%s", err, buf.String())
	}
	if parsed.Entries[0].Files != "src/auth/middleware.go,src/auth/validate.go" {
		t.Errorf("agent should see affected files, got %q", parsed.Entries[0].Files)
	}
}

// TestFormatWhispers_MetadataFiles_AbsentWhenNoMetadata verifies no spurious files
// attribute appears when metadata is nil.
// Failure prevented: agents see empty/garbage files attribute on every entry.
func TestFormatWhispers_MetadataFiles_AbsentWhenNoMetadata(t *testing.T) {
	t.Parallel()

	entries := []whisperstore.WhisperEntry{
		{ID: "1", Source: "murmur", Topic: "wip", Content: "working", Importance: whisperstore.ImportanceNormal, AgentID: "OxB"},
	}
	var buf bytes.Buffer
	formatWhispers(&buf, entries)

	var parsed xmlSystemReminder
	if err := xml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("XML parse failed: %v", err)
	}
	if parsed.Entries[0].Files != "" {
		t.Errorf("no metadata should produce no files attribute, got %q", parsed.Entries[0].Files)
	}
}

// TestFormatWhispers_MetadataFiles_EmptyValueSuppressed verifies that metadata
// with files="" doesn't emit a meaningless attribute.
// Failure prevented: agents waste time parsing empty files attribute.
func TestFormatWhispers_MetadataFiles_EmptyValueSuppressed(t *testing.T) {
	t.Parallel()

	entries := []whisperstore.WhisperEntry{
		{
			ID: "1", Source: "murmur", Topic: "wip", Content: "working",
			Importance: whisperstore.ImportanceNormal,
			Metadata:   map[string]string{"files": ""},
		},
	}
	var buf bytes.Buffer
	formatWhispers(&buf, entries)
	if strings.Contains(buf.String(), "files=") {
		t.Error("empty files value should not produce files attribute")
	}
}

// TestFormatWhispers_MetadataFiles_OtherKeysIgnored verifies that non-files
// metadata keys don't leak into the XML.
// Failure prevented: arbitrary metadata keys appearing as XML attributes.
func TestFormatWhispers_MetadataFiles_OtherKeysIgnored(t *testing.T) {
	t.Parallel()

	entries := []whisperstore.WhisperEntry{
		{
			ID: "1", Source: "murmur", Topic: "wip", Content: "working",
			Importance: whisperstore.ImportanceNormal, AgentID: "OxC",
			Metadata:   map[string]string{"branch": "ryan/auth", "pr": "287"},
		},
	}
	var buf bytes.Buffer
	formatWhispers(&buf, entries)
	output := buf.String()
	if strings.Contains(output, "branch=") || strings.Contains(output, "pr=") {
		t.Error("only 'files' metadata should appear as an attribute; other keys should not leak")
	}
}

// TestFormatWhispers_MetadataFiles_NonMurmurEntry verifies file metadata renders
// on non-murmur entries too (e.g., file-change whispers from daemon).
// Failure prevented: files attribute only works for murmur source, not other whisper types.
func TestFormatWhispers_MetadataFiles_NonMurmurEntry(t *testing.T) {
	t.Parallel()

	entries := []whisperstore.WhisperEntry{
		{
			ID: "1", Source: "file-changes", Topic: "file-changes", Content: "3 files modified",
			Importance: whisperstore.ImportanceNormal,
			Metadata:   map[string]string{"files": "cmd/ox/agent.go"},
		},
	}
	var buf bytes.Buffer
	formatWhispers(&buf, entries)

	var parsed xmlSystemReminder
	if err := xml.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("XML parse failed: %v", err)
	}
	if parsed.Entries[0].Files != "cmd/ox/agent.go" {
		t.Errorf("files attribute should render on non-murmur entries too, got %q", parsed.Entries[0].Files)
	}
}
