//go:build slow

package main

import "testing"

// Lines past the former 4 MiB scanner buffer but below the file cap must be
// redacted by both stop and doctor rather than leaving a session stranded.
// Race instrumentation makes the full-length regex pass too slow for the routine gate.
func TestSessionUpload_ScansPastFormerBuffer(t *testing.T) {
	testSessionUploadScansLongLines(t, 4*1024*1024+1024)
}
