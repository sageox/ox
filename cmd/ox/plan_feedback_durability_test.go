package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/plan"
	"github.com/sageox/ox/pkg/planreview"
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
// with an explicit id and with an id derived from content, and when the second
// copy picked up whitespace from a paste or an editor. Failure prevented: a
// retried paste doubles every reviewer mark in the digest, or is refused as a
// conflicting operation.
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
				if i == 1 {
					pasted := strings.ReplaceAll(tt.feedback, ",", ",\n  ") + "\n"
					if err := os.WriteFile(src, []byte(pasted), 0o644); err != nil {
						t.Fatal(err)
					}
				}
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

// TestRunPlanFeedbackApply_RefusesBadInput verifies apply refuses an export it
// cannot store before writing anything: a missing file, one over the record
// limit, and one naming no plan. Failure prevented: an oversized paste is read
// whole into memory, or half-applied.
func TestRunPlanFeedbackApply_RefusesBadInput(t *testing.T) {
	_, planDir := saveFeedbackTestPlan(t, "apply-bad")
	big := filepath.Join(t.TempDir(), "big.json")
	if err := os.WriteFile(big, bytes.Repeat([]byte(" "), planreview.MaxRecordBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	noSlug := filepath.Join(t.TempDir(), "noslug.json")
	if err := os.WriteFile(noSlug, []byte(`{"items":[{"anchor":"h1","status":"flag"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, slug, from, want string
	}{
		{"missing file", "apply-bad", filepath.Join(t.TempDir(), "gone.json"), "read feedback"},
		{"over the record limit", "apply-bad", big, "over the"},
		{"no plan named", "", noSlug, "no slug"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.SetOut(&bytes.Buffer{})
			if err := runPlanFeedbackApply(cmd, tt.slug, tt.from); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(planDir, "feedback")); !os.IsNotExist(err) {
		t.Fatalf("a refused export must write nothing, stat: %v", err)
	}
}

// TestRunPlanFeedbackResolve_RecordsOneResolution verifies `ox plan feedback
// resolve` closes the item through one resolution record, says when it is not
// yet shared, and refuses an unknown state without writing. Failure prevented:
// the agent's resolution never lands, or a typo stores a state no reader
// understands.
func TestRunPlanFeedbackResolve_RecordsOneResolution(t *testing.T) {
	_, planDir := saveFeedbackTestPlan(t, "resolve-one")
	roundForTest(t, planDir, time.Now().Add(-time.Minute), planreview.Item{Anchor: "h1", Status: planreview.StatusRequestChange, Note: "fix"})

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runPlanFeedbackResolve(cmd, "resolve-one", "h1", "nonsense", "", ""); err == nil {
		t.Fatal("an unknown state must be refused")
	}
	if err := runPlanFeedbackResolve(cmd, "resolve-one", "h1", "addressed", "abc123", "bounded it"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains(out.String(), "Marked h1 as addressed.") {
		t.Errorf("output = %q", out.String())
	}
	files, _ := filepath.Glob(filepath.Join(planDir, "feedback", "resolutions", "*.json"))
	items := plan.ReadReview(planDir).Items
	if len(files) != 1 || len(items) != 1 || items[0].Open || items[0].Resolution.Commit != "abc123" {
		t.Fatalf("want one resolution record closing h1, got files %v items %+v", files, items)
	}
}

// TestRunPlanFeedbackShow_ReportsAnIncompleteReview verifies a torn round is
// named in both the human digest and --json, and that a review it cannot read
// at all says so instead of "no feedback". Failure prevented: an agent reads a
// digest missing a reviewer's round and believes the review is complete.
func TestRunPlanFeedbackShow_ReportsAnIncompleteReview(t *testing.T) {
	_, planDir := saveFeedbackTestPlan(t, "show-corrupt")
	roundForTest(t, planDir, time.Now(), planreview.Item{Anchor: "h1", Status: planreview.StatusFlag, Note: "ok"})
	// an altered round (its bytes no longer hash to its name) is still read
	records, _ := filepath.Glob(filepath.Join(planDir, "feedback", "round-*.json"))
	if len(records) != 1 {
		t.Fatalf("want one round record, got %v", records)
	}
	if b, err := os.ReadFile(records[0]); err != nil || os.WriteFile(records[0], append(b, ' '), 0o644) != nil {
		t.Fatalf("alter the round: %v", err)
	}
	torn := filepath.Join(planDir, "feedback", "round-20260101-000000.000000000-deadbeef.json")
	if err := os.WriteFile(torn, []byte(`{"items":[`), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runPlanFeedbackShow(cmd, "show-corrupt", false); err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(out.String(), "WARNING: this review is incomplete") || !strings.Contains(out.String(), strconv.Quote(torn)+": undecodable") {
		t.Errorf("text output must warn and name the torn round, got:\n%s", out.String())
	}

	out.Reset()
	if err := runPlanFeedbackShow(cmd, "show-corrupt", true); err != nil {
		t.Fatalf("show --json: %v", err)
	}
	var got planFeedbackShowJSON
	dec := json.NewDecoder(&out)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode --json: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].Note != "ok" {
		t.Errorf("items = %+v, want the intact round's mark", got.Items)
	}
	if got.Complete || len(got.Failures) != 2 || len(got.CorruptRounds) != 1 || got.CorruptRounds[0] != torn {
		t.Errorf("want an incomplete review whose corrupt_rounds names only the unreadable %s, got complete=%v failures=%+v corrupt_rounds=%v", torn, got.Complete, got.Failures, got.CorruptRounds)
	}

	// nothing readable at all is still not "no feedback"
	if err := os.RemoveAll(filepath.Join(planDir, "feedback")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "feedback"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runPlanFeedbackShow(cmd, "show-corrupt", false); err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.Contains(out.String(), "No review feedback") || !strings.Contains(out.String(), "not_regular") {
		t.Errorf("an unreadable feedback dir must be reported, got:\n%s", out.String())
	}
}

// A plan with no review yet must say so in text and return empty arrays (not
// null) and complete=true in JSON.
func TestRunPlanFeedbackShow_Empty(t *testing.T) {
	saveFeedbackTestPlan(t, "show-empty")

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runPlanFeedbackShow(cmd, "show-empty", false); err != nil {
		t.Fatalf("show text: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "No review feedback for this plan yet." {
		t.Fatalf("text = %q", got)
	}

	out.Reset()
	if err := runPlanFeedbackShow(cmd, "show-empty", true); err != nil {
		t.Fatalf("show json: %v", err)
	}
	if got, want := strings.TrimSpace(out.String()), `{
  "items": [],
  "corrupt_rounds": [],
  "complete": true,
  "failures": [],
  "conflicts": []
}`; got != want {
		t.Fatalf("json = %s, want %s", got, want)
	}
}

// writeTornRound leaves an undecodable round record in planDir.
func writeTornRound(t *testing.T, planDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(planDir, "feedback"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "feedback", "round-20260101-000000.000000000-deadbeef.json"), []byte(`{"items":[`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPlanSurfaces_MarkAnIncompleteReview verifies `ox plan list` and a saved
// plan's render say when its review could not be fully read: the REVIEW column
// never shows "—" for one, and the page carries the failure count its warning
// bar reads. Failure prevented: a damaged record hides a reviewer's open
// feedback behind a table and a page that look complete.
func TestPlanSurfaces_MarkAnIncompleteReview(t *testing.T) {
	root, partly := saveFeedbackTestPlan(t, "partly-read")
	roundForTest(t, partly, time.Now(), planreview.Item{Anchor: "h1", Status: planreview.StatusRequestChange})
	writeTornRound(t, partly)
	unread, _, err := plan.Save(root, plan.Input{Raw: "# unread\n"}, plan.Result{}, nil, plan.Meta{Topic: "unread", Slug: "unread"})
	if err != nil {
		t.Fatal(err)
	}
	writeTornRound(t, unread)

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runPlanList(cmd, false); err != nil {
		t.Fatalf("list: %v", err)
	}
	rows := map[string]string{}
	for _, line := range strings.Split(out.String(), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			rows[f[0]] = line
		}
	}
	if !strings.Contains(rows["partly-read"], "1 open, incomplete") || !strings.Contains(rows["unread"], "incomplete") || strings.Contains(rows["unread"], "open") {
		t.Errorf("want partly-read as \"1 open, incomplete\" and unread as \"incomplete\", got:\n%s", out.String())
	}

	page := filepath.Join(t.TempDir(), "page.html")
	if err := runPlanRenderSaved(cmd, "unread", page, false, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	if b, err := os.ReadFile(page); err != nil || !strings.Contains(string(b), `data-review-failures="1"`) {
		t.Fatalf("the rendered page must carry the review's failure count (err %v)", err)
	}
}
