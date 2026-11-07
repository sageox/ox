package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/prime"
	"github.com/sageox/ox/internal/session/contexttrace"
)

// recordPrimeInvocation appends prime's own `invoked` event to the AI
// coworker's usage trace: how many estimated tokens prime put in front of the
// model, split by who controls them, and — when a hook cap trimmed it — the
// untrimmed size and which sections were deferred. The PostToolUse hook skips
// `agent prime` so this is the only event a prime produces. Best-effort.
//
// budget is the prime's final per-source estimate; it is computed over the
// untrimmed document, so when fullBytes is set it describes the full bundle
// and emittedBytes is what actually reached the model.
func recordPrimeInvocation(agentID string, budget *prime.ContextBudget, emittedBytes, fullBytes int, deferred []string) {
	if agentID == "" || !config.TraceConsultationsEnabled() {
		return
	}
	projectRoot, err := findProjectRoot()
	if err != nil || projectRoot == "" {
		slog.Debug("usage trace: prime has no project root", "err", err)
		return
	}

	ev := contexttrace.Event{
		Type:        contexttrace.EventInvoked,
		Source:      contexttrace.SourceOxCLI,
		Command:     "agent prime",
		Label:       "loaded team context",
		OutputBytes: emittedBytes,
		Deferred:    deferred,
	}
	if fullBytes > emittedBytes {
		ev.FullBytes = fullBytes
	}
	if budget != nil && len(budget.BySource) > 0 {
		ev.TokensBySource = make(map[string]int, len(budget.BySource))
		for src, n := range budget.BySource {
			ev.TokensBySource[src] = n
		}
		ev.InlineTokens = budget.Total()
	}
	appendUsageTraceEvent(projectRoot, agentID, ev)
}

// primeDeferredSections names the sections a hook cap left out of an emitted
// prime, read back from its <deferred> pointer ("- <name>[: hint]" lines).
// nil when nothing was deferred.
func primeDeferredSections(emitted string) []string {
	start := strings.LastIndex(emitted, "\n<deferred path=\"")
	if start < 0 {
		return nil
	}
	block := emitted[start:]
	if end := strings.Index(block, "</deferred>"); end >= 0 {
		block = block[:end]
	}
	var names []string
	for _, line := range strings.Split(block, "\n")[1:] {
		name, ok := strings.CutPrefix(line, "- ")
		if !ok {
			continue
		}
		if i := strings.Index(name, ": "); i >= 0 {
			name = name[:i]
		}
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// primeFullBytes is the untrimmed prime's size when a hook cap trimmed the
// emitted one: the full bundle prime just wrote beside the pointer. 0 when
// nothing was deferred (so a stale bundle from an earlier prime is ignored).
func primeFullBytes(emitted, fullBundlePath string) int {
	if fullBundlePath == "" || primeDeferredSections(emitted) == nil {
		return 0
	}
	info, err := os.Stat(fullBundlePath)
	if err != nil {
		return 0
	}
	return int(info.Size())
}
