package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/search"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/decision"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/ledgersearch"
	"github.com/sageox/ox/internal/observability"
)

// queryArgs holds parsed arguments for the query command.
type queryArgs struct {
	query    string
	mode     string
	limit    int
	teamID   string
	repoID   string
	source   string // "team" (default), "code", "local", "all"
	jsonOnly bool   // emit machine-readable JSON, no decoration
}

// parseQueryArgs extracts flags and the positional query from raw args.
// --limit not --k: self-describing flag names over ML jargon;
// agents and humans guess --limit first
func parseQueryArgs(args []string) (*queryArgs, error) {
	qa := &queryArgs{
		mode:   "hybrid",
		limit:  5,
		source: "team",
	}

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--mode" && i+1 < len(args):
			qa.mode = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--mode="):
			qa.mode = strings.TrimPrefix(args[i], "--mode=")
		// TODO(ox-54a): move --k alias to friction catalog once hand-crafted catalog merging lands
		case (args[i] == "--limit" || args[i] == "--k") && i+1 < len(args):
			v, err := strconv.Atoi(args[i+1])
			if err != nil {
				return nil, fmt.Errorf("invalid --limit value %q: must be an integer", args[i+1])
			}
			qa.limit = v
			i++
		case strings.HasPrefix(args[i], "--limit=") || strings.HasPrefix(args[i], "--k="):
			raw := strings.TrimPrefix(strings.TrimPrefix(args[i], "--limit="), "--k=")
			v, err := strconv.Atoi(raw)
			if err != nil {
				return nil, fmt.Errorf("invalid --limit value %q: must be an integer", raw)
			}
			qa.limit = v
		case args[i] == "--team" && i+1 < len(args):
			qa.teamID = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--team="):
			qa.teamID = strings.TrimPrefix(args[i], "--team=")
		case args[i] == "--repo" && i+1 < len(args):
			qa.repoID = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--repo="):
			qa.repoID = strings.TrimPrefix(args[i], "--repo=")
		case args[i] == "--source" && i+1 < len(args):
			qa.source = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--source="):
			qa.source = strings.TrimPrefix(args[i], "--source=")
		case args[i] == "--local":
			qa.source = "local"
		case args[i] == "--json":
			qa.jsonOnly = true
		case !strings.HasPrefix(args[i], "--"):
			qa.query = args[i]
		}
	}

	if qa.query == "" {
		return nil, fmt.Errorf("query text is required")
	}

	switch qa.mode {
	case "hybrid", "knn", "bm25":
		// ok
	default:
		return nil, fmt.Errorf("invalid mode %q: must be hybrid, knn, or bm25", qa.mode)
	}

	// normalize alias: teamctx is shorthand agents may use
	if qa.source == "teamctx" {
		qa.source = "team"
	}
	// accept "local-ledger" as a more explicit synonym for --source=local
	if qa.source == "local-ledger" {
		qa.source = "local"
	}

	switch qa.source {
	case "all", "team", "code", "local":
		// ok
	default:
		return nil, fmt.Errorf("invalid source %q: must be all, team, code, or local", qa.source)
	}

	return qa, nil
}

const queryUsage = `Usage: ox query "search text" [flags]

Flags:
  --limit N      Max results to return (default: 5)
  --mode MODE    Search mode: hybrid, knn, or bm25 (default: hybrid)
  --team ID      Team ID to search (default: project config, else the credential's team)
  --repo ID      Repo ID to search (default: from project config)
  --source SRC   Search source: team (default), code, local, all
  --local        Shorthand for --source=local (zero-network ledger search)
  --json         Emit machine-readable JSON only (no decoration)

Sources:
  team      Search team discussions, docs, and session history (default)
  code      Search local code index only (queries)
  local     Search locally-cached ledger only (zero network calls)
  all       Search both team context and local code index

Searches across team discussions, docs, and session history.
For code search, use: ox code search "<pattern>" or --source=code
For pre-prompt hooks needing instant local context, use: --local

Also available as: ox agent <id> query "search text"`

