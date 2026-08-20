package format

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadMetadata(t *testing.T) {
	t.Run("empty title is valid", func(t *testing.T) {
		m, err := LoadMetadata(fixture(t, "2026-08-11-22-32-full"))
		if err != nil {
			t.Fatalf("LoadMetadata: %v", err)
		}
		if m == nil {
			t.Fatal("want metadata, got nil")
		}
		if m.Title != "" {
			t.Errorf("Title = %q, want empty", m.Title)
		}
		if m.RecordingID != "rec_019ff2f5-2079-7be1-b05e-8caad2772e61" {
			t.Errorf("RecordingID = %q", m.RecordingID)
		}
	})
	t.Run("missing file is nil nil", func(t *testing.T) {
		m, err := LoadMetadata(t.TempDir())
		if m != nil || err != nil {
			t.Fatalf("got %v, %v; want nil, nil", m, err)
		}
	})
	t.Run("malformed json errors", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, MetadataFileName), []byte("{"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadMetadata(dir); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestLoadSummary(t *testing.T) {
	t.Run("show fields only, unnamed participants skipped", func(t *testing.T) {
		s, err := LoadSummary(fixture(t, "2026-08-11-22-32-full"))
		if err != nil {
			t.Fatalf("LoadSummary: %v", err)
		}
		if s == nil {
			t.Fatal("want summary, got nil")
		}
		if s.Title != "Full Fixture Discussion" {
			t.Errorf("Title = %q", s.Title)
		}
		if !strings.Contains(s.HumanSummary, "fixture discussion") {
			t.Errorf("HumanSummary = %q", s.HumanSummary)
		}
		if got := s.ParticipantNames(); !reflect.DeepEqual(got, []string{"Galex Yen"}) {
			t.Errorf("ParticipantNames = %v, want [Galex Yen]", got)
		}
	})
	t.Run("missing summary is data not error", func(t *testing.T) {
		s, err := LoadSummary(fixture(t, "2026-08-12-01-00-legacy"))
		if s != nil || err != nil {
			t.Fatalf("got %v, %v; want nil, nil", s, err)
		}
	})
	t.Run("nil receiver participant names", func(t *testing.T) {
		var s *Summary
		if got := s.ParticipantNames(); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
}

func TestLoadSummaryMarkdown(t *testing.T) {
	data, err := LoadSummaryMarkdown(fixture(t, "2026-08-12-01-00-legacy"))
	if err != nil {
		t.Fatalf("LoadSummaryMarkdown: %v", err)
	}
	if !strings.Contains(string(data), "Legacy Summary") {
		t.Errorf("summary.md content = %q", data)
	}
	missing, err := LoadSummaryMarkdown(fixture(t, "2026-08-11-22-32-full"))
	if missing != nil || err != nil {
		t.Fatalf("missing summary.md: got %v, %v; want nil, nil", missing, err)
	}
}

func TestInvalidRecordString(t *testing.T) {
	withLine := InvalidRecord{Path: "a.jsonl", Line: 3, Reason: "bad"}
	if got := withLine.String(); got != "a.jsonl:3: bad" {
		t.Errorf("String() = %q", got)
	}
	noLine := InvalidRecord{Path: "b.json", Reason: "bad"}
	if got := noLine.String(); got != "b.json: bad" {
		t.Errorf("String() = %q", got)
	}
}
