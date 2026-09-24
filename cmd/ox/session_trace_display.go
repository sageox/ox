package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/cli"
)

func printSessionTraceStatus(w io.Writer, status sessionTraceStatus) error {
	var out strings.Builder
	optIn, optInStyle := "Disabled", "muted"
	if status.Enabled {
		optIn, optInStyle = "Enabled", "success"
	}
	receiver, receiverStyle := "Stopped", "muted"
	if status.Running {
		receiver, receiverStyle = fmt.Sprintf("Listening on 127.0.0.1:%d", status.Port), "success"
	} else if status.Enabled {
		receiver, receiverStyle = "Not running", "warning"
	}
	exporter, exporterStyle := "Needs setup", "warning"
	if status.Exporter.Ready {
		exporter, exporterStyle = "Configured locally", "success"
	}
	out.WriteString(renderTable("Session Tracing", [][]string{
		{"Trace opt-in", optIn, optInStyle},
		{"Receiver", receiver, receiverStyle},
		{"Claude exporter", exporter, exporterStyle},
	}))

	lastReceipt := "Not yet received"
	if status.LastReceiptAgeSeconds != nil {
		lastReceipt = formatDurationHuman(time.Duration(*status.LastReceiptAgeSeconds)*time.Second) + " ago"
	}
	out.WriteString(renderTable("Local Traces", [][]string{
		{"Stored sessions", strconv.Itoa(status.Sessions), ""},
		{"Disk usage", formatSize(status.Bytes), ""},
		{"Last receipt", lastReceipt, ""},
		{"Directory", status.SpoolPath, "muted"},
	}))

	if len(status.Warnings)+len(status.Exporter.Issues) > 0 {
		out.WriteString(renderTable("Warnings", nil))
		for _, warnings := range [][]string{status.Warnings, status.Exporter.Issues} {
			for _, warning := range warnings {
				var text strings.Builder
				if reason, ok := strings.CutPrefix(warning, "retention skipped: "); ok {
					writeWrapped(&text, "  ⚠ ", "    ", "Trace cleanup skipped. This does not stop the receiver from accepting traces.")
					writeTraceStyledLines(&out, cli.StyleWarning, text.String())
					text.Reset()
					writeWrapped(&text, "    ", "    ", reason)
					writeTraceStyledLines(&out, cli.StyleDim, text.String())
				} else {
					writeWrapped(&text, "  ⚠ ", "    ", warning)
					writeTraceStyledLines(&out, cli.StyleWarning, text.String())
				}
			}
		}
	}

	if len(status.Exporter.Notes) > 0 {
		out.WriteString(renderTable("Verification", nil))
		for _, note := range status.Exporter.Notes {
			var text strings.Builder
			writeWrapped(&text, "  • ", "    ", note)
			writeTraceStyledLines(&out, cli.StyleDim, text.String())
		}
	}
	if !status.Exporter.Ready {
		out.WriteString(renderTable("Launch Claude Code", nil))
		out.WriteString("Run from the shell that will start Claude Code:\n\n")
		writeTraceStyledLines(&out, cli.StyleCommand, traceLaunchExample(status.Port))
	}
	_, err := io.WriteString(w, out.String())
	return err
}

// Styling a multiline block pads every line to the longest line. Apply color
// per line so wrapped prose stays compact and shell continuations remain safe
// to copy (no spaces may follow a trailing backslash).
func writeTraceStyledLines(out *strings.Builder, style lipgloss.Style, text string) {
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		out.WriteString(style.Render(line))
		out.WriteByte('\n')
	}
}
