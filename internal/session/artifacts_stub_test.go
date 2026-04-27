package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsStubSummary_RejectsLocalSummaryFingerprint is the ox-0pxt
// regression: galexy-account sessions shipped to the team ledger with
// summary.json containing the LocalSummary stats-only string ("N user
// messages, N assistant responses"). The structural detector must flag
// any SummarizeResponse that has only the free-text Summary field set.
func TestIsStubSummary_RejectsLocalSummaryFingerprint(t *testing.T) {
	stub := &SummarizeResponse{
		Summary: "42 user messages, 38 assistant responses",
		Outcome: "local",
	}
	if !IsStubSummary(stub) {
		t.Errorf("LocalSummary-shaped stub must be detected; got IsStubSummary=false")
	}
}

// TestIsStubSummary_AcceptsSubstantiveSummary is the positive control:
// any LLM summary that populates *one* of the structured fields
// (Title, KeyActions, AhaMoments, Diagrams, SageoxInsights, AgentSummary)
// must NOT be flagged as a stub.
func TestIsStubSummary_AcceptsSubstantiveSummary(t *testing.T) {
	cases := []struct {
		name string
		resp *SummarizeResponse
	}{
		{"title only", &SummarizeResponse{Title: "Fix lint issues"}},
		{"key actions only", &SummarizeResponse{KeyActions: []string{"ran tests"}}},
		{"aha moments only", &SummarizeResponse{AhaMoments: []AhaMoment{{Seq: 1, Type: "insight"}}}},
		{"diagrams only", &SummarizeResponse{Diagrams: []string{"graph TD\nA-->B"}}},
		{"sageox insights only", &SummarizeResponse{SageoxInsights: []SageoxInsight{{Topic: "ldap"}}}},
		{"agent summary only", &SummarizeResponse{AgentSummary: &AgentSummary{Constraints: []string{"x"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if IsStubSummary(tc.resp) {
				t.Errorf("substantive summary (%s) must not be flagged as stub", tc.name)
			}
		})
	}
}

// TestIsStubSummary_NilIsStub: a nil response is treated as a stub so
// callers can use IsStubSummary as a single guard.
func TestIsStubSummary_NilIsStub(t *testing.T) {
	if !IsStubSummary(nil) {
		t.Errorf("nil response should be treated as stub")
	}
}

// TestIsStubSummary_DaemonFailureMarkerIsNotStub guards the load-bearing
// distinction between LocalSummary stubs (skip-write, daemon will replace)
// and daemon-side validation-failure markers (explicit failure artifact
// the daemon WANTS persisted so teammates / doctor see the failure in the
// ledger). Both shapes look structurally similar — ScoreReason and
// ValidationError both signal "deliberate failure artifact, persist".
// Without this guard, validation-failure tests in internal/daemon/agentwork
// would silently lose their failure-marker summary.json/.md files.
//
// Fixtures intentionally do NOT put the diagnostic into the user-visible
// Summary field — that would bake in the ox-qqka leak as expected
// behavior. Failure markers post-ox-qqka leave Summary empty and surface
// the diagnostic via ValidationError + ScoreReason + SummaryStatus.
func TestIsStubSummary_DaemonFailureMarkerIsNotStub(t *testing.T) {
	cases := []struct {
		name string
		resp *SummarizeResponse
	}{
		{
			"unparsable LLM output",
			&SummarizeResponse{
				QualityScore:    0.0,
				ScoreReason:     "unparsable LLM output",
				SummaryStatus:   "failed_validation",
				ValidationError: "LLM output was not valid JSON",
			},
		},
		{
			"content validation failure",
			&SummarizeResponse{
				ScoreReason:     "content validation failed: title too short (0 chars, minimum 3)",
				SummaryStatus:   "failed_validation",
				ValidationError: "content validation failed: title too short",
			},
		},
		{
			"richness validation failure",
			&SummarizeResponse{
				ScoreReason:     "richness validation failed: missing key_actions",
				SummaryStatus:   "failed_validation",
				ValidationError: "richness validation failed: missing key_actions",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if IsStubSummary(tc.resp) {
				t.Errorf("daemon failure marker (%s) must persist on disk; IsStubSummary=true would silently drop it from the ledger", tc.name)
			}
		})
	}
}

// TestWriteSessionArtifacts_SkipsStubSummaryJSON proves WriteSessionArtifacts
// no longer persists the LocalSummary stub to disk. Before this fix,
// summary.json was written unconditionally — the stats-only stub became
// the "official" summary on the team ledger when no LLM path ran. After
// the fix: stub in → no summary.json written, but session.md still emitted.
func TestWriteSessionArtifacts_SkipsStubSummaryJSON(t *testing.T) {
	dir := t.TempDir()
	stub := &SummarizeResponse{
		Summary: "42 user messages, 38 assistant responses",
		Outcome: "local",
	}
	stored := &StoredSession{
		Meta:    &StoreMeta{AgentID: "test"},
		Entries: []map[string]any{},
	}

	paths, err := WriteSessionArtifacts(dir, stored, stub)
	if err != nil {
		t.Fatalf("WriteSessionArtifacts: %v", err)
	}

	if paths.SummaryJSON != "" {
		t.Errorf("stub summary should NOT produce summary.json; got path=%q", paths.SummaryJSON)
	}
	if paths.SummaryMD != "" {
		t.Errorf("stub summary should NOT produce summary.md; got path=%q", paths.SummaryMD)
	}
	// session.md is the raw transcript — always useful, always written.
	if paths.SessionMD == "" {
		t.Errorf("session.md must be written even when summary is a stub")
	}

	// Belt and suspenders: assert summary.json file truly absent on disk.
	if _, err := os.Stat(filepath.Join(dir, "summary.json")); !os.IsNotExist(err) {
		t.Errorf("summary.json should not exist on disk for stub input; stat err=%v", err)
	}

	// And confirm we didn't accidentally serialize the fingerprint anywhere
	// else in the artifact set (e.g., via session.md which echoes user
	// content but should never echo the stub Summary text).
	if paths.SessionMD != "" {
		body, _ := os.ReadFile(paths.SessionMD)
		if strings.Contains(string(body), "user messages, ") &&
			strings.Contains(string(body), "assistant responses") {
			t.Errorf("session.md should not contain LocalSummary stub fingerprint")
		}
	}
}
