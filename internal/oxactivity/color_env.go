package oxactivity

import (
	"os"
	"strconv"
	"strings"
)

// ColorAllowedFor is ColorAllowed judged against noColor, the NO_COLOR value
// from the coworker's own environment, instead of the current one.
//
// The ox binary sets NO_COLOR=1 during startup whenever stdout is not a
// terminal or an AI coworker is detected (cmd/ox/main.go init), which is
// always the case inside a harness hook. After that, ColorAllowed reports
// false for every coworker. Callers in the ox binary pass the value captured
// before that override.
func ColorAllowedFor(noColor string) bool {
	if off, _ := strconv.ParseBool(noColor); off {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OX_COLOR_PROFILE"))) {
	case "ascii", "none", "notty":
		return false
	}
	return true
}
