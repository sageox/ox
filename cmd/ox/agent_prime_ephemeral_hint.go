package main

import (
	"strings"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/ephemeral"
	"github.com/sageox/ox/internal/prime"
)

// buildEphemeralHint constructs the EphemeralHint emitted in prime output
// when ephemeral.IsEphemeral() returns true. Callers should only invoke
// when in ephemeral mode — the function is conservative (always returns a
// populated hint) since the cost is negligible and the calling agent
// benefits from explicit guidance even when local caches happen to exist.
//
// LocalDataSparse is true when:
//   - the team-context discovery returned nil (no clone, HTTP fallback also failed), or
//   - the team-context info is present but came from the HTTP fallback (signaled
//     by a nil Path on the *prime.TeamContextInfo since the fallback writes to
//     paths.TeamContextDir but the discovery path may still flag it as partial).
//
// The hint always points at the cloud MCP server endpoint derived from the
// active SageOx endpoint. When the MCP route is not yet deployed, the
// agent will get a 404 on first call and fall back to the HTTP team-context
// route — graceful degradation.
func buildEphemeralHint(teamCtx *prime.TeamContextInfo, projectRoot string) *prime.EphemeralHint {
	hint := &prime.EphemeralHint{
		Active: true,
		Reason: ephemeral.Reason(),
	}

	// Local data is sparse when there is no team-context info at all (no clone,
	// no HTTP fallback), OR when the info is present but the on-disk Path is
	// empty (HTTP fallback may write a subset without populating Path).
	if teamCtx == nil || teamCtx.Path == "" {
		hint.LocalDataSparse = true
	}

	hint.CloudMCPEndpoint = mcpEndpointFromAPI(endpoint.GetForProject(projectRoot))
	hint.Recommendation = "Local sync is disabled or partial in ephemeral mode. Prefer the cloud MCP server for context operations (team-context search, codebase search, knowledge-bubble queries). Fall back to local file reads only for operations that are purely on the working tree."
	hint.SuggestedTools = []string{
		"sageox.search_team_context",
		"sageox.search_codebase",
		"sageox.kb_query",
		"sageox.session_history",
	}

	return hint
}

// mcpEndpointFromAPI derives the cloud MCP base URL from the SageOx API
// endpoint. Convention: replace the leading "api." host segment with "mcp."
// when present, otherwise append "/mcp" to the existing host. The MCP
// route also exists under /mcp on the same host as the API for
// self-hosted deployments — the append form covers that case.
func mcpEndpointFromAPI(apiURL string) string {
	if apiURL == "" {
		return ""
	}
	trimmed := strings.TrimRight(apiURL, "/")
	// Standard cloud convention: api.sageox.ai -> api.sageox.ai/mcp
	// (we do not flip subdomains; the route lives under /mcp on the API host).
	return trimmed + "/mcp"
}
