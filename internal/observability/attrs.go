package observability

import (
	"runtime"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// Span attribute keys for ox CLI traces.
//
// These are exported as constants so call sites in cmd/ox don't typo a key
// and end up with two attributes for the same concept. New keys belong here.
//
// Naming follows two conventions:
//
//   - OTel semantic conventions where they exist (host.os, cli.exit_code).
//   - The "ox.*" namespace for everything else, mirroring how the daemon
//     scopes its task spans.
const (
	// AttrOXVersion is the ox CLI/daemon version string. Always set on the
	// root command span so traces can be sliced by release without joining
	// against build metadata.
	AttrOXVersion = "ox.version"

	// AttrHostOS is the operating system the binary is running on
	// (linux/darwin/windows). Always set on the root command span.
	AttrHostOS = "host.os"

	// AttrHostArch is the CPU architecture the binary was built for
	// (amd64/arm64/...). Cheap to capture and useful for narrowing
	// platform-specific regressions.
	AttrHostArch = "host.arch"

	// AttrCLIExitCode is the integer exit code the CLI process returned.
	// Set unconditionally from main() after Execute returns, on both
	// success (0) and error (non-zero) paths.
	AttrCLIExitCode = "cli.exit_code"

	// AttrCLIArgs is the redacted command-line arguments. Flag NAMES are
	// preserved; flag VALUES and positional arguments are replaced with
	// "<REDACTED>" by RedactArgs. The intent is to make "which flags do
	// users pass to ox code search" queryable without ever exporting user
	// content.
	AttrCLIArgs = "ox.command.args"

	// AttrResultCount is the size of the result set returned by a command
	// that produces results (search, query, list). Set best-effort by
	// individual commands via SetResultCount; not every command has one.
	AttrResultCount = "result.count"
)

// SetCommandAttrs sets the universal attributes that should be present on
// every CLI root span: ox.version, host.os, host.arch, and ox.command.args.
//
// Safe to call when tracing is disabled (no-op if there is no root span).
// Args are redacted via RedactArgs before being attached.
//
// Should be called once per command, immediately after StartCommand. The
// CLI bootstrap in internal/cli.NewContext does this; downstream code does
// not need to.
func SetCommandAttrs(version string, args []string) {
	mu.RLock()
	span := rootSpan
	mu.RUnlock()
	if span == nil {
		return
	}
	redacted := RedactArgs(args)
	span.SetAttributes(
		attribute.String(AttrOXVersion, version),
		attribute.String(AttrHostOS, runtime.GOOS),
		attribute.String(AttrHostArch, runtime.GOARCH),
		attribute.StringSlice(AttrCLIArgs, redacted),
	)
}

// SetExitCode records the process exit code on the root command span.
// Called from main() after rootCmd.Execute returns, so the attribute is
// present on both success and error paths — even when PersistentPostRunE
// never ran (cobra skips PostRunE on RunE errors).
//
// On non-zero exit codes the span status is also marked Error so backends
// can filter for failed commands without inspecting the exit code field.
//
// Safe to call when tracing is disabled.
func SetExitCode(code int) {
	mu.RLock()
	span := rootSpan
	mu.RUnlock()
	if span == nil {
		return
	}
	span.SetAttributes(attribute.Int(AttrCLIExitCode, code))
	if code != 0 {
		span.SetStatus(codes.Error, "non-zero exit")
	}
}

// SetResultCount attaches a result.count attribute to the root command
// span. Best-effort: callers in commands that produce a result set
// (search, query, list) call this once they know the count. Commands
// without a natural result set should not call it.
//
// Safe to call when tracing is disabled.
func SetResultCount(n int) {
	mu.RLock()
	span := rootSpan
	mu.RUnlock()
	if span == nil {
		return
	}
	span.SetAttributes(attribute.Int(AttrResultCount, n))
}
