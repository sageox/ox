package api

// kb_search_test.go covers KBClient.SearchFiles: request shaping (body
// fields, headers, order preservation), response decoding (caps + all four
// group statuses), and the shared status mapping (404 → ErrKBAPIUnavailable,
// 401 → ErrUnauthorized) on the POST path that this endpoint introduced to
// the kb client.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSearchFiles_RequestShape asserts the exact wire request: POST to
// /api/v1/kb/search, JSON content type, bearer auth, and a body carrying the
// query, the kb ids in caller order, mode, k, and path_prefix.
//
// Failure prevented: a silent contract drift (renamed field, lost ordering)
// that the server would answer with a 400 or — worse — wrong-order groups.
func TestSearchFiles_RequestShape(t *testing.T) {
	t.Parallel()

	var gotMethod, gotPath, gotAuth, gotContentType string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"caps":{"max_k":50,"max_kbs":20,"max_query_len":1024,"snippet_chars":480},"groups":[]}`))
	}))
	defer srv.Close()

	client := NewKBClientWithEndpoint(srv.URL).WithAuthToken("tok-123")
	_, err := client.SearchFiles(context.Background(), KBSearchRequest{
		Query:      "relay telemetry",
		KBs:        []string{"kb_b", "kb_a"},
		Mode:       KBSearchModeBM25,
		K:          5,
		PathPrefix: "docs/",
	})
	if err != nil {
		t.Fatalf("SearchFiles: %v", err)
	}

	if gotMethod != "POST" || gotPath != "/api/v1/kb/search" {
		t.Errorf("request: got %s %s, want POST /api/v1/kb/search", gotMethod, gotPath)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization: got %q", gotAuth)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type: got %q", gotContentType)
	}
	if gotBody["query"] != "relay telemetry" || gotBody["mode"] != "bm25" || gotBody["k"] != float64(5) || gotBody["path_prefix"] != "docs/" {
		t.Errorf("body fields: got %v", gotBody)
	}
	kbs, _ := gotBody["kbs"].([]any)
	if len(kbs) != 2 || kbs[0] != "kb_b" || kbs[1] != "kb_a" {
		t.Errorf("kbs order not preserved: got %v", kbs)
	}
}

// TestSearchFiles_DecodesResponse walks a full response through the decoder:
// caps, all four statuses, and hit fields including matched_chunks.
//
// Failure prevented: a decode regression that silently zeroes a field the
// renderer keys off (status strings especially — a zeroed status would fall
// into the renderer's unknown-status branch for every group).
func TestSearchFiles_DecodesResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"caps":{"max_k":50,"max_kbs":20,"max_query_len":1024,"snippet_chars":480},
			"groups":[
				{"kb_id":"kb_ok","index_config_version":1,"status":"ok","hits":[
					{"hit_id":"row1","path":"knowledge/relay.md","title":"Relay","content_rev":"abc",
					 "chunk_index":2,"chunk_count":5,"matched_chunks":[2,4],"rank":1,"snippet":"the relay batches"}]},
				{"kb_id":"kb_empty","index_config_version":1,"status":"empty","hits":[]},
				{"kb_id":"kb_new","status":"not_indexed","hits":[]},
				{"kb_id":"kb_bad","status":"error","error_class":"backend","hits":[]}
			]}`))
	}))
	defer srv.Close()

	resp, err := NewKBClientWithEndpoint(srv.URL).SearchFiles(context.Background(), KBSearchRequest{
		Query: "relay", KBs: []string{"kb_ok", "kb_empty", "kb_new", "kb_bad"},
	})
	if err != nil {
		t.Fatalf("SearchFiles: %v", err)
	}

	if resp.Caps.MaxK != 50 || resp.Caps.MaxQueryLen != 1024 || resp.Caps.SnippetChars != 480 {
		t.Errorf("caps: got %+v", resp.Caps)
	}
	if len(resp.Groups) != 4 {
		t.Fatalf("groups: got %d, want 4", len(resp.Groups))
	}
	statuses := []string{KBSearchStatusOK, KBSearchStatusEmpty, KBSearchStatusNotIndexed, KBSearchStatusError}
	for i, want := range statuses {
		if resp.Groups[i].Status != want {
			t.Errorf("group %d status: got %q, want %q", i, resp.Groups[i].Status, want)
		}
	}
	hit := resp.Groups[0].Hits[0]
	if hit.Path != "knowledge/relay.md" || hit.Rank != 1 || hit.ChunkCount != 5 || len(hit.MatchedChunks) != 2 {
		t.Errorf("hit: got %+v", hit)
	}
	if resp.Groups[3].ErrorClass != "backend" {
		t.Errorf("error_class: got %q", resp.Groups[3].ErrorClass)
	}
	if resp.Groups[2].IndexConfigVersion != 0 {
		t.Errorf("not_indexed group should omit index_config_version, got %d", resp.Groups[2].IndexConfigVersion)
	}
}

// TestSearchFiles_StatusMapping pins the typed-error translation on the POST
// path: 404 (feature flag off — the route is absent) and 403 map to the
// non-fatal sentinel, 401 to ErrUnauthorized, and a 400 surfaces the server
// message verbatim.
//
// Failure prevented: the CLI printing a raw "HTTP 404" instead of the
// "search isn't enabled yet" copy, or retry loops treating flag-off as fatal.
func TestSearchFiles_StatusMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErrIs  error
		wantSubstr string
	}{
		{name: "404 feature off", statusCode: http.StatusNotFound, wantErrIs: ErrKBAPIUnavailable},
		{name: "403 forbidden", statusCode: http.StatusForbidden, wantErrIs: ErrKBAPIUnavailable},
		{name: "401 unauthenticated", statusCode: http.StatusUnauthorized, wantErrIs: ErrUnauthorized},
		{name: "400 malformed", statusCode: http.StatusBadRequest, body: "both path filters set", wantSubstr: "both path filters set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			_, err := NewKBClientWithEndpoint(srv.URL).SearchFiles(context.Background(), KBSearchRequest{
				Query: "q", KBs: []string{"kb_x"},
			})
			if err == nil {
				t.Fatal("want error, got nil")
			}
			if tt.wantErrIs != nil && !errors.Is(err, tt.wantErrIs) {
				t.Errorf("errors.Is(%v, %v) = false", err, tt.wantErrIs)
			}
			if tt.wantSubstr != "" && !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantSubstr)
			}
		})
	}
}

// TestSearchFiles_LocalValidation asserts the no-HTTP guard rails: an empty
// query, an empty kb list, and both path filters at once are rejected before
// any request is made.
//
// Failure prevented: burning a network round-trip (and an embedding charge
// on the server) on a request the contract already documents as a 400.
func TestSearchFiles_LocalValidation(t *testing.T) {
	t.Parallel()

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	client := NewKBClientWithEndpoint(srv.URL)

	cases := []KBSearchRequest{
		{Query: "  ", KBs: []string{"kb_x"}},
		{Query: "q", KBs: nil},
		{Query: "q", KBs: []string{"kb_x"}, PathPrefix: "a/", PathExact: "a/b.md"},
	}
	for i, req := range cases {
		if _, err := client.SearchFiles(context.Background(), req); err == nil {
			t.Errorf("case %d: want validation error, got nil", i)
		}
	}
	if called {
		t.Error("local validation must reject before any HTTP request")
	}
}
