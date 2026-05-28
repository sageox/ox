package main

import (
	"os"
	"strings"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/ephemeral"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/prime"
	"github.com/sageox/ox/internal/runtime"
)

// buildEphemeralHint constructs the EphemeralHint emitted in prime output.
//
// The hint is split into two concerns:
//
//  1. *Always-emit* fields that surface the cloud MCP server as a context
//     source. CloudMCPEndpoint and SuggestedTools come back populated even
//     on a dev laptop with a fully synced local clone — MCP is a valid
//     source there too, just not the preferred one. A laptop developer
//     with a slow team-context clone benefits from knowing MCP is
//     available; the old code hid the endpoint from them.
//
//  2. *Constraint-gated* fields (Active, Reason, LocalDataSparse,
//     Recommendation). These fire only when the runtime cannot
//     reliably satisfy context queries locally — no persistent disk OR
//     no daemon to keep caches warm. That's the original semantic of
//     ephemeral.IsEphemeral(); the call site below mirrors it via
//     runtime.Caps() so the dependency arrow stays one-way.
//
// LocalDataSparse historically read "is teamCtx.Path empty?", which
// was wrong: the HTTP fallback writes files into paths.TeamContextDir()
// without populating Path. We now check whether the on-disk team-context
// directory actually has any files. That matches what the *calling*
// agent is asking — "do I have data locally I can grep, or should I
// route to MCP?"
//
// When the cloud MCP route is not yet deployed for the active endpoint,
// the agent will get a 404 on first call and fall back to the HTTP
// team-context route — graceful degradation, no extra plumbing here.
func buildEphemeralHint(teamCtx *prime.TeamContextInfo, projectRoot string) *prime.EphemeralHint {
	caps := runtime.Caps()
	constrained := !caps.PersistDisk || !caps.DaemonViable

	hint := &prime.EphemeralHint{
		// Always populated so the agent can route to MCP even on a
		// laptop. Active/Reason/etc reflect whether we're *forcing* the
		// route or merely offering it.
		CloudMCPEndpoint: mcpEndpointFromAPI(endpoint.GetForProject(projectRoot)),
		SuggestedTools:   prime.CloudMCPSuggestedTools,
	}

	if !constrained {
		return hint
	}

	hint.Active = true
	hint.Reason = ephemeral.Reason()
	hint.LocalDataSparse = isTeamContextSparse(teamCtx)
	hint.Recommendation = "Local sync is disabled or partial in ephemeral mode. Prefer the cloud MCP server for context operations (team-context search, codebase search, knowledge-bubble queries). Fall back to local file reads only for operations that are purely on the working tree."
	return hint
}

// isTeamContextSparse decides whether the agent should treat the local
// team-context tree as a usable source. The right question to ask is
// "does the on-disk directory actually contain content?" — checking
// teamCtx.Path alone misses the HTTP-fallback case where the discovery
// layer wrote files but didn't set Path.
//
// We deliberately do NOT check the directory recursively or measure
// content quality. A directory with at least one entry beats nothing,
// and a more thorough check would couple this heuristic to the
// team-context layout. The cost of a false negative (we say sparse
// when content exists) is the agent prefers MCP for one query; the
// cost of a false positive (we say not-sparse when content is missing)
// is a noisy fallback the agent has to recover from on its own. Both
// are cheap.
func isTeamContextSparse(teamCtx *prime.TeamContextInfo) bool {
	if teamCtx == nil {
		return true
	}
	// trust an explicit Path when set — that's the canonical clone case
	if teamCtx.Path != "" {
		return !dirHasEntries(teamCtx.Path)
	}
	// HTTP fallback case: discovery wrote files under paths.TeamContextDir
	// but left Path empty. Probe the canonical directory directly.
	if teamCtx.TeamID == "" {
		return true
	}
	dir := paths.TeamContextDir(teamCtx.TeamID, "")
	return !dirHasEntries(dir)
}

func dirHasEntries(dir string) bool {
	if dir == "" {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	// hidden-only ("." prefix) shouldn't fool us — but in practice any
	// non-empty team context has AGENTS.md / agents/ / etc., so a single
	// regular entry is sufficient signal.
	for range entries {
		// any single entry is enough — content quality probing belongs
		// elsewhere
		return true
	}
	return false
}

// mcpEndpointFromAPI derives the cloud MCP base URL from the SageOx API
// endpoint. Convention: append "/mcp" to the existing host. The MCP
// route lives under /mcp on the same host as the API for both cloud
// and self-hosted deployments.
func mcpEndpointFromAPI(apiURL string) string {
	if apiURL == "" {
		return ""
	}
	trimmed := strings.TrimRight(apiURL, "/")
	return trimmed + "/mcp"
}
