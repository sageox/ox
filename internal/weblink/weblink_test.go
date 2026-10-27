package weblink

import "testing"

const ep = "https://sageox.ai"

const uuid = "0192a3b4-5c6d-7e8f-9a0b-1c2d3e4f5a6b"

func TestArtifactURL(t *testing.T) {
	tests := []struct {
		name, raw, prefix, idPrefix, want string
	}{
		{"full https passthrough", "https://test.sageox.ai/c/ses_x", "/c/", "ses_", "https://test.sageox.ai/c/ses_x"},
		{"full http passthrough", "http://localhost:3000/plan/pln_x", "/plan/", "pln_", "http://localhost:3000/plan/pln_x"},
		{"bare session id", "ses_9f2a3c", "/c/", "ses_", "https://sageox.ai/c/ses_9f2a3c"},
		{"bare plan id", "pln_4d8e2f", "/plan/", "pln_", "https://sageox.ai/plan/pln_4d8e2f"},
		{"prefixless id still resolves", "abc", "/plan/", "pln_", "https://sageox.ai/plan/abc"},
		{"mismatched type prefix is rejected", "ses_9f2a3c", "/plan/", "pln_", ""},
		{"empty is dropped", "  ", "/c/", "ses_", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ArtifactURL(ep, tc.raw, tc.prefix, tc.idPrefix); got != tc.want {
				t.Errorf("ArtifactURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestArtifactURL_noEndpointDropsBareID(t *testing.T) {
	// A bare id with no endpoint has no way to become a real URL — drop it
	// rather than emit a relative link.
	if got := ArtifactURL("", "ses_x", "/c/", "ses_"); got != "" {
		t.Errorf("want empty for bare id + no endpoint, got %q", got)
	}
}

func TestIDTypePrefix(t *testing.T) {
	tests := map[string]string{
		"pln_4d8e2f": "pln_",
		"4d8e2f":     "",
		"_x":         "",
		"Pln_x":      "",
		"a1_b":       "",
		"cnv_a_b":    "cnv_",
	}
	for raw, want := range tests {
		if got := IDTypePrefix(raw); got != want {
			t.Errorf("IDTypePrefix(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestConversation: only ids the /c/ route resolves become links. Failure
// prevented: a slug, doc path or foreign host renders as a plausible
// sageox.ai link that 404s or points somewhere else.
func TestConversation(t *testing.T) {
	tests := []struct {
		name, endpoint, id, want string
	}{
		{"cnv id", ep, "cnv_" + uuid, ep + "/c/cnv_" + uuid},
		{"rec id", ep, "rec_" + uuid, ep + "/c/rec_" + uuid},
		{"ses id", ep, "ses_" + uuid, ep + "/c/ses_" + uuid},
		{"trims surrounding space", ep, "  cnv_abc\n", ep + "/c/cnv_abc"},
		{"endpoint trailing slash", ep + "/", "cnv_abc", ep + "/c/cnv_abc"},
		{"subdomain endpoint", "https://test.sageox.ai", "rec_abc", "https://test.sageox.ai/c/rec_abc"},
		{"absolute sageox link passes through", ep, "https://sageox.ai/c/cnv_abc", "https://sageox.ai/c/cnv_abc"},
		{"absolute subdomain link passes through", "", "https://test.sageox.ai/c/rec_abc", "https://test.sageox.ai/c/rec_abc"},
		{"absolute foreign host rejected", ep, "https://evil.example/c/cnv_abc", ""},
		{"absolute lookalike host rejected", ep, "https://notsageox.ai/c/cnv_abc", ""},
		{"absolute non-/c/ path rejected", ep, "https://sageox.ai/plan/pln_abc", ""},
		{"absolute bare /c/ rejected", ep, "https://sageox.ai/c/", ""},
		{"absolute with whitespace rejected", ep, "https://sageox.ai/c/cnv_a b", ""},
		{"absolute unparseable rejected", ep, "https://sageox.ai/c/%zz", ""},
		{"plan id rejected", ep, "pln_abc", ""},
		{"prefixless id rejected", ep, "abc", ""},
		{"slug rejected", ep, "usage-report-design", ""},
		{"doc path rejected", ep, "discussions/2026-10-23.md", ""},
		{"empty body rejected", ep, "cnv_", ""},
		{"body with slash rejected", ep, "cnv_a/b", ""},
		{"empty id", ep, "", ""},
		{"no endpoint", "", "cnv_abc", ""},
		{"non-http endpoint", "sageox.ai", "cnv_abc", ""},
		{"endpoint trailing newline trimmed", "https://sageox.ai\n", "cnv_abc", ep + "/c/cnv_abc"},
		{"endpoint with inner space", "https://sage ox.ai", "cnv_abc", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Conversation(tc.endpoint, tc.id); got != tc.want {
				t.Errorf("Conversation(%q, %q) = %q, want %q", tc.endpoint, tc.id, got, tc.want)
			}
		})
	}
}

// TestPlan: only pln_ ids become /plan/ links.
func TestPlan(t *testing.T) {
	tests := []struct {
		name, endpoint, id, want string
	}{
		{"pln id", ep, "pln_4d8e2f", ep + "/plan/pln_4d8e2f"},
		{"pln uuid id", ep, "pln_" + uuid, ep + "/plan/pln_" + uuid},
		{"conversation id rejected", ep, "cnv_abc", ""},
		{"prefixless rejected", ep, "4d8e2f", ""},
		{"absolute link rejected", ep, "https://sageox.ai/plan/pln_abc", ""},
		{"slug rejected", ep, "usage-receipts", ""},
		{"empty body rejected", ep, "pln_", ""},
		{"no endpoint", "", "pln_abc", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Plan(tc.endpoint, tc.id); got != tc.want {
				t.Errorf("Plan(%q, %q) = %q, want %q", tc.endpoint, tc.id, got, tc.want)
			}
		})
	}
}
