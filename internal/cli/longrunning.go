package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

// AnnotationLongRunning marks a cobra command whose invocation is a
// long-running service process rather than a discrete command.
//
// A service process blocks for the lifetime of the service — hours or days —
// so cobra's PersistentPostRunE fires at *shutdown*, not at completion of any
// unit of work. Instrumenting it like a normal command reports the service
// lifetime as command latency: `ox daemon start --foreground` was landing in
// command-duration telemetry with a p50 of 70 minutes and a max of 8.8 days,
// swamping every real command in the same aggregate.
//
// Supported values:
//
//	"true"        — every invocation of this command is a service
//	"flag:<name>" — a service only when boolean flag <name> is set
//
// The flag form exists because `ox daemon start` is two different things
// depending on one flag: without --foreground it spawns a child and returns in
// milliseconds (a real command, worth measuring); with --foreground it *is*
// the daemon.
const AnnotationLongRunning = "ox.long_running"

// longRunningFlagPrefix is the "service only when this flag is set" value form.
const longRunningFlagPrefix = "flag:"

// IsLongRunning reports whether this invocation is a long-running service
// process, per the command's AnnotationLongRunning annotation.
//
// Callers use this to suppress command-scoped instrumentation — duration
// telemetry and the wall-clock OTel root span. Service processes carry their
// own instrumentation (the daemon emits ox-daemon spans and its own telemetry
// events), so nothing is lost by skipping the CLI-level wrapper.
func IsLongRunning(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}

	val, ok := cmd.Annotations[AnnotationLongRunning]
	if !ok {
		return false
	}

	if flagName, isFlagForm := strings.CutPrefix(val, longRunningFlagPrefix); isFlagForm {
		if flagName == "" {
			return false
		}
		set, err := cmd.Flags().GetBool(flagName)
		return err == nil && set
	}

	return val == "true"
}
