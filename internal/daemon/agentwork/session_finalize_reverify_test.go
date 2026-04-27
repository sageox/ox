package agentwork

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestWorkNoLongerNeeded covers the precondition re-check that prevents the
// daemon from clobbering freshly-produced summaries (bd ox-91sl).
//
// The Phase 2 batch on 2026-04-25 saw 31 of 71 sessions get clobbered:
// daemon enqueued a finalize WorkItem at Detect() time, then 30+ seconds
// later (after the CLI regen had already landed a good summary) the daemon
// processed the WorkItem, invoked the LLM, got narration back, failed
// validation, and overwrote the good summary with a failure-marker stub.
//
// This test asserts the new re-check correctly distinguishes:
//   - Work still needed → returns false (proceed)
//   - Work already done → returns true (skip; don't clobber)
func TestWorkNoLongerNeeded(t *testing.T) {
	h := &SessionFinalizeHandler{logger: slog.New(slog.DiscardHandler)}

	t.Run("missing summary.json → still needed", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "session.md"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		if h.workNoLongerNeeded(dir) {
			t.Error("missing summary.json must be reported as still-needed")
		}
	})

	t.Run("all artifacts present + good title → no longer needed (the load-bearing case)", func(t *testing.T) {
		dir := t.TempDir()
		writeAll(t, dir, map[string]string{
			"summary.json": `{"title":"Real summary title","summary":"a real one paragraph summary","key_actions":["x"]}`,
			"summary.md":   "# real",
			"session.md":   "# real",
		})
		if !h.workNoLongerNeeded(dir) {
			t.Error("session with all artifacts and a real title MUST be skipped — without this, the daemon clobbers the good summary")
		}
	})

	t.Run("all artifacts present but empty title → still needed (failure-marker stub from prior round)", func(t *testing.T) {
		dir := t.TempDir()
		// Fixture intent: a prior round failed content validation. After
		// ox-qqka, the failure stub keeps user-visible fields (title,
		// summary) EMPTY and records the diagnostic in validation_error
		// + summary_status. Tests must mirror the new shape — older
		// fixtures that put the diagnostic into "summary" baked the
		// ox-qqka leak in as expected behavior.
		writeAll(t, dir, map[string]string{
			"summary.json": `{"title":"","summary":"","summary_status":"failed_validation","validation_error":"content validation failed: title too short","score_reason":"content validation failed"}`,
			"summary.md":   "# stub",
			"session.md":   "# stub",
		})
		if h.workNoLongerNeeded(dir) {
			t.Error("an empty-title summary indicates a stub from a prior failed round; the daemon SHOULD redo this work, not skip it")
		}
	})

	t.Run("needs-summary marker present → still needed", func(t *testing.T) {
		dir := t.TempDir()
		writeAll(t, dir, map[string]string{
			"summary.json":   `{"title":"Real","summary":"x","key_actions":["x"]}`,
			"summary.md":     "x",
			"session.md":     "x",
			".needs-summary": `{}`,
		})
		if h.workNoLongerNeeded(dir) {
			t.Error("a session with the .needs-summary marker still set requires finalization regardless of artifact presence")
		}
	})

	t.Run("malformed summary.json → still needed (defensive)", func(t *testing.T) {
		dir := t.TempDir()
		writeAll(t, dir, map[string]string{
			"summary.json": "not json at all",
			"summary.md":   "x",
			"session.md":   "x",
		})
		if h.workNoLongerNeeded(dir) {
			t.Error("a malformed summary.json must NOT be treated as done; better to re-run finalize than persist garbage")
		}
	})
}

func writeAll(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}
