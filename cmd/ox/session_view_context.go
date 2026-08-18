package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/theme"
)

// semantic styles for context trace (aligned with murmur list palette)
var (
	ctxSourceTeamStyle = lipgloss.NewStyle().
				Foreground(cli.ColorSecondary).
				Bold(true)

	ctxSourceWhisperStyle = lipgloss.NewStyle().
				Foreground(cli.ColorAccent)

	ctxSourceDocsStyle = lipgloss.NewStyle().
				Foreground(cli.ColorInfo)

	ctxSourceConfigStyle = lipgloss.NewStyle().
				Foreground(cli.ColorDim)

	ctxDocNameStyle = lipgloss.NewStyle().
			Foreground(theme.Color("#CCCCCC"))

	ctxDecisionStyle = lipgloss.NewStyle().
				Foreground(theme.Color("#CCCCCC"))

	ctxTokenStyle = lipgloss.NewStyle().
			Foreground(cli.ColorInfo)
)

// sourceStyle returns the appropriate semantic style for a context source type.
func sourceStyle(source contexttrace.SourceType) lipgloss.Style {
	switch source {
	case contexttrace.SourceTeamContext, contexttrace.SourceTeamMemory:
		return ctxSourceTeamStyle
	case contexttrace.SourceTeamWhisper, contexttrace.SourceProjectWhisper:
		return ctxSourceWhisperStyle
	case contexttrace.SourceTeamDocs, contexttrace.SourceOnDemand:
		return ctxSourceDocsStyle
	case contexttrace.SourceProjectConfig:
		return ctxSourceConfigStyle
	default:
		return ctxDocNameStyle
	}
}

// resolveContextTrace finds and reads context-trace events for a session.
// Checks: store primary/cache → ledger cache → ledger sessions → LFS hydration.
func resolveContextTrace(store *session.Store, sessionName, projectRoot string) ([]contexttrace.Event, error) {
	// try store's primary dir and cache via ResolveContentFile
	if store != nil {
		path := store.ResolveContentFile(sessionName, contexttrace.FileName)
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			if !lfs.IsPointerFile(path) {
				return contexttrace.ReadEvents(path)
			}
		}
	}

	// try ledger cache (recordings land here before upload)
	ledgerPath, err := resolveLedgerPath()
	if err != nil {
		return nil, nil // no ledger available
	}

	ledgerCachePath := filepath.Join(ledgerPath, ".sageox", "cache", "sessions", sessionName, contexttrace.FileName)
	if info, err := os.Stat(ledgerCachePath); err == nil && info.Size() > 0 {
		return contexttrace.ReadEvents(ledgerCachePath)
	}

	// try ledger sessions dir (uploaded sessions)
	sessionsDir := filepath.Join(ledgerPath, "sessions")
	ledgerSessionPath := filepath.Join(sessionsDir, sessionName, contexttrace.FileName)
	if info, err := os.Stat(ledgerSessionPath); err == nil && info.Size() > 0 {
		if !lfs.IsPointerFile(ledgerSessionPath) {
			return contexttrace.ReadEvents(ledgerSessionPath)
		}
	}

	// check meta.json manifest before attempting LFS hydration
	sessionDir := filepath.Join(sessionsDir, sessionName)
	meta, metaErr := lfs.ReadSessionMeta(sessionDir)
	if metaErr != nil {
		return nil, nil // no meta.json = old session or not in ledger
	}
	if _, hasTrace := meta.Files[contexttrace.FileName]; !hasTrace {
		return nil, nil // session exists but has no context trace artifact
	}

	// meta confirms context-trace exists in LFS — hydrate it
	if err := hydrateFromLedger(projectRoot, sessionsDir, sessionName, true); err != nil {
		return nil, fmt.Errorf("download context trace: %w", err)
	}

	// re-resolve after hydration (hydrated files go to ledger cache)
	if info, err := os.Stat(ledgerCachePath); err == nil && info.Size() > 0 {
		return contexttrace.ReadEvents(ledgerCachePath)
	}
	if store != nil {
		path := store.ResolveContentFile(sessionName, contexttrace.FileName)
		if _, err := os.Stat(path); err == nil {
			return contexttrace.ReadEvents(path)
		}
	}

	return nil, nil
}

