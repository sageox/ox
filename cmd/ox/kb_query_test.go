package main

// kb_query_test.go covers the logic `ox kb query` layers on top of the
// api.KBClient: input validation (the last-arg-is-query grammar and the
// client-side cap mirrors), the prose heuristic behind the "quote your
// question" hint, the pure renderer (status honesty, request-order
// grouping, missing-bubble attribution, terminal sanitization, the JSON
// envelope with guidance), and httptest-backed integration of the
// resolve → search flow including the feature-off 404.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/api"
)

// --- A. input validation ---

// TestValidateKBQueryInput exercises every fail-fast rule: empty query,
// byte-counted query cap (multi-byte runes pin the bytes-not-chars
// semantics), bubble-count cap, mode allowlist, and negative k.
//
// Failure prevented: shipping requests the server documents as 400s —
// especially a 1024-CHARACTER client check that would let a multi-byte
// query through to a confusing server rejection.
func TestValidateKBQueryInput(t *testing.T) {
	t.Parallel()

	manyIDs := make([]string, 21)
	for i := range manyIDs {
		manyIDs[i] = "kb_x"
	}

	tests := []struct {
		name       string
		ids        []string
		query      string
		mode       string
		k          int
		wantErr    bool
		wantSubstr string
	}{
		{name: "valid", ids: []string{"kb_x"}, query: "relay spans", mode: "hybrid"},
		{name: "empty query", ids: []string{"kb_x"}, query: "", mode: "hybrid", wantErr: true, wantSubstr: "query text required"},
		{name: "query at byte cap passes", ids: []string{"kb_x"}, query: strings.Repeat("a", 1024), mode: "hybrid"},
		{name: "query over byte cap", ids: []string{"kb_x"}, query: strings.Repeat("a", 1025), mode: "hybrid", wantErr: true, wantSubstr: "1024 bytes"},
		// 600 runes of 'é' (2 bytes each) = 1200 bytes > the 1024-byte cap.
		{name: "multi-byte query counted in bytes", ids: []string{"kb_x"}, query: strings.Repeat("é", 600), mode: "hybrid", wantErr: true, wantSubstr: "bytes"},
		{name: "too many bubbles", ids: manyIDs, query: "q", mode: "hybrid", wantErr: true, wantSubstr: "limit is 20"},
		{name: "bad mode", ids: []string{"kb_x"}, query: "q", mode: "knn", wantErr: true, wantSubstr: "invalid --mode"},
		{name: "bm25 mode ok", ids: []string{"kb_x"}, query: "q", mode: "bm25"},
		{name: "vector mode ok", ids: []string{"kb_x"}, query: "q", mode: "vector"},
		{name: "negative k", ids: []string{"kb_x"}, query: "q", mode: "hybrid", k: -1, wantErr: true, wantSubstr: "invalid -k"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateKBQueryInput(tt.ids, tt.query, tt.mode, tt.k)
			if tt.wantErr && err == nil {
				t.Fatal("want error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
			if err != nil && tt.wantSubstr != "" && !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantSubstr)
			}
		})
	}
}

// TestLooksLikeProse pins the hint heuristic: bare lowercase words read as
// prose; anything slug-shaped (hyphen, digit, kb_ prefix, mixed case) does
// not.
//
// Failure prevented: the "quote your question" hint firing on a genuinely
// mistyped slug (noise), or staying silent on the classic unquoted-query
// mistake it exists to catch.
func TestLooksLikeProse(t *testing.T) {
	t.Parallel()

	prose := []string{"how", "batch", "spans"}
	notProse := []string{"", "kb_01HX", "my-bubble", "team2", "Platform", "eng_docs"}
	for _, in := range prose {
		if !looksLikeProse(in) {
			t.Errorf("looksLikeProse(%q) = false, want true", in)
		}
	}
	for _, in := range notProse {
		if looksLikeProse(in) {
			t.Errorf("looksLikeProse(%q) = true, want false", in)
		}
	}
}

// --- B. renderer ---

func kbQueryFixtureResponse() *api.KBSearchResponse {
	return &api.KBSearchResponse{
		Caps: api.KBSearchCaps{MaxK: 50, MaxKBs: 20, MaxQueryLen: 1024, SnippetChars: 480},
		Groups: []api.KBSearchGroup{
			{KBID: "kb_ok", IndexConfigVersion: 1, Status: api.KBSearchStatusOK, Hits: []api.KBSearchHit{{
				HitID: "row1", Path: "knowledge/relay.md", Title: "Relay telemetry", ContentRev: "abc",
				ChunkIndex: 2, ChunkCount: 5, MatchedChunks: []int{2, 4}, Rank: 1,
				Snippet: "the relay batches spans before flushing",
			}}},
			{KBID: "kb_empty", Status: api.KBSearchStatusEmpty, Hits: []api.KBSearchHit{}},
			{KBID: "kb_new", Status: api.KBSearchStatusNotIndexed, Hits: []api.KBSearchHit{}},
			{KBID: "kb_bad", Status: api.KBSearchStatusError, ErrorClass: "backend", Hits: []api.KBSearchHit{}},
		},
	}
}

