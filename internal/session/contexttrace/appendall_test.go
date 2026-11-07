package contexttrace

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// TestAppendAll prevents a batched trace write from landing half a batch (or
// an empty file) — a torn context-trace would misreport what ox put in front
// of the AI coworker.
func TestAppendAll(t *testing.T) {
	tests := []struct {
		name      string
		events    []Event
		path      func(dir string) string
		wantErr   bool
		wantFile  bool
		wantLines int
	}{
		{
			name:     "empty batch is a no-op and creates no file",
			events:   nil,
			path:     func(dir string) string { return filepath.Join(dir, FileName) },
			wantFile: false,
		},
		{
			name: "batch lands as one line per event",
			events: []Event{
				{Type: EventInvoked, Source: SourceOxCLI, Command: "murmur list"},
				{Type: EventProvided, Source: SourceTeamContext, Team: "t", Timestamp: "2026-01-01T00:00:00Z"},
				{Type: EventConsulted, Source: SourceOnDemand, Ref: "doc.md"},
			},
			path:      func(dir string) string { return filepath.Join(dir, FileName) },
			wantFile:  true,
			wantLines: 3,
		},
		{
			name: "one unmarshalable event writes nothing",
			events: []Event{
				{Type: EventInvoked, Source: SourceOxCLI},
				{Type: EventConsulted, Source: SourceOnDemand, Score: math.NaN()},
			},
			path:     func(dir string) string { return filepath.Join(dir, FileName) },
			wantErr:  true,
			wantFile: false,
		},
		{
			name:    "open error in a missing directory is returned",
			events:  []Event{{Type: EventInvoked, Source: SourceOxCLI}},
			path:    func(dir string) string { return filepath.Join(dir, "missing", FileName) },
			wantErr: true,
		},
		{
			name:    "open error when the path is a directory is returned",
			events:  []Event{{Type: EventInvoked, Source: SourceOxCLI}},
			path:    func(dir string) string { return dir },
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			p := tt.path(dir)
			w := NewFileWriter(p)
			if w.Path() != p {
				t.Fatalf("Path() = %q, want %q", w.Path(), p)
			}
			err := w.AppendAll(tt.events)
			if (err != nil) != tt.wantErr {
				t.Fatalf("AppendAll err = %v, wantErr %v", err, tt.wantErr)
			}
			if p == dir {
				return
			}
			_, statErr := os.Stat(p)
			if gotFile := statErr == nil; gotFile != tt.wantFile {
				t.Fatalf("file exists = %v, want %v", gotFile, tt.wantFile)
			}
			if !tt.wantFile {
				return
			}
			got, err := ReadEvents(p)
			if err != nil {
				t.Fatalf("ReadEvents: %v", err)
			}
			if len(got) != tt.wantLines {
				t.Fatalf("got %d events, want %d", len(got), tt.wantLines)
			}
			for i, ev := range got {
				if ev.Timestamp == "" {
					t.Errorf("event %d has no timestamp", i)
				}
				if ev.Type != tt.events[i].Type {
					t.Errorf("event %d type = %q, want %q", i, ev.Type, tt.events[i].Type)
				}
			}
			if got[1].Timestamp != "2026-01-01T00:00:00Z" {
				t.Errorf("preset timestamp overwritten: %q", got[1].Timestamp)
			}
		})
	}
}