// viewContextTraceText renders context-trace events as formatted terminal output.
func viewContextTraceText(w io.Writer, events []contexttrace.Event, sessionName string) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, showTitleStyle.Render("Context Trace"))
	fmt.Fprintln(w, showSeparatorStyle.Render(strings.Repeat("-", 60)))
	fmt.Fprintln(w)
	printShowField(w, "Session", sessionName)
	printShowField(w, "Events", fmt.Sprintf("%d", len(events)))
	fmt.Fprintln(w)

	// partition events
	var provided, influenced []contexttrace.Event
	for _, ev := range events {
		switch ev.Type {
		case contexttrace.EventInfluenced:
			influenced = append(influenced, ev)
		default:
			provided = append(provided, ev)
		}
	}

	// provided context
	if len(provided) > 0 {
		fmt.Fprintln(w, showSectionStyle.Render("Provided Context"))
		fmt.Fprintln(w, showSeparatorStyle.Render(strings.Repeat("-", 40)))
		fmt.Fprintln(w)

		for _, ev := range provided {
			printProvidedEvent(w, ev)
		}
		fmt.Fprintln(w)
	}

	// influenced decisions
	if len(influenced) > 0 {
		fmt.Fprintln(w, showSectionStyle.Render("Influenced Decisions"))
		fmt.Fprintln(w, showSeparatorStyle.Render(strings.Repeat("-", 40)))
		fmt.Fprintln(w)

		for _, ev := range influenced {
			printInfluencedEvent(w, ev)
		}
		fmt.Fprintln(w)
	}

	// summary
	fmt.Fprintln(w, showSeparatorStyle.Render(strings.Repeat("-", 60)))
	fmt.Fprintf(w, "  %s provided, %s influenced decisions\n",
		showHighlightStyle.Render(fmt.Sprintf("%d sources", len(provided))),
		showHighlightStyle.Render(fmt.Sprintf("%d", len(influenced))))
	fmt.Fprintln(w)
}

func printProvidedEvent(w io.Writer, ev contexttrace.Event) {
	ts := formatTraceTimestamp(ev.Timestamp)
	style := sourceStyle(ev.Source)

	// source badge + doc name
	line := "  " + style.Render(string(ev.Source))

	if ev.Doc != "" {
		line += " " + ctxDocNameStyle.Render(ev.Doc)
	} else if len(ev.Docs) > 0 {
		line += " " + ctxDocNameStyle.Render(strings.Join(ev.Docs, ", "))
	}

	if ev.Section != "" {
		line += " " + cli.StyleDim.Render("("+ev.Section+")")
	}

	if ts != "" {
		line += " " + showEntryTimestampStyle.Render(ts)
	}

	fmt.Fprintln(w, line)

	// details line
	var details []string
	if ev.InlineTokens > 0 {
		details = append(details, ctxTokenStyle.Render(fmt.Sprintf("%d tokens", ev.InlineTokens)))
	}
	if ev.ReadOnDemand {
		details = append(details, cli.StyleDim.Render("read-on-demand"))
	}
	if ev.From != "" {
		details = append(details, cli.StyleDim.Render(fmt.Sprintf("from %s", ev.From)))
	}
	if ev.Topic != "" {
		details = append(details, cli.StyleDim.Render(fmt.Sprintf("topic: %s", ev.Topic)))
	}

	if len(details) > 0 {
		fmt.Fprintf(w, "    %s\n", strings.Join(details, " "+cli.StyleDim.Render("|")+" "))
	}
}

func printInfluencedEvent(w io.Writer, ev contexttrace.Event) {
	ts := formatTraceTimestamp(ev.Timestamp)
	style := sourceStyle(ev.Source)

	line := "  " + style.Render(string(ev.Source))

	if ev.Doc != "" {
		line += " " + ctxDocNameStyle.Render(ev.Doc)
	}

	if ts != "" {
		line += " " + showEntryTimestampStyle.Render(ts)
	}

	fmt.Fprintln(w, line)

	if ev.Decision != "" {
		decision := ev.Decision
		if len(decision) > 200 {
			decision = decision[:197] + "..."
		}
		fmt.Fprintf(w, "    %s\n", ctxDecisionStyle.Render(decision))
	}

	if ev.PlanStep > 0 {
		fmt.Fprintf(w, "    %s\n", cli.StyleDim.Render(fmt.Sprintf("plan step %d", ev.PlanStep)))
	}
}

// contextTraceJSONOutput is the JSON structure for --context --json output.
type contextTraceJSONOutput struct {
	SessionName string               `json:"session_name"`
	EventCount  int                  `json:"event_count"`
	Provided    int                  `json:"provided"`
	Influenced  int                  `json:"influenced"`
	Events      []contexttrace.Event `json:"events"`
}

// viewContextTraceJSON renders context-trace events as structured JSON.
func viewContextTraceJSON(w io.Writer, events []contexttrace.Event, sessionName string, limit int) error {
	var provided, influenced int
	for _, ev := range events {
		switch ev.Type {
		case contexttrace.EventInfluenced:
			influenced++
		default:
			provided++
		}
	}

	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}

	out := contextTraceJSONOutput{
		SessionName: sessionName,
		EventCount:  provided + influenced,
		Provided:    provided,
		Influenced:  influenced,
		Events:      events,
	}

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(out)
}

func formatTraceTimestamp(ts string) string {
	if ts == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return parsed.Format("15:04:05")
}
