package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/sageox/ox/internal/updatenotice"
	"github.com/sageox/ox/internal/version"
)

const (
	// HeaderMinVersion is returned by the server to indicate the minimum CLI version required
	HeaderMinVersion = "X-SageOx-Min-Version"

	// HeaderDeprecated is returned by the server to warn of upcoming deprecation
	HeaderDeprecated = "X-SageOx-Deprecated"
)

var (
	deprecationShown sync.Once // only show deprecation warning once per session

	// noticeOut is where version notices are written. A var so tests can
	// capture them without swapping the process-global os.Stderr.
	noticeOut io.Writer = os.Stderr
)

// CheckVersionResponse inspects HTTP response for version deprecation signals.
// Returns true if the CLI should abort due to unsupported version (HTTP 426).
// For successful responses, checks for soft deprecation warning header.
func CheckVersionResponse(resp *http.Response) bool {
	// hard block: 426 Upgrade Required
	if resp.StatusCode == http.StatusUpgradeRequired {
		minVersion := resp.Header.Get(HeaderMinVersion)
		PrintUpgradeRequired(minVersion)
		return true
	}

	// soft warning: deprecated but still functional
	if deprecated := resp.Header.Get(HeaderDeprecated); deprecated != "" {
		// The Once only spares us re-reading the ledger for the dozen responses
		// a single command produces. The cross-process cap is the ledger's job.
		deprecationShown.Do(func() { maybeWarnDeprecated(deprecated) })
	}

	return false
}

// maybeWarnDeprecated prints the server's deprecation copy at most once per
// release line per day, and never when nobody is watching stderr.
//
// This is the urgent tier: the server owns the message (it knows the floors and
// the stakes), the client owns only the cadence. Before the ledger, the
// sync.Once above was the ONLY dedup — and for a CLI, where one process is one
// command, that means the warning printed on every single `ox` invocation,
// forever. The ledger is the cross-process memory the Once could never be.
func maybeWarnDeprecated(message string) {
	if updatenotice.Suppressed() {
		return
	}
	line, now := updatenotice.Line(version.Version), time.Now()
	if !updatenotice.ShouldNotify(updatenotice.Read(), line, now) {
		return
	}
	PrintDeprecationWarning(message)
	updatenotice.RecordNotified(line, now)
}

// PrintUpgradeRequired displays a message indicating the CLI version is no longer supported
// Uses red color semantically indicating a blocking error
func PrintUpgradeRequired(minVersion string) {
	red := color.New(color.FgRed, color.Bold)
	redDim := color.New(color.FgRed)

	fmt.Fprintln(noticeOut)
	red.Fprintln(noticeOut, "  ✗ CLI Version No Longer Supported")
	fmt.Fprintln(noticeOut)
	if minVersion != "" {
		redDim.Fprintf(noticeOut, "  Minimum required version: %s\n", minVersion)
	}
	redDim.Fprintln(noticeOut, "  Please upgrade: brew upgrade sageox/tap/ox")
	fmt.Fprintln(noticeOut)
}

// PrintDeprecationWarning displays a warning that the CLI version is deprecated
// Uses yellow color semantically indicating a non-blocking warning
func PrintDeprecationWarning(message string) {
	yellow := color.New(color.FgYellow)
	if message != "" {
		yellow.Fprintf(noticeOut, "  ⚠ Deprecation warning: %s\n", message)
	} else {
		yellow.Fprintln(noticeOut, "  ⚠ This CLI version is deprecated. Please upgrade soon.")
	}
}