// combinedQueryResponse holds results from both team context and local code search.
type combinedQueryResponse struct {
	TeamContext  *api.QueryResponse    `json:"team_context,omitempty"`
	CodeResults  []search.Result       `json:"code_results,omitempty"` // used by --full-json only
	LocalResults []ledgersearch.Result `json:"local_results,omitempty"`
}

// compactQueryResponse is the default agent query output — minimal context footprint.
type compactQueryResponse struct {
	TeamContext  *api.QueryResponse    `json:"team_context,omitempty"`
	CodeResults  []compactSearchResult `json:"code_results,omitempty"`
	LocalResults []ledgersearch.Result `json:"local_results,omitempty"`
	Guidance     string                `json:"guidance,omitempty"`
}

// runAgentQuery handles `ox agent <id> query "search text"`.
// Thin wrapper around executeQuery that adds context byte tracking.
func runAgentQuery(inst *agentinstance.Instance, args []string) error {
	qa, err := parseQueryArgs(args)
	if err != nil {
		return fmt.Errorf("%w\n\n%s", err, queryUsage)
	}

	outputBytes, err := executeQuery(qa, inst.AgentID, inst.AgentType)
	if err != nil {
		return err
	}

	// track context bytes for agent-specific cumulative tracking
	slog.Debug("query response context cost", "agent_id", inst.AgentID, "bytes", outputBytes)
	trackContextBytes(int64(outputBytes))
	return nil
}

// executeQuery performs the core query: resolves project config, searches team context
// and/or local code index based on --source flag, and writes JSON results to stdout.
// Returns bytes written for context tracking.
// agentID and agentType are optional — passed to the server for analytics.
func executeQuery(qa *queryArgs, agentID string, agentType string) (int, error) {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return 0, fmt.Errorf("could not find project root: %w", err)
	}

	if qa.limit <= 0 {
		return 0, fmt.Errorf("invalid --limit: must be > 0")
	}

	combined := &combinedQueryResponse{}

	// --source=local is a zero-network fast path: search ONLY the cached ledger
	// (sessions + murmurs). Used by UserPromptSubmit hooks where latency matters.
	// Fail-open: empty cache or read error returns empty results, exit 0.
	if qa.source == "local" {
		results := queryLocalLedger(qa, projectRoot)
		combined.LocalResults = results
		return writeQueryResponse(combined, qa)
	}

	// query team context if source is "all" or "team"
	if qa.source == "all" || qa.source == "team" {
		resp, err := queryTeamContext(qa, projectRoot, agentID, agentType)
		if err != nil {
			if qa.source == "team" {
				return 0, fmt.Errorf("team context query failed: %w", err)
			}
			slog.Warn("team context query failed, continuing with code search", "error", err)
		} else {
			combined.TeamContext = resp
		}
	}

	// query local code index if source is "all" or "code"
	var codeResults []search.Result
	if qa.source == "all" || qa.source == "code" {
		results, err := queryCodeDB(qa, projectRoot)
		if err != nil {
			if qa.source == "code" {
				return 0, fmt.Errorf("code search failed: %w", err)
			}
			slog.Warn("code search failed, continuing with team context", "error", err)
		} else {
			codeResults = results
		}
	}

	combined.CodeResults = codeResults
	return writeQueryResponse(combined, qa)
}

// writeQueryResponse marshals the combined response and writes JSON to stdout.
// Returns bytes written for context tracking.
func writeQueryResponse(combined *combinedQueryResponse, qa *queryArgs) (int, error) {
	resp := compactQueryResponse{
		TeamContext:  combined.TeamContext,
		LocalResults: combined.LocalResults,
	}
	if len(combined.CodeResults) > 0 {
		compact := compactSearchResults(combined.CodeResults, qa.limit, 0, decision.PathMatcher(findGitRoot()))
		resp.CodeResults = compact.Results
		resp.Guidance = compact.Guidance
	}

	// Attach combined result count to the root span. Counts the *raw* results
	// before --limit so the metric reflects what the search actually returned.
	totalResults := len(combined.CodeResults) + len(combined.LocalResults)
	if combined.TeamContext != nil {
		totalResults += len(combined.TeamContext.Results)
	}
	observability.SetResultCount(totalResults)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(resp); err != nil {
		return 0, fmt.Errorf("failed to encode response: %w", err)
	}

	outputBytes := buf.Len()
	_, err := buf.WriteTo(os.Stdout)
	return outputBytes, err
}

