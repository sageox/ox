package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// payloadLogMaxBytes caps the diagnostics file so a chatty prime cannot grow
// it without bound. When the file is already larger than this on open it is
// truncated — losing old diagnostics is fine, filling a disk is not.
const payloadLogMaxBytes = 1 << 20

// InitPayloadMode reconfigures the process logger for commands whose stdout
// IS an AI coworker's context: `ox agent prime` and `ox agent hook`.
//
// Why: every hook template runs those commands with `2>&1`, so each byte on
// stderr lands in the model's context beside the payload. The eval pilot on
// 2026-09-11 showed what that costs — a single "kb fetch: list failed …
// connection refused" WARN in front of a complete <ox-prime> bundle and the
// model concluded that no team context had loaded at all, scoring 0.25 where
// the run without the plugin scored 1.00. A transient WARN (VPN, offline,
// expired token) must never be able to cancel the payload it precedes.
//
// Policy:
//   - ERROR still goes to stderr — the coworker can act on a hard failure.
//   - WARN and below go to logPath (append, size-capped) so `ox doctor` and
//     a human can still find them; if the file cannot be opened they are
//     dropped rather than leaked to stderr.
//   - verbose keeps everything on stderr: an operator asked for it.
//
// Not applied to other commands: a human running `ox status` should see
// warnings. Only the two payload-producing commands opt in.
func InitPayloadMode(verbose bool, logPath string) {
	if verbose {
		Init(true)
		return
	}

	stderr := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level:       slog.LevelError,
		ReplaceAttr: redactAttr,
	})

	quiet := payloadQuietHandler(logPath)

	newLog := slog.New(&splitHandler{loud: stderr, quiet: quiet})
	log.Store(newLog)
	slog.SetDefault(newLog)
}

// payloadQuietHandler is where WARN and below go: the diagnostics file when
// it can be opened, otherwise nowhere — never stderr.
func payloadQuietHandler(logPath string) slog.Handler {
	f := openPayloadLog(logPath)
	if f == nil {
		return slog.DiscardHandler
	}
	return slog.NewTextHandler(f, &slog.HandlerOptions{
		Level:       slog.LevelWarn,
		ReplaceAttr: redactAttr,
	})
}

// openPayloadLog opens logPath for append, truncating first when it has
// outgrown payloadLogMaxBytes. Returns nil on any failure — callers fall
// back to discarding, never to stderr.
func openPayloadLog(logPath string) io.Writer {
	if logPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return nil
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if info, err := os.Stat(logPath); err == nil && info.Size() > payloadLogMaxBytes {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(logPath, flags, 0o600)
	if err != nil {
		return nil
	}
	return f
}

// splitHandler routes records by level: ERROR and above to loud (stderr),
// everything else to quiet (the diagnostics file or discard).
type splitHandler struct {
	loud  slog.Handler
	quiet slog.Handler
}

func (h *splitHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if level >= slog.LevelError {
		return h.loud.Enabled(ctx, level)
	}
	return h.quiet.Enabled(ctx, level)
}

func (h *splitHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError {
		return h.loud.Handle(ctx, r)
	}
	return h.quiet.Handle(ctx, r)
}

func (h *splitHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &splitHandler{loud: h.loud.WithAttrs(attrs), quiet: h.quiet.WithAttrs(attrs)}
}

func (h *splitHandler) WithGroup(name string) slog.Handler {
	return &splitHandler{loud: h.loud.WithGroup(name), quiet: h.quiet.WithGroup(name)}
}
