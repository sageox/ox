package api

// KB file search — POST /api/v1/kb/search, the "query" half of the knowledge
// bubble consumption model (the "mount" half is the local git checkout).
//
// One query text is fanned across up to 20 bubbles because the server embeds
// the query once and reuses the vector per bubble; several query texts per
// request are deliberately unsupported. Results come back GROUPED PER BUBBLE
// in request order, never fused into one list — keyword scores are computed
// per bubble index and are not comparable across bubbles.
//
// Enumeration safety: a bubble the caller cannot read, one that does not
// exist, and one whose type is outside the indexed allowlist are all simply
// ABSENT from the response groups. All-inaccessible is a 200 with zero
// groups, not an error.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const kbSearchPath = "/api/v1/kb/search"

// Search modes. Hybrid runs body-keyword, title-keyword, and vector legs and
// fuses them by rank; bm25 skips the embedding call entirely.
const (
	KBSearchModeHybrid = "hybrid"
	KBSearchModeBM25   = "bm25"
	KBSearchModeVector = "vector"
)

// Per-bubble group statuses. `ok` always carries at least one hit; `empty`
// means indexed-but-matched-nothing; `not_indexed` means the bubble has never
// been indexed (distinct from empty, so a caller never reports "this bubble
// knows nothing" about a bubble that was simply never built); `error` means
// this one bubble could not be served.
const (
	KBSearchStatusOK         = "ok"
	KBSearchStatusEmpty      = "empty"
	KBSearchStatusNotIndexed = "not_indexed"
	KBSearchStatusError      = "error"
)

// KBSearchRequest is the POST /api/v1/kb/search body.
type KBSearchRequest struct {
	// Query is the search text. The server caps it at caps.max_query_len
	// BYTES of UTF-8 (it bounds the paid embedding call), not characters.
	Query string `json:"query"`
	// KBs are the bubble ids to search, in the order the response groups them.
	KBs []string `json:"kbs"`
	// Mode is hybrid (default), bm25, or vector.
	Mode string `json:"mode,omitempty"`
	// K is the number of FILES per bubble; 0 means the server default and an
	// over-cap value is clamped to caps.max_k.
	K int `json:"k,omitempty"`
	// PathPrefix and PathExact restrict hits by path; at most one may be set.
	// v1 server limitation: applied to the ranked results, not pushed into
	// the engine, so a narrow filter can yield `empty` even when matching
	// files rank below the cutoff.
	PathPrefix string `json:"path_prefix,omitempty"`
	PathExact  string `json:"path_exact,omitempty"`
}

// KBSearchCaps echoes the server-side limits that shaped a request, so a
// client can see why its k or bubble list was reduced instead of guessing.
type KBSearchCaps struct {
	MaxK   int `json:"max_k"`
	MaxKBs int `json:"max_kbs"`
	// MaxQueryLen is in BYTES of UTF-8.
	MaxQueryLen  int `json:"max_query_len"`
	SnippetChars int `json:"snippet_chars"`
}

// KBSearchHit is one FILE hit. A split file appears once, at its best rank,
// listing every chunk position that matched — k counts files, not rows.
type KBSearchHit struct {
	HitID string `json:"hit_id"`
	// Path is the repo-relative path — the handle for reading the file from
	// the bubble's local mount or the KB file endpoints.
	Path  string `json:"path"`
	Title string `json:"title"`
	// ContentRev identifies the indexed copy, so a caller can tell whether
	// the file it reads is the version that matched.
	ContentRev string `json:"content_rev"`
	// ChunkIndex is the best-ranked chunk (0 for an unsplit file);
	// ChunkCount is the file's total (1 when unsplit).
	ChunkIndex    int   `json:"chunk_index"`
	ChunkCount    int   `json:"chunk_count"`
	MatchedChunks []int `json:"matched_chunks"`
	// Rank is the 1-based fused rank within this bubble's group.
	Rank    int    `json:"rank"`
	Snippet string `json:"snippet"`
}

// KBSearchGroup is one bubble's outcome.
type KBSearchGroup struct {
	KBID string `json:"kb_id"`
	// IndexConfigVersion is the index config version this group was served
	// from; omitted when the bubble has no live index (not_indexed).
	IndexConfigVersion int    `json:"index_config_version,omitempty"`
	Status             string `json:"status"`
	// ErrorClass is a stable class (access_check, state_lookup, embed,
	// backend, namespace, unsupported_config_version), present only with
	// status=error. Never raw error text.
	ErrorClass string        `json:"error_class,omitempty"`
	Hits       []KBSearchHit `json:"hits"`
}

// KBSearchResponse is the wire response: applied caps plus one group per
// requested, accessible bubble, in request order.
type KBSearchResponse struct {
	Caps   KBSearchCaps    `json:"caps"`
	Groups []KBSearchGroup `json:"groups"`
}

// SearchFiles calls POST /api/v1/kb/search. Requires authentication.
//
// A 404 means the search route is absent — the server-side feature flag is
// off — and surfaces as ErrKBAPIUnavailable via the shared status mapping;
// per-bubble failures never error the call (they arrive as status=error
// groups), and inaccessible bubbles are silently absent from Groups.
func (c *KBClient) SearchFiles(ctx context.Context, req KBSearchRequest) (*KBSearchResponse, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, fmt.Errorf("kb search requires a query")
	}
	if len(req.KBs) == 0 {
		return nil, fmt.Errorf("kb search requires at least one kb id")
	}
	if req.PathPrefix != "" && req.PathExact != "" {
		return nil, fmt.Errorf("kb search accepts a path prefix or an exact path, not both")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal kb search request: %w", err)
	}

	reqURL := strings.TrimSuffix(c.baseURL, "/") + kbSearchPath
	respBytes, err := c.do(ctx, "POST", reqURL, body)
	if err != nil {
		return nil, err
	}

	var resp KBSearchResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode kb search response: %w", err)
	}
	return &resp, nil
}