// errNoTeamOrRepoID is the answer when nothing — flags, project config, or the
// credential itself — names a team or a repo to search. `ox init` and the
// explicit flags are the only remedies left at that point.
var errNoTeamOrRepoID = errors.New("no team or repo ID available. Run 'ox init' first or pass --team/--repo flags")

// teamIDFromCredential asks the introspection endpoint which team the bearer
// credential authenticates as. Only a team-scoped credential names one.
//
// errNoTeamOrRepoID is reserved for the one case it describes: introspection
// ANSWERED, and the answer named no single team. Every way of failing to get an
// answer — offline, rejected, unreadable — keeps its own error, because none of
// them is an initialization problem and `ox init` cannot fix any of them (it
// needs a working credential itself). Collapsing them would tell a coworker
// holding a revoked token to run the one command that will fail the same way.
func teamIDFromCredential(ep, accessToken string) (string, error) {
	res, err := auth.Introspect(ep, accessToken)
	if err != nil {
		if errors.Is(err, auth.ErrEndpointUnreachable) {
			return "", fmt.Errorf("no team or repo ID available and %s could not be reached to read the one your credential is bound to: %w",
				endpoint.NormalizeEndpoint(ep), err)
		}
		// Rejected credential or an unreadable answer. Introspect deliberately
		// carries no sentinel to tell those apart (the server returns a uniform
		// 401 so a caller cannot enumerate which credentials exist), so name
		// the likely remedy conditionally rather than assert the wrong one for
		// what may be a protocol fault. Its message is already redacted.
		return "", fmt.Errorf("could not read the team your credential is bound to: %w (run 'ox login' if it is stale)", err)
	}
	if res.Team == nil || res.Team.TeamID == "" {
		return "", errNoTeamOrRepoID
	}
	slog.Debug("team id defaulted from credential", "team_id", res.Team.TeamID)
	return res.Team.TeamID, nil
}

// queryTeamContext searches team context via the vector search API.
func queryTeamContext(qa *queryArgs, projectRoot, agentID, agentType string) (*api.QueryResponse, error) {
	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("could not load project config: %w", err)
	}

	if qa.teamID == "" {
		qa.teamID = cfg.TeamID
	}
	if qa.repoID == "" {
		qa.repoID = cfg.RepoID
	}

	req := &api.QueryRequest{
		Query:     qa.query,
		Mode:      qa.mode,
		K:         qa.limit,
		AgentID:   agentID,
		AgentType: agentType,
	}
	if qa.teamID != "" {
		req.Teams = []string{qa.teamID}
	}
	if qa.repoID != "" {
		req.Repos = []string{qa.repoID}
	}

	ep := endpoint.GetForProject(projectRoot)
	token, err := auth.EnsureValidTokenForEndpoint(ep, 300)
	if err != nil || token == nil || token.AccessToken == "" {
		return nil, fmt.Errorf("not authenticated. Run 'ox login' first")
	}

	// Outside an initialized repo the credential is the only identity there
	// is, and a team-scoped one names exactly one team. Refusing here without
	// asking sent the caller to `ox init` — wrong for a scratch directory or a
	// CI checkout that only wants to read — for a fact the credential already
	// carries and `ox status` already prints. Last resort by construction:
	// --team/--repo and the project config are both resolved above.
	if len(req.Teams) == 0 && len(req.Repos) == 0 {
		teamID, err := teamIDFromCredential(ep, token.AccessToken)
		if err != nil {
			return nil, err
		}
		req.Teams = []string{teamID}
	}

	client := api.NewRepoClientWithEndpoint(ep).WithAuthToken(token.AccessToken)

	resp, err := client.Query(req)
	if err != nil && errors.Is(err, api.ErrUnauthorized) {
		// proactive refresh above can still miss a token invalidated
		// server-side (e.g. revoked) after the check; one bounded reactive
		// retry before giving up, mirroring auth.AuthenticatedRequest.
		refreshed, refreshErr := auth.Handle401ErrorForEndpoint(token, ep)
		if refreshErr == nil && refreshed != nil && refreshed.AccessToken != "" {
			client = client.WithAuthToken(refreshed.AccessToken)
			resp, err = client.Query(req)
		}
	}
	if err != nil {
		if errors.Is(err, api.ErrUnauthorized) {
			return nil, fmt.Errorf("not authenticated. Run 'ox login' first")
		}
		if errors.Is(err, api.ErrVersionUnsupported) {
			return nil, fmt.Errorf("CLI version too old. Run 'ox version' and update")
		}
		if isNetworkError(err) {
			return nil, fmt.Errorf("query failed (is %s reachable?): %w", endpoint.NormalizeEndpoint(ep), err)
		}
		return nil, fmt.Errorf("query failed: %w", err)
	}

	return resp, nil
}

