package usage

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sageox/ox/internal/session/contexttrace"
)

// TestBuild_RelevantItemDetail keeps the raw evidence beside its display
// bucket. Failure prevented: the brief says "read" for a re-query or "cited"
// for a commit citation, losing the one fact a coworker can verify.
func TestBuild_RelevantItemDetail(t *testing.T) {
	tests := []struct {
		evidence   string
		wantBucket string
		wantGrade  string
	}{
		{contexttrace.EvidenceRead, "read", "provable"},
		{contexttrace.EvidenceRequery, "read", "provable"},
		{contexttrace.EvidenceCited, "cited", "provable"},
		{contexttrace.EvidenceCitedCommit, "cited", "provable"},
		{contexttrace.EvidenceReported, "reported", "reported"},
		{contexttrace.EvidenceJudged, "judged", "judged"},
	}
	for _, tt := range tests {
		t.Run(tt.evidence, func(t *testing.T) {
			r := Build(Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{used(tt.evidence, "mur_x", "murmur", 3)}})
			require.Len(t, r.Relevant.Items, 1)
			it := r.Relevant.Items[0]
			assert.Equal(t, tt.wantBucket, it.Evidence)
			assert.Equal(t, tt.evidence, it.Detail)
			assert.Equal(t, tt.wantGrade, it.Grade)
			assert.Equal(t, 3, it.Seq)
		})
	}
}

// TestBuild_RelevantItemsTimeline orders used items by the turn they were
// used in, ties in rollup order, so the brief reads as a timeline.
func TestBuild_RelevantItemsTimeline(t *testing.T) {
	r := Build(Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{
		used(contexttrace.EvidenceReported, "late", "doc", 30),
		used(contexttrace.EvidenceRead, "tie-a", "doc", 10),
		used(contexttrace.EvidenceRead, "early", "doc", 2),
		used(contexttrace.EvidenceRead, "tie-b", "doc", 10),
		used(contexttrace.EvidenceRead, "unsequenced", "doc", 0),
	}})
	var refs []string
	for _, it := range r.Relevant.Items {
		refs = append(refs, it.Ref)
	}
	assert.Equal(t, []string{"unsequenced", "early", "tie-a", "tie-b", "late"}, refs)
}

// TestBuild_RelevantItemCommitAndWhy carries the citing commit (short) and
// the reported --why onto the item. Failure prevented: "cited in commit"
// with no commit, or a runaway why bloating agent context.
func TestBuild_RelevantItemCommitAndWhy(t *testing.T) {
	cited := used(contexttrace.EvidenceCitedCommit, "cnv_abc", "discussion", 12)
	cited.Commit = "3e1c9d0f5a6b7c8d9e0f"
	plainCite := used(contexttrace.EvidenceCited, "pln_q", "plan", 13)
	plainCite.Commit = "ffffffffffff" // a non-commit citation never shows a commit
	short := used(contexttrace.EvidenceCitedCommit, "mur_s", "murmur", 14)
	short.Commit = "abc12"
	reported := used(contexttrace.EvidenceReported, "pln_r", "plan", 15)
	reported.Decision = "  kept the retry budget at 3  "
	long := used(contexttrace.EvidenceReported, "mur_long", "murmur", 16)
	long.Decision = strings.Repeat("é", maxWhyRunes+50)

	r := Build(Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{cited, plainCite, short, reported, long}})
	require.Len(t, r.Relevant.Items, 5)
	byRef := map[string]RelevantItem{}
	for _, it := range r.Relevant.Items {
		byRef[it.Ref] = it
	}
	assert.Equal(t, "3e1c9d0", byRef["cnv_abc"].Commit)
	assert.Equal(t, "cited_commit", byRef["cnv_abc"].Detail)
	assert.Empty(t, byRef["pln_q"].Commit)
	assert.Equal(t, "abc12", byRef["mur_s"].Commit)
	assert.Equal(t, "kept the retry budget at 3", byRef["pln_r"].Why)
	assert.Equal(t, maxWhyRunes, utf8.RuneCountInString(byRef["mur_long"].Why))
	assert.True(t, utf8.ValidString(byRef["mur_long"].Why))
}

// TestBuild_RelevantItemWhySurvivesStrongerEvidence: a provable read wins the
// dedup, but the why reported for the same ref is still shown.
func TestBuild_RelevantItemWhySurvivesStrongerEvidence(t *testing.T) {
	rep := used(contexttrace.EvidenceReported, "pln_x", "plan", 5)
	rep.Decision = "followed step 2"
	r := Build(Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{rep, used(contexttrace.EvidenceRead, "pln_x", "plan", 7)}})
	require.Len(t, r.Relevant.Items, 1)
	assert.Equal(t, "read", r.Relevant.Items[0].Detail)
	assert.Equal(t, "followed step 2", r.Relevant.Items[0].Why)
}