func kbQueryFixtureTargets() []kbQueryTarget {
	return []kbQueryTarget{
		{Input: "engineering", KBID: "kb_ok"},
		{Input: "platform", KBID: "kb_empty"},
		{Input: "kb_new", KBID: "kb_new"},
		{Input: "infra", KBID: "kb_bad"},
		{Input: "ghost", KBID: "kb_ghost"}, // absent from the response
	}
}

// TestRenderKBQueryResult_StatusHonesty asserts the four statuses and the
// missing-bubble case each render with distinct copy, in request order.
//
// Failure prevented: the exact misreport this command is designed against —
// an agent reading "not indexed yet" (never built) as "the bubble knows
// nothing", or a silently dropped inaccessible bubble.
func TestRenderKBQueryResult_StatusHonesty(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := renderKBQueryResult(&buf, kbQueryFixtureResponse(), kbQueryFixtureTargets(), false); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"knowledge/relay.md",
		"Relay telemetry",
		"the relay batches spans before flushing",
		"matched 2 of 5 sections",
		"no matches",
		"not indexed yet",
		"search failed for this bubble (backend)",
		"not searchable for you",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}

	// Request order: engineering before platform before infra.
	if strings.Index(out, "engineering") > strings.Index(out, "platform") ||
		strings.Index(out, "platform") > strings.Index(out, "infra") {
		t.Errorf("groups not in request order\n---\n%s", out)
	}
}

// TestRenderKBQueryResult_HitHintOnlyWithHits asserts the "Hits are files"
// hint prints only when at least one ok group actually carries a hit —
// groups alone (empty/not_indexed/error) must not trigger it.
//
// Failure prevented: telling a coworker to go read a file when the result
// contains no file to read.
func TestRenderKBQueryResult_HitHintOnlyWithHits(t *testing.T) {
	t.Parallel()

	const hint = "Hits are files"

	var withHits bytes.Buffer
	if err := renderKBQueryResult(&withHits, kbQueryFixtureResponse(), kbQueryFixtureTargets(), false); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(withHits.String(), hint) {
		t.Errorf("hint missing when a hit exists\n---\n%s", withHits.String())
	}

	// kb_ok_empty is contract-violating (ok always carries ≥1 hit) but pins
	// that the hint keys on hits, not on status alone.
	hitless := &api.KBSearchResponse{Groups: []api.KBSearchGroup{
		{KBID: "kb_ok_empty", Status: api.KBSearchStatusOK, Hits: []api.KBSearchHit{}},
		{KBID: "kb_empty", Status: api.KBSearchStatusEmpty, Hits: []api.KBSearchHit{}},
		{KBID: "kb_new", Status: api.KBSearchStatusNotIndexed, Hits: []api.KBSearchHit{}},
		{KBID: "kb_bad", Status: api.KBSearchStatusError, ErrorClass: "backend", Hits: []api.KBSearchHit{}},
	}}
	targets := []kbQueryTarget{
		{Input: "ok-empty", KBID: "kb_ok_empty"},
		{Input: "empty", KBID: "kb_empty"},
		{Input: "new", KBID: "kb_new"},
		{Input: "bad", KBID: "kb_bad"},
	}
	var noHits bytes.Buffer
	if err := renderKBQueryResult(&noHits, hitless, targets, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(noHits.String(), hint) {
		t.Errorf("hint printed with zero hits\n---\n%s", noHits.String())
	}
}

// TestRenderKBQueryResult_SanitizesServerText embeds ANSI/OSC escapes in the
// snippet, title, and path — indexed file content, the most hostile text
// this command renders — and asserts they are neutralized.
//
// Failure prevented: a hostile or compromised bubble repainting the
// terminal, forging output rows, or smuggling an OSC hyperlink/clipboard
// write through search results.
func TestRenderKBQueryResult_SanitizesServerText(t *testing.T) {
	t.Parallel()

	resp := &api.KBSearchResponse{Groups: []api.KBSearchGroup{{
		KBID: "kb_ok", Status: api.KBSearchStatusOK, Hits: []api.KBSearchHit{{
			Path:    "docs/\x1b[31mred.md",
			Title:   "evil\x1b]8;;http://evil.example\x07link",
			Rank:    1,
			Snippet: "line one\x1b[2Jcleared\nline two",
		}},
	}}}
	targets := []kbQueryTarget{{Input: "engineering", KBID: "kb_ok"}}

	var buf bytes.Buffer
	if err := renderKBQueryResult(&buf, resp, targets, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "\x1b[31m") || strings.Contains(out, "\x1b]8;;") || strings.Contains(out, "\x1b[2J") {
		t.Errorf("raw escape sequence survived rendering\n---\n%q", out)
	}
}

// TestRenderKBQueryResult_JSONEnvelope asserts the --json shape: verbatim
// groups + caps, the missing list attributing the absent bubble to the
// user's identifier, and a non-empty guidance string that names the missing
// bubble and preserves the empty/not_indexed distinction.
//
// Failure prevented: an agent surface without the guidance the thin-relay
// rule depends on, or a missing bubble silently vanishing from --json.
func TestRenderKBQueryResult_JSONEnvelope(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := renderKBQueryResult(&buf, kbQueryFixtureResponse(), kbQueryFixtureTargets(), true); err != nil {
		t.Fatalf("render: %v", err)
	}

	var out kbQueryJSONOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\n---\n%s", err, buf.String())
	}
	if len(out.Groups) != 4 {
		t.Errorf("groups: got %d, want 4", len(out.Groups))
	}
	if out.Caps.MaxQueryLen != 1024 {
		t.Errorf("caps not carried: %+v", out.Caps)
	}
	if len(out.Missing) != 1 || out.Missing[0].Input != "ghost" || out.Missing[0].KBID != "kb_ghost" {
		t.Errorf("missing: got %+v", out.Missing)
	}
	for _, want := range []string{"ghost", "not_indexed", "content_rev"} {
		if !strings.Contains(out.Guidance, want) {
			t.Errorf("guidance missing %q: %s", want, out.Guidance)
		}
	}
}

