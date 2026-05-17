package main

import (
	"io"

	"github.com/sageox/ox/internal/ui"
	"github.com/sageox/ox/internal/version"
)

// renderDoctorHeader writes the branded header with ASCII wordmark and
// version. The wordmark itself lives in internal/ui so the catalog and
// other commands can reuse it — see docs/design/components/wordmark.md.
func renderDoctorHeader(w io.Writer, fixMode bool) {
	ui.WriteWordmark(w, version.Version, fixMode)
}
