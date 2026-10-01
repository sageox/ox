package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/plan"
	"github.com/spf13/cobra"
)

func saveFeedbackTestPlan(t *testing.T, slug string) (root, planDir string) {
	t.Helper()
	root = newPlanStatusTestRepo(t)
	dir, _, err := plan.Save(root, plan.Input{Raw: "# " + slug + "\n"}, plan.Result{}, nil, plan.Meta{Topic: slug, Slug: slug})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	return root, dir
}

func countRoundFiles(t *testing.T, planDir string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(planDir, "feedback", "round-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}

// TestRunPlanFeedbackApply_Idempotent verifies applying the same export twice
// writes one round and the second run reports "already applied" and succeeds —
// with an explicit id and with an id derived from content. Failure prevented:
// a retried paste doubles every reviewer mark in the digest.
func TestRunPlanFeedbackApply_Idempotent(t *testing.T) {
	tests := []struct {
		name     string
		feedback string
		wantID   string
	}{
		{"explicit id", `{"id":"export-0001","items":[{"anchor":"h1","status":"flag","note":"hi"}]}`, "export-0001"},
		{"derived id", `{"reviewer":"person-a","items":[{"anchor":"h1","status":"flag","note":"hi"}]}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, planDir := saveFeedbackTestPlan(t, "apply-idem")
			src := filepath.Join(t.TempDir(), "feedback.json")
			if err := os.WriteFile(src, []byte(tt.feedback), 0o644); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				cmd := &cobra.Command{}
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&out)
				if err := runPlanFeedbackApply(cmd, "apply-idem", src); err != nil {
					t.Fatalf("apply #%d: %v", i+1, err)
				}
				already := strings.Contains(out.String(), "already applied (round ")
				if already != (i == 1) {
					t.Errorf("apply #%d already-applied=%v, output:\n%s", i+1, already, out.String())
				}
				if i == 1 && tt.wantID != "" && !strings.Contains(out.String(), tt.wantID) {
					t.Errorf("second apply must name round %s, got:\n%s", tt.wantID, out.String())
				}
			}
			if got := countRoundFiles(t, planDir); got != 1 {
				t.Errorf("round files = %d, want 1", got)
			}
		})
	}
}

// TestRunPlanFeedbackShow_ReportsCorruptRounds verifies a torn round is named
// in both the human digest and --json. Failure prevented: an agent reads a
// digest missing a reviewer's round and believes the review is complete.
func TestRunPlanFeedbackShow_ReportsCorruptRounds(t *testing.T) {
	_, planDir := saveFeedbackTestPlan(t, "show-corrupt")
	if _, err := plan.SaveFeedback(planDir, plan.FeedbackSet{Items: []plan.FeedbackItem{{Anchor: "h1", Status: plan.FeedbackFlag, Note: "ok"}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	torn := filepath.Join(planDir, "feedback", "round-20260101-000000.000000000-deadbeef.json")
	if err := os.WriteFile(torn, []byte(`{"items":[`), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		json bool
	}{
		{"text", false},
		{"json", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			var out bytes.Buffer
			cmd.SetOut(&out)
			if err := runPlanFeedbackShow(cmd, "show-corrupt", tt.json); err != nil {
				t.Fatalf("show: %v", err)
			}
			if !tt.json {
				if !strings.Contains(out.String(), "WARNING: 1 review round(s) could not be read") || !strings.Contains(out.String(), torn) {
					t.Errorf("text output must warn and name the torn round, got:\n%s", out.String())
				}
				return
			}
			var got struct {
				Items         []plan.MergedItem `json:"items"`
				CorruptRounds []string          `json:"corrupt_rounds"`
			}
			dec := json.NewDecoder(&out)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&got); err != nil {
				t.Fatalf("decode --json: %v", err)
			}
			if len(got.Items) != 1 || got.Items[0].Note != "ok" {
				t.Errorf("items = %+v, want the intact round's mark", got.Items)
			}
			if len(got.CorruptRounds) != 1 || got.CorruptRounds[0] != torn {
				t.Errorf("corrupt_rounds = %v, want [%s]", got.CorruptRounds, torn)
			}
		})
	}
}
