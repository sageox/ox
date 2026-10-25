package logger

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStderr swaps os.Stderr for a pipe for the duration of fn and
// returns what was written. Handlers bind the *os.File at construction, so
// InitPayloadMode must be called inside fn.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	os.Stderr = orig
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// Failure prevented: a WARN at session start riding the hook's 2>&1 into the
// model's context and reading as "the payload failed" (eval pilot 2026-09-11,
// case 04: 0.25 with the plugin vs 1.00 without, purely from one WARN line).
func TestInitPayloadMode_WarnGoesToFileNotStderr(t *testing.T) {
	t.Cleanup(func() { Init(false) })
	logPath := filepath.Join(t.TempDir(), "logs", "agent-payload.log")

	stderr := captureStderr(t, func() {
		InitPayloadMode(false, logPath)
		slog.Warn("kb fetch: list failed", "err", "connection refused")
		Warn("package-level warn too")
		slog.Error("hard failure", "err", "boom")
	})

	if strings.Contains(stderr, "kb fetch") || strings.Contains(stderr, "package-level") {
		t.Fatalf("WARN leaked to stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "hard failure") {
		t.Fatalf("ERROR must still reach stderr, got:\n%s", stderr)
	}

	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("diagnostics file not written: %v", err)
	}
	if !strings.Contains(string(logged), "kb fetch") || !strings.Contains(string(logged), "package-level") {
		t.Fatalf("WARN must be preserved in the diagnostics file, got:\n%s", logged)
	}
}

// Failure prevented: an unwritable log location silently re-routing WARN
// back to stderr — the leak this mode exists to stop.
func TestInitPayloadMode_UnopenableLogDropsWarnInsteadOfLeaking(t *testing.T) {
	t.Cleanup(func() { Init(false) })
	// a regular file where the log directory should be → MkdirAll fails
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	stderr := captureStderr(t, func() {
		InitPayloadMode(false, filepath.Join(blocker, "agent-payload.log"))
		slog.Warn("should vanish")
		slog.Error("should surface")
	})

	if strings.Contains(stderr, "should vanish") {
		t.Fatalf("WARN leaked to stderr when the log file was unopenable:\n%s", stderr)
	}
	if !strings.Contains(stderr, "should surface") {
		t.Fatalf("ERROR must still reach stderr, got:\n%s", stderr)
	}
}

// --verbose is an explicit request for everything on stderr; honor it.
func TestInitPayloadMode_VerboseKeepsStderr(t *testing.T) {
	t.Cleanup(func() { Init(false) })
	stderr := captureStderr(t, func() {
		InitPayloadMode(true, filepath.Join(t.TempDir(), "agent-payload.log"))
		slog.Warn("visible when verbose")
	})
	if !strings.Contains(stderr, "visible when verbose") {
		t.Fatalf("verbose must keep WARN on stderr, got:\n%s", stderr)
	}
}

// The cap is the only thing standing between a chatty prime and a full disk.
func TestOpenPayloadLog_TruncatesWhenOversized(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "agent-payload.log")
	if err := os.WriteFile(logPath, bytes.Repeat([]byte("x"), payloadLogMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	w := openPayloadLog(logPath)
	if w == nil {
		t.Fatal("expected a writer")
	}
	if _, err := io.WriteString(w, "fresh\n"); err != nil {
		t.Fatal(err)
	}
	if f, ok := w.(*os.File); ok {
		_ = f.Close()
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fresh\n" {
		t.Fatalf("oversized log must be truncated before append, got %d bytes", len(got))
	}
}
