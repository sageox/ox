package main

import (
	"fmt"
	"os"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/daemon"
)

// ShowErrorBannerIfNeeded displays recent daemon errors at the start of command output.
// This is non-blocking: if the daemon isn't running, it returns immediately.
//
// Design: CLI commands block the agent's event loop, so this must be fast (< 1ms).
// We use TryConnect which returns nil if daemon isn't running.
func ShowErrorBannerIfNeeded() {
	client := daemon.TryConnect()
	if client == nil {
		// daemon not running - not an error, just skip banner
		return
	}

	errors, err := client.GetUnviewedErrors()
	if err != nil {
		// couldn't get errors - daemon might be busy, skip banner
		return
	}

	if len(errors) == 0 {
		return
	}

	// show banner with most recent error (errors are sorted by timestamp desc)
	mostRecent := errors[0]
	age := timeSinceError(mostRecent.Timestamp)

	// format based on severity
	switch mostRecent.Severity {
	case "error":
		fmt.Fprintf(os.Stderr, "%s %s (%s ago)\n",
			cli.StyleError.Render("[!]"),
			mostRecent.Message,
			age,
		)
	default: // warning
		fmt.Fprintf(os.Stderr, "%s %s (%s ago)\n",
			cli.StyleWarning.Render("[!]"),
			mostRecent.Message,
			age,
		)
	}

	// if multiple errors, mention the count
	if len(errors) > 1 {
		fmt.Fprintf(os.Stderr, "%s\n",
			cli.StyleDim.Render(fmt.Sprintf("    Run 'ox daemon logs' to see all %d errors", len(errors))),
		)
	}
}

// timeSinceError formats the time since an error as a human-readable string.
func timeSinceError(t time.Time) string {
	diff := time.Since(t)

	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", mins)
	case diff < 24*time.Hour:
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	default:
		days := int(diff.Hours() / 24)
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	}
}

// GetUnviewedErrorCount returns the number of unviewed daemon errors.
// Returns 0 if daemon isn't running or errors couldn't be retrieved.
// This is non-blocking and intended for quick status checks.
func GetUnviewedErrorCount() int {
	client := daemon.TryConnect()
	if client == nil {
		return 0
	}

	errors, err := client.GetUnviewedErrors()
	if err != nil {
		return 0
	}

	return len(errors)
}

// MarkAllErrorsViewed marks all daemon errors as viewed.
// Returns true if successful, false if daemon isn't running or operation failed.
func MarkAllErrorsViewed() bool {
	client := daemon.TryConnect()
	if client == nil {
		return false
	}

	err := client.MarkErrorsViewed(nil) // nil means mark all
	return err == nil
}
