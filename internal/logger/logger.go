package logger

import (
	"log/slog"
	"os"
	"sync/atomic"
)

// log is the package-level logger. Stored as atomic.Pointer so concurrent
// reads (every Debug/Info/Warn/Error call across goroutines, including the
// telemetry flush worker) cannot race with Init's write at CLI startup.
// Plain *slog.Logger here trips the race detector under test runs that
// drive cobra (each new test invocation re-runs Init while a previous
// test's telemetry goroutine is still logging).
var log atomic.Pointer[slog.Logger]

func init() {
	// initialize with a default logger (warn level, text handler) so log
	// is never nil even if Init() is not called.
	log.Store(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	})))
}

func Init(verbose bool) {
	level := slog.LevelWarn
	if verbose {
		level = slog.LevelDebug
	}

	// use JSON handler for production, text for development
	var handler slog.Handler
	if os.Getenv("OX_ENV") == "production" {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: level,
		})
	} else {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: level,
		})
	}

	newLog := slog.New(handler)
	log.Store(newLog)
	slog.SetDefault(newLog)
}

func Info(msg string, args ...any) {
	log.Load().Info(msg, args...)
}

func Debug(msg string, args ...any) {
	log.Load().Debug(msg, args...)
}

func Warn(msg string, args ...any) {
	log.Load().Warn(msg, args...)
}

func Error(msg string, args ...any) {
	log.Load().Error(msg, args...)
}