// TestRenderKBQueryResult_DropsUnrequestedGroups asserts a response group
// whose kb_id was never a resolved target is omitted from both output modes.
//
// Failure prevented: a malformed or compromised service response smuggling
// another bubble's identifiers, paths, and snippets through the CLI.
func TestRenderKBQueryResult_DropsUnrequestedGroups(t *testing.T) {
	t.Parallel()

	resp := &api.KBSearchResponse{Groups: []api.KBSearchGroup{
		{KBID: "kb_ok", Status: api.KBSearchStatusOK, Hits: []api.KBSearchHit{{
			Path: "knowledge/relay.md", Rank: 1, Snippet: "legit",
		}}},
		{KBID: "kb_rogue", Status: api.KBSearchStatusOK, Hits: []api.KBSearchHit{{
			Path: "secrets/other-team.md", Rank: 1, Snippet: "smuggled content",
		}}},
	}}
	targets := []kbQueryTarget{{Input: "engineering", KBID: "kb_ok"}}

	for _, jsonMode := range []bool{false, true} {
		var buf bytes.Buffer
		if err := renderKBQueryResult(&buf, resp, targets, jsonMode); err != nil {
			t.Fatalf("render (json=%v): %v", jsonMode, err)
		}
		out := buf.String()
		if !strings.Contains(out, "knowledge/relay.md") {
			t.Errorf("json=%v: requested group missing\n---\n%s", jsonMode, out)
		}
		for _, leaked := range []string{"kb_rogue", "secrets/other-team.md", "smuggled content"} {
			if strings.Contains(out, leaked) {
				t.Errorf("json=%v: unrequested group leaked %q\n---\n%s", jsonMode, leaked, out)
			}
		}
	}
}

// --- C. integration: resolve → search over httptest ---

