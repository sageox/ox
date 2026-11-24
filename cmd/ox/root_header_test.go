package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/sageox/ox/internal/ui"
)

func TestRootHelpHeader(t *testing.T) {
	tests := []struct {
		name      string
		art       []string
		wantRows  int
		wantLogo  bool
		textStart int // row the name line sits on
	}{
		{name: "text alone", art: nil, wantRows: 2, textStart: 0},
		{name: "mark beside text", art: ui.RenderMark(), wantRows: ui.MarkRows, wantLogo: true, textStart: (ui.MarkRows - 2) / 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := strings.Split(strings.TrimSuffix(ansi.Strip(rootHelpHeader(tt.art)), "\n"), "\n")
			if len(rows) != tt.wantRows {
				t.Fatalf("rows = %d, want %d:\n%s", len(rows), tt.wantRows, strings.Join(rows, "\n"))
			}
			if !strings.Contains(rows[tt.textStart], "ox - Shared team context") {
				t.Errorf("row %d = %q, want the name line", tt.textStart, rows[tt.textStart])
			}
			if !strings.Contains(rows[tt.textStart+1], "Making agentic engineering multiplayer.") {
				t.Errorf("row %d = %q, want the tagline", tt.textStart+1, rows[tt.textStart+1])
			}
			for i, row := range rows {
				if w := ansi.StringWidth(row); w > 80 {
					t.Errorf("row is %d columns, over 80: %q", w, row)
				}
				if tt.wantLogo && !strings.HasPrefix(row, ansi.Strip(tt.art[i])) {
					t.Errorf("row %d = %q, does not start with mark row %q", i, row, ansi.Strip(tt.art[i]))
				}
			}
		})
	}
}

func TestRootHeaderArt_NotForPipes(t *testing.T) {
	// a bytes.Buffer is not a terminal: a pipe, a redirect, an AI coworker
	if art := rootHeaderArt(&bytes.Buffer{}, 200); art != nil {
		t.Fatalf("got art for a non-terminal writer")
	}
}

func TestRootHeaderWidthBeside_FitsIn80(t *testing.T) {
	if w := rootHeaderWidthBeside(ui.RenderMark()); w > 80 {
		t.Fatalf("header with mark is %d columns, over 80", w)
	}
}