// queryCodeDB searches the local code index.
func queryCodeDB(qa *queryArgs, projectRoot string) ([]search.Result, error) {
	dataDir := resolveCodeDBDir(projectRoot)
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		return nil, nil // no index yet, return empty
	}

	if isCodeDBIndexing(false) {
		slog.Debug("codedb indexing in progress, skipping code search")
		return nil, nil // return empty rather than blocking
	}

	db, err := codedb.Open(dataDir)
	if err != nil {
		return nil, fmt.Errorf("open codedb: %w", err)
	}
	defer db.Close()

	// attach daemon-built dirty overlay for uncommitted file search
	if err := db.AttachDirtyIndex(projectRoot); err != nil {
		slog.Debug("dirty overlay not available, searching committed content only", "err", err)
	}

	return db.Search(context.Background(), qa.query)
}

// queryLocalLedger searches the locally-cached ledger (sessions + murmurs) with
// zero network calls. Always returns slice (possibly empty) and never errors —
// fail-open is required by hook callers that must exit 0 on missing cache.
//
// Hook contract (ox-tshf):
//   - Output JSON shape: {"local_results": [{score, text, doc_type, file_path,
//     source_type:"ledger", source_id, created_at}, ...]}
//   - Empty cache, missing ledger, or any read error -> {"local_results": null}
//   - Exit code: 0 always for --source=local (no auth/network failure modes).
func queryLocalLedger(qa *queryArgs, projectRoot string) []ledgersearch.Result {
	ledgerPath := resolveLocalLedgerPath(projectRoot)
	if ledgerPath == "" {
		slog.Debug("local ledger path unresolved, returning empty results")
		return nil
	}
	results, err := ledgersearch.Search(ledgersearch.Options{
		LedgerPath: ledgerPath,
		Query:      qa.query,
		Limit:      qa.limit,
	})
	if err != nil {
		// ledgersearch.Search is documented fail-open, but defend against
		// future changes — never propagate errors to hook callers.
		slog.Debug("local ledger search returned error, swallowing", "err", err)
		return nil
	}
	return results
}

// resolveLocalLedgerPath finds the cached ledger checkout for the current project.
// Returns "" if the project isn't initialized or has no resolvable ledger.
// This is intentionally never an error path — see queryLocalLedger's contract.
//
// Uses the canonical ProjectContext resolver per .claude/rules/endpoints.md
// so we honor the same local-config-aware path resolution chain other
// cmd/ox commands use, rather than constructing the path manually.
func resolveLocalLedgerPath(projectRoot string) string {
	if projectRoot == "" {
		return ""
	}
	pctx, err := config.LoadProjectContext(projectRoot)
	if err != nil || pctx == nil {
		return ""
	}
	return pctx.DefaultLedgerPath()
}
