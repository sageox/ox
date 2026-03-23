package main

import (
	"bytes"
	"strings"
	"testing"

	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

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

	// verify XML wrapper
	if !strings.Contains(output, "<new-context>") {
		t.Error("missing <new-context> opening tag")
	}
	if !strings.Contains(output, "</new-context>") {
		t.Error("missing </new-context> closing tag")
	}

	// verify each entry is an <entry> element with attributes
	if !strings.Contains(output, `importance="critical"`) {
		t.Error("missing importance=critical attribute")
	}
	if !strings.Contains(output, `importance="normal"`) {
		t.Error("missing importance=normal attribute")
	}
	if !strings.Contains(output, `importance="ambient"`) {
		t.Error("missing importance=ambient attribute")
	}

	// verify topic attributes
	if !strings.Contains(output, `topic="lint"`) {
		t.Error("missing topic=lint attribute")
	}
	if !strings.Contains(output, `topic="conflict"`) {
		t.Error("missing topic=conflict attribute")
	}

	// verify source attributes
	if !strings.Contains(output, `source="murmur"`) {
		t.Error("missing source=murmur attribute")
	}

	// verify content is element body
	if !strings.Contains(output, ">fix rule X</entry>") {
		t.Error("missing entry content for 'fix rule X'")
	}
	if !strings.Contains(output, ">auth middleware</entry>") {
		t.Error("missing entry content for 'auth middleware'")
	}
	if !strings.Contains(output, ">CI passed</entry>") {
		t.Error("missing entry content for 'CI passed'")
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

	// all types surfaced — none filtered
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
				ID:         string(rune('0' + id)),
				Type:       typ,
				Topic:      string(typ),
				Content:    string(imp),
				Importance: imp,
				Source:     "test",
			})
		}
	}

	var buf bytes.Buffer
	wrote := formatWhispers(&buf, entries)
	if !wrote {
		t.Fatal("expected formatWhispers to return true")
	}

	output := buf.String()

	// all 9 entries should appear as <entry> elements
	for _, e := range entries {
		if !strings.Contains(output, ">"+e.Content+"</entry>") {
			t.Errorf("missing entry content: %s", e.Content)
		}
	}

	// verify XML wrapper present
	if !strings.Contains(output, "<new-context>") {
		t.Error("missing <new-context> tag")
	}
	if !strings.Contains(output, "</new-context>") {
		t.Error("missing </new-context> tag")
	}
}

func TestFormatWhispers_NoWhispers_NoOutput(t *testing.T) {
	var buf bytes.Buffer
	wrote := formatWhispers(&buf, nil)
	if wrote {
		t.Error("expected formatWhispers to return false for nil entries")
	}
	if buf.Len() > 0 {
		t.Errorf("expected no output, got: %q", buf.String())
	}

	wrote = formatWhispers(&buf, []whisperstore.WhisperEntry{})
	if wrote {
		t.Error("expected formatWhispers to return false for empty entries")
	}
	if buf.Len() > 0 {
		t.Errorf("expected no output, got: %q", buf.String())
	}
}

func TestFormatWhispers_SingleImportanceOnly(t *testing.T) {
	tests := []struct {
		name       string
		importance whisperstore.Importance
		wantAttr   string
		noAttrs    []string
	}{
		{
			name:       "critical only",
			importance: whisperstore.ImportanceCritical,
			wantAttr:   `importance="critical"`,
			noAttrs:    []string{`importance="normal"`, `importance="ambient"`},
		},
		{
			name:       "normal only",
			importance: whisperstore.ImportanceNormal,
			wantAttr:   `importance="normal"`,
			noAttrs:    []string{`importance="critical"`, `importance="ambient"`},
		},
		{
			name:       "ambient only",
			importance: whisperstore.ImportanceAmbient,
			wantAttr:   `importance="ambient"`,
			noAttrs:    []string{`importance="critical"`, `importance="normal"`},
		},
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
				t.Errorf("missing expected attribute %q in output:\n%s", tt.wantAttr, output)
			}
			for _, a := range tt.noAttrs {
				if strings.Contains(output, a) {
					t.Errorf("unexpected attribute %q in output", a)
				}
			}
		})
	}
}

func TestFormatWhispers_TopicAndContentPreserved(t *testing.T) {
	entries := []whisperstore.WhisperEntry{
		{ID: "1", Topic: "lint/eslint", Content: "rule no-unused-vars failing in src/auth/", Importance: whisperstore.ImportanceNormal, Source: "lint"},
		{ID: "2", Topic: "architecture", Content: "API contract v3 rolling out — breaking change in /users endpoint", Importance: whisperstore.ImportanceCritical, Source: "murmur"},
		{ID: "3", Topic: "team-context", Content: "Team context updated: docs/conventions.md", Importance: whisperstore.ImportanceNormal, Source: "team-context"},
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
