package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	localtrace "github.com/sageox/ox/internal/trace"
	"github.com/stretchr/testify/require"
)

// A healthy receiver is not proof of Claude telemetry: retain the separate
// exporter check and receipt age, and do not present cleanup failure as a
// receiver failure or hide the file that needs repair.
func TestTraceStatusSeparatesReadinessReceiptAndCleanup(t *testing.T) {
	age := int64(3882)
	var out bytes.Buffer
	require.NoError(t, printSessionTraceStatus(&out, sessionTraceStatus{
		Enabled: true, Running: true, Port: 14318, Sessions: 2, Bytes: 580555,
		SpoolPath: "/cache/trace/spool", LastReceiptAgeSeconds: &age,
		Warnings: []string{"retention skipped: parse session meta file=/ledger/sessions/old/meta.json: unresolved Git conflict markers"},
		Exporter: localtrace.ExportStatus{Ready: true, Notes: []string{"Local checks cannot observe a running Claude Code session."}},
	}))
	text := ansi.Strip(out.String())
	require.Contains(t, text, "Listening on 127.0.0.1:14318")
	require.Contains(t, text, "Configured locally")
	require.Contains(t, text, "1 hour 4 minutes ago")
	require.Contains(t, text, "566.9 KB")
	require.Contains(t, text, "/ledger/sessions/old/meta.json")
	require.Contains(t, text, "Trace cleanup skipped")
	require.Contains(t, text, "does not stop the receiver")
	require.Contains(t, text, "cannot observe a running Claude Code session")
	require.NotContains(t, text, "Launch Claude Code")
}

// Setup guidance must remain directly copyable after styling and wrapping.
// In particular, padding after a shell continuation breaks the launch command.
func TestTraceStatusSetupGuidanceIsCopyable(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		var out bytes.Buffer
		require.NoError(t, printSessionTraceStatus(&out, sessionTraceStatus{
			Enabled: enabled, Port: 15432, SpoolPath: "/cache/trace/spool",
			Exporter: localtrace.ExportStatus{Issues: []string{"OTEL_TRACES_EXPORTER must be otlp for local capture"}},
		}))
		text := ansi.Strip(out.String())
		require.Contains(t, text, "Not yet received")
		require.Contains(t, text, "Needs setup")
		require.Contains(t, text, "OTEL_TRACES_EXPORTER must be otlp")
		require.Contains(t, text, traceLaunchExample(15432))
		if enabled {
			require.Contains(t, text, "Not running")
		} else {
			require.Contains(t, text, "Disabled")
			require.Contains(t, text, "Stopped")
		}
		for _, line := range strings.Split(text, "\n") {
			require.Equal(t, strings.TrimRight(line, " \t"), line, "no styling padding may alter pasted commands")
		}
	}
}

type traceStatusErrorWriter struct{ err error }

func (w traceStatusErrorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestTraceStatusReportsOutputFailure(t *testing.T) {
	writeErr := errors.New("output closed")
	require.ErrorIs(t, printSessionTraceStatus(traceStatusErrorWriter{writeErr}, sessionTraceStatus{}), writeErr)
}
