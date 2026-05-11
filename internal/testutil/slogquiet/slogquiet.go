// Package slogquiet silences the default slog logger inside test binaries.
//
// Why this exists: production code in this repo calls slog.Info/Warn/Error
// against the slog default logger. In CI those lines flood the test log,
// making real failures impossible to find and burning context tokens for
// any AI coworker reviewing the run via gh run view --log.
//
// How to apply: a test package that exercises code paths emitting slog
// output adds a TestMain that calls Silence(). Set OX_TEST_LOG=1 to opt
// back into log output for local debugging.
package slogquiet

import (
	"log/slog"
	"os"
)

// Silence redirects the slog default logger to the discard handler.
// Honors OX_TEST_LOG=1 to keep logs visible during local debugging.
func Silence() {
	if os.Getenv("OX_TEST_LOG") == "1" {
		return
	}
	slog.SetDefault(slog.New(slog.DiscardHandler))
}