// TestWebURL is the one link rule both surfaced and used items share.
// Failure prevented: a murmur, doc or slug renders as a plausible sageox.ai
// link that 404s, or the two lists disagree on what is linkable.
func TestWebURL(t *testing.T) {
	const ep = "https://sageox.ai"
	tests := []struct {
		name, refType, ref, endpoint, want string
	}{
		{"cnv discussion", "discussion", testCnv, ep, ep + "/c/" + testCnv},
		{"rec discussion", "discussion", "rec_abc", ep, ep + "/c/rec_abc"},
		{"ses session", "session", "ses_abc", ep, ep + "/c/ses_abc"},
		{"pln plan", "plan", "pln_4d8e2f", ep, ep + "/plan/pln_4d8e2f"},
		{"session name is not guessed", "session", "2026-10-01-ryan-Ox1", ep, ""},
		{"cnv id under session is not guessed", "session", "cnv_abc", ep, ""},
		{"murmur has no page", "murmur", "mur_x", ep, ""},
		{"doc has no page", "doc", "MEMORY.md", ep, ""},
		{"untyped ref", "", "cnv_abc", ep, ""},
		{"no endpoint", "plan", "pln_4d8e2f", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, webURL(tt.refType, tt.ref, tt.endpoint))
		})
	}
}

// TestBuild_RelevantItemURLs links used items that were never surfaced (so
// the surfaced join had no URL to copy), and never overwrites a carried URL.
func TestBuild_RelevantItemURLs(t *testing.T) {
	const ep = "https://sageox.ai"
	r := Build(Input{TraceSource: TraceSourceSession, Endpoint: ep, Events: []contexttrace.Event{
		used(contexttrace.EvidenceRead, testCnv, "discussion", 1),
		used(contexttrace.EvidenceRead, "rec_abc", "discussion", 2),
		used(contexttrace.EvidenceReported, "pln_q", "plan", 3),
		used(contexttrace.EvidenceRead, "ses_abc", "session", 4),
		used(contexttrace.EvidenceReported, "mur_x", "murmur", 5),
		used(contexttrace.EvidenceRead, "MEMORY.md", "doc", 6),
	}})
	var urls []string
	for _, it := range r.Relevant.Items {
		urls = append(urls, it.URL)
	}
	assert.Equal(t, []string{ep + "/c/" + testCnv, ep + "/c/rec_abc", ep + "/plan/pln_q", ep + "/c/ses_abc", "", ""}, urls)

	items := []RelevantItem{{Ref: "pln_q", RefType: "plan", URL: "https://test.sageox.ai/plan/pln_q"}}
	fillItemURLs(items, ep)
	assert.Equal(t, "https://test.sageox.ai/plan/pln_q", items[0].URL, "an existing url is never overwritten")

	none := Build(Input{TraceSource: TraceSourceSession, Events: []contexttrace.Event{used(contexttrace.EvidenceRead, "pln_q", "plan", 1)}})
	assert.Empty(t, none.Relevant.Items[0].URL, "no endpoint, no link")
}

// TestBuild_RelevantItemKeepsSurfacedURL: a URL the trace carried on the
// surfaced item wins over a derived one.
func TestBuild_RelevantItemKeepsSurfacedURL(t *testing.T) {
	ev := invoked("searched team memory", "query", 10, 1)
	ev.Query, ev.RefType = testCnv, "discussion"
	r := Build(Input{TraceSource: TraceSourceSession, Endpoint: "https://test.sageox.ai", Events: []contexttrace.Event{
		ev, used(contexttrace.EvidenceCited, testCnv, "discussion", 2),
	}})
	require.Len(t, r.Relevant.Items, 1)
	assert.Equal(t, r.Surfaced[0].URL, r.Relevant.Items[0].URL)
	assert.Equal(t, "https://test.sageox.ai/c/"+testCnv, r.Relevant.Items[0].URL)
}

// TestBuild_AllowJudged: judged evidence is always listed but reaches the
// derived category only when allowed (contextuse.Derive: judged-only → minor).
func TestBuild_AllowJudged(t *testing.T) {
	for _, allow := range []bool{false, true} {
		r := Build(Input{TraceSource: TraceSourceSession, AllowJudged: allow, Events: []contexttrace.Event{
			used(contexttrace.EvidenceJudged, "mur_j", "murmur", 2),
		}})
		require.Len(t, r.Relevant.Items, 1)
		want := "none"
		if allow {
			want = "minor"
		}
		assert.Equal(t, want, r.Relevant.Derived.Category, "allow=%v", allow)
		assert.Equal(t, want, r.Attribution.Category, "allow=%v", allow)
	}
}

// TestGuidanceText pins the substance of the narration contract while
// keeping it short (it is AI coworker context; every token costs). Failure
// prevented: the AI coworker claims an available item was used, nags the
// coworker about reporting, or is pointed at the retired command name.
func TestGuidanceText(t *testing.T) {
	for _, want := range []string{
		"relevant.items were used", "evidence", "surfaced[]", "available, not used",
		"used.self_report.command", "once, now", "Do not tell the coworker about reporting",
		"attribution is display-only",
	} {
		assert.Contains(t, guidanceText, want)
	}
	assert.NotContains(t, guidanceText, "ox usage")
	assert.Less(t, len(guidanceText), 1100, "keep guidance compact")
}
