package testguard

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCountOpenFDs(t *testing.T) {
	count := countOpenFDs()
	if count < 0 {
		t.Skip("FD counting not supported on this platform")
	}
	// any running process has at least stdin/stdout/stderr
	assert.GreaterOrEqual(t, count, 3, "should have at least 3 FDs (stdin/stdout/stderr)")
}

func TestCountOpenFDs_DetectsDelta(t *testing.T) {
	before := countOpenFDs()
	if before < 0 {
		t.Skip("FD counting not supported on this platform")
	}

	// open 5 FDs
	files := make([]*os.File, 5)
	for i := range files {
		f, err := os.Open("/dev/null")
		if err != nil {
			t.Fatalf("open /dev/null: %v", err)
		}
		files[i] = f
	}

	after := countOpenFDs()
	assert.GreaterOrEqual(t, after-before, 4, "opening 5 files should increase FD count by at least 4")

	// close them
	for _, f := range files {
		f.Close()
	}

	final := countOpenFDs()
	assert.InDelta(t, before, final, 2, "FD count should return to baseline after closing")
}

func TestRequireNoFDLeak_PassesWhenClean(t *testing.T) {
	// verifies the helper doesn't false-positive on a clean test
	RequireNoFDLeak(t)
}
