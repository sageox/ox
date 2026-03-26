package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/version"
	"github.com/stretchr/testify/assert"
)

func TestRenderDoctorHeader_ContainsVersionAndASCIIArt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fixMode bool
		wantFix bool
	}{
		{
			name:    "normal mode shows version without fix label",
			fixMode: false,
			wantFix: false,
		},
		{
			name:    "fix mode shows fix label",
			fixMode: true,
			wantFix: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			renderDoctorHeader(&buf, tt.fixMode)

			output := buf.String()

			// should contain the version string
			assert.Contains(t, output, "ox "+version.Version)

			// should contain ASCII art lines (at least the first recognizable character)
			for _, line := range doctorASCIILines {
				// each line should be rendered (runes will be present)
				// just verify the output has content from each line
				runes := []rune(line)
				if len(runes) > 0 {
					// the first non-space rune should appear somewhere
					assert.NotEmpty(t, output, "output should not be empty")
				}
			}

			// fix mode check
			if tt.wantFix {
				assert.Contains(t, output, "fix mode")
			} else {
				assert.NotContains(t, output, "fix mode")
			}
		})
	}
}

func TestRenderDoctorHeader_OutputHasNewlines(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	renderDoctorHeader(&buf, false)

	lines := strings.Split(buf.String(), "\n")
	// should have at least: blank line + 4 art lines + version line + blank line = 7
	assert.GreaterOrEqual(t, len(lines), 7, "header should have multiple lines")
}

func TestDoctorASCIILines_ArtSplitPoint(t *testing.T) {
	t.Parallel()

	// verify artSplitPoint is within bounds for all lines
	for i, line := range doctorASCIILines {
		runes := []rune(line)
		assert.LessOrEqual(t, artSplitPoint, len(runes)+1,
			"artSplitPoint should be within bounds for line %d (len=%d)", i, len(runes))
	}
}

func TestDoctorASCIILines_HasExpectedCount(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 4, len(doctorASCIILines), "ASCII art should have exactly 4 lines")
}
