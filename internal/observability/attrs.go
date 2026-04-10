package observability

import (
	"os"
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

	// AttrAgentEnv identifies the AI coworker that invoked ox, sourced
	// from the AGENT_ENV environment variable that adapter hooks set
	// (claude-code, aider, gemini, codex, ...). Only present when the
	// command is being driven by an AI coworker — human-driven
	// invocations leave this attribute off the span entirely so a
	// `ox.agent.env exists` query in the trace backend cleanly isolates
	// agent traffic from human traffic.
	AttrAgentEnv = "ox.agent.env"

	// AttrCommandSubcommand is the resolved subcommand path for commands
	// that go through a dispatcher. Cobra's PreRunE creates the root
	// span before the dispatcher resolves what was actually invoked, so
	// every dispatcher hit would otherwise share one bland span name
	// (e.g. "ox agent"). RenameRootSpan attaches this attribute alongside
	// the renamed span so backends can group by it even if a future
	// refactor changes the span-name scheme — and so high-cardinality
	// suffixes like hook phase names don't bloat span-name buckets.
	AttrCommandSubcommand = "ox.command.subcommand"
)

// agentEnv returns the value of the AGENT_ENV environment variable, or the
// empty string if it is unset. Centralized in one place so the lookup
// convention is consistent across resource attrs and span attrs.
func agentEnv() string {
	return os.Getenv("AGENT_ENV")
}

// SetCommandAttrs sets the universal attributes that should be present on
// every CLI root span: ox.version, host.os, host.arch, ox.command.args,
// and (when set) ox.agent.env.
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
	attrs := []attribute.KeyValue{
		attribute.String(AttrOXVersion, version),
		attribute.String(AttrHostOS, runtime.GOOS),
		attribute.String(AttrHostArch, runtime.GOARCH),
		attribute.StringSlice(AttrCLIArgs, redacted),
	}
	// Only attach ox.agent.env when the AGENT_ENV env var is set, so
	// human-driven invocations don't pollute trace backends with empty
	// strings. Filter "ox.agent.env exists" to find agent traffic.
	if env := agentEnv(); env != "" {
		attrs = append(attrs, attribute.String(AttrAgentEnv, env))
	}
	span.SetAttributes(attrs...)
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

// RenameRootSpan rewrites the name of the root command span and attaches
// an ox.command.subcommand attribute. Used by command dispatchers — most
// notably the `ox agent <agent_id> <cmd>` dispatcher in cmd/ox/agent.go —
// that need to refine the span name once they have resolved the actual
// invocation.
//
// Why this exists: cobra's PersistentPreRunE creates the root span before
// the dispatcher's RunE runs, so the span starts life with a bland name
// like "ox agent" because cobra only sees the agentCmd. Without renaming,
// every dispatcher hit ends up in one giant bucket in the trace backend
// and you can't answer "which dispatcher path is slow / erroring?".
//
// The subcommand string is also attached as an attribute (AttrCommandSubcommand)
// so dashboards can group by it independently of the span name. Empty
// subcommand strings are not attached.
//
// Should be called BEFORE any early-return paths in the dispatcher
// (unknown agent_id, validation errors, help) so even fast failures end
// up under the right span name.
//
// Safe to call when tracing is disabled.
func RenameRootSpan(name, subcommand string) {
	mu.RLock()
	span := rootSpan
	mu.RUnlock()
	if span == nil {
		return
	}
	if name != "" {
		span.SetName(name)
	}
	if subcommand != "" {
		span.SetAttributes(attribute.String(AttrCommandSubcommand, subcommand))
	}
}
