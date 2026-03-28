package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPrintSubsectionBreak_NoPanic(t *testing.T) {
	t.Parallel()
	// PrintSubsectionBreak is a no-op, but should not panic
	assert.NotPanics(t, func() {
		PrintSubsectionBreak()
	})
}

func TestSectionBreakConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "\n", SectionBreak)
	assert.Equal(t, "", SubsectionBreak)
}