// TestKBQuery_DeduplicatesResolvedIDs drives runKBQuery with the same bubble
// named twice (slug and kb_id form) and asserts the search request carries
// the id once and the output renders one group.
//
// Failure prevented: a duplicated identifier wasting a server bubble slot
// and collapsing the kb_id-keyed group rendering into misattributed rows.
func TestKBQuery_DeduplicatesResolvedIDs(t *testing.T) {
	projectRoot := stageKBDescribeProject(t, "team_dedupe")

	var searchBody api.KBSearchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/kb/resolve":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"kb_id":"kb_eng"}`))
		case "/api/v1/kb/search":
			_ = json.NewDecoder(r.Body).Decode(&searchBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"caps":{"max_k":50,"max_kbs":20,"max_query_len":1024,"snippet_chars":480},"groups":[{"kb_id":"kb_eng","status":"empty","hits":[]}]}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := api.NewKBClientWithEndpoint(srv.URL)
	ctx := t.Context()

	targets := make([]kbQueryTarget, 0, 2)
	seen := map[string]bool{}
	for _, input := range []string{"eng", "kb_eng"} {
		kbID, err := resolveKBIdentifier(ctx, client, input, "team", projectRoot)
		if err != nil {
			t.Fatalf("resolve %q: %v", input, err)
		}
		if seen[kbID] {
			continue
		}
		seen[kbID] = true
		targets = append(targets, kbQueryTarget{Input: input, KBID: kbID})
	}
	if len(targets) != 1 {
		t.Fatalf("targets: got %d, want 1 after dedupe", len(targets))
	}

	resp, err := client.SearchFiles(ctx, api.KBSearchRequest{Query: "q", KBs: kbQueryTargetIDs(targets)})
	if err != nil {
		t.Fatalf("SearchFiles: %v", err)
	}
	if len(searchBody.KBs) != 1 || searchBody.KBs[0] != "kb_eng" {
		t.Errorf("search kbs: got %v, want [kb_eng]", searchBody.KBs)
	}

	var buf bytes.Buffer
	if err := renderKBQueryResult(&buf, resp, targets, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := strings.Count(buf.String(), "no matches"); got != 1 {
		t.Errorf("group rendered %d times, want once\n---\n%s", got, buf.String())
	}
}

// TestKBQuery_ResolveThenSearch drives the full flow against a fake server:
// slugs resolve within the project team scope, and the search request
// carries the resolved kb_ids in argument order.
//
// Failure prevented: a wiring break between the batch resolution loop and
// the search call — wrong scope params, dropped identifiers, or reordered
// kbs (which would mis-attribute every group downstream).
func TestKBQuery_ResolveThenSearch(t *testing.T) {
	projectRoot := stageKBDescribeProject(t, "team_query")

	var searchBody api.KBSearchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/kb/resolve":
			slug := r.URL.Query().Get("slug")
			if got := r.URL.Query().Get("scope_id"); got != "team_query" {
				t.Errorf("scope_id: got %q, want team_query", got)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"kb_id":"kb_` + slug + `"}`))
		case "/api/v1/kb/search":
			_ = json.NewDecoder(r.Body).Decode(&searchBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"caps":{"max_k":50,"max_kbs":20,"max_query_len":1024,"snippet_chars":480},"groups":[{"kb_id":"kb_eng","status":"empty","hits":[]},{"kb_id":"kb_plat","status":"empty","hits":[]}]}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := api.NewKBClientWithEndpoint(srv.URL)
	ctx := t.Context()

	var targets []kbQueryTarget
	for _, input := range []string{"eng", "plat"} {
		kbID, err := resolveKBIdentifier(ctx, client, input, "team", projectRoot)
		if err != nil {
			t.Fatalf("resolve %q: %v", input, err)
		}
		targets = append(targets, kbQueryTarget{Input: input, KBID: kbID})
	}
	resp, err := client.SearchFiles(ctx, api.KBSearchRequest{
		Query: "relay spans", KBs: kbQueryTargetIDs(targets), Mode: "hybrid",
	})
	if err != nil {
		t.Fatalf("SearchFiles: %v", err)
	}

	if len(searchBody.KBs) != 2 || searchBody.KBs[0] != "kb_eng" || searchBody.KBs[1] != "kb_plat" {
		t.Errorf("search kbs: got %v, want [kb_eng kb_plat]", searchBody.KBs)
	}
	if searchBody.Query != "relay spans" {
		t.Errorf("search query: got %q", searchBody.Query)
	}
	var buf bytes.Buffer
	if err := renderKBQueryResult(&buf, resp, targets, false); err != nil {
		t.Fatalf("render: %v", err)
	}
}

// TestHandleKBSearchError_FeatureOff asserts a 404 from the search route —
// the flag-off signal, distinct from describe's "slug not found" meaning of
// the same sentinel — renders the dedicated "isn't enabled yet" copy in
// JSON mode (human mode writes to stderr; JSON is what agents consume).
//
// Failure prevented: the misleading describe-flavored "no knowledge bubble
// matching…" copy for a bubble that resolved fine seconds earlier.
func TestHandleKBSearchError_FeatureOff(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := api.NewKBClientWithEndpoint(srv.URL).SearchFiles(t.Context(), api.KBSearchRequest{
		Query: "q", KBs: []string{"kb_x"},
	})
	if err == nil {
		t.Fatal("want error from 404")
	}

	var buf bytes.Buffer
	if renderErr := handleKBSearchError(&buf, err, true); renderErr != nil {
		t.Fatalf("handleKBSearchError json: %v", renderErr)
	}
	var out map[string]any
	if jsonErr := json.Unmarshal(buf.Bytes(), &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if out["status"] != "unavailable" {
		t.Errorf("status: got %v", out["status"])
	}
	msg, _ := out["message"].(string)
	if !strings.Contains(msg, "isn't enabled for your team yet") {
		t.Errorf("message: got %q", msg)
	}
	if guidance, _ := out["guidance"].(string); guidance == "" {
		t.Error("guidance must be present for agent consumers")
	}
}
