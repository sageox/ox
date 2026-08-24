package main

// plan_capture_test.go tests savePlanArtifacts — the COMPOSITION ROOT of plan
// capture, and the layer that was missing when three attribution defects shipped
// together and survived three months.
//
// All three were composition failures, not unit failures: provenance was
// resolved and then discarded by a guard in the caller; the recording state was
// resolved and then re-derived a second, different way; and a notification the
// caller simply never made. Every function involved was individually correct and
// individually tested. A unit test of any one of them passes against all three
// bugs, because each is handed the argument the composition root is the only
// thing that computes.
//
// So these tests assert DURABLE ARTIFACTS ONLY — what is on disk after a save —
// never call counts, never in-memory structs the test itself built, never wall
// clock. Each assertion names the specific loss it pins.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sageox/ox/internal/plan"
	"github.com/sageox/ox/internal/session"
)

const captureTestRepoID = "plan-capture-test-repo"

// newPlanCaptureTestRepo is newPlanStatusTestRepo's sandbox (HOME + every
// XDG_*_HOME + a .sageox/config.json carrying a repo_id) under this file's own
// repo id, so seeded recordings land in a temp session cache that
// sessionsSearchPaths can actually discover.
func newPlanCaptureTestRepo(t *testing.T) string {
	t.Helper()
	root := newPlanEnrichTestRepo(t)

	xdgHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdgHome, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdgHome, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(xdgHome, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(xdgHome, "state"))

	sageoxDir := filepath.Join(root, ".sageox")
	if err := os.MkdirAll(sageoxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"config_version":"2","repo_id":"` + captureTestRepoID + `"}`
	if err := os.WriteFile(filepath.Join(sageoxDir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// startFakeRecording seeds a live .recording.json discoverable by BOTH
// LoadRecordingStateForAgent and LoadRecordingStateForWorkspace, in the shape
// production writes it (under the repo's session cache, with WorkspacePath set).
// The absence of this fixture is the direct reason the workspace-fallback branch
// — the whole point of loadPlanRecordingState — had no coverage.
func startFakeRecording(t *testing.T, root string, st session.RecordingState) *session.RecordingState {
	t.Helper()
	if st.SessionPath == "" {
		ctxPath := session.GetContextPath(captureTestRepoID)
		if ctxPath == "" {
			t.Fatal("GetContextPath returned empty; XDG sandbox not applied")
		}
		st.SessionPath = filepath.Join(ctxPath, "sessions", "2026-08-10T09-00-person-a-Oxcap1")
	}
	if st.WorkspacePath == "" {
		st.WorkspacePath = root
	}
	if err := session.SaveRecordingState(root, &st); err != nil {
		t.Fatalf("seed recording state: %v", err)
	}
	return &st
}

// gitConfig sets a REPO-LOCAL git config value. cmd.Dir is the temp repo, never
// the real one, and callers pair it with a sandboxed GIT_CONFIG_GLOBAL — the
// identity resolver shells out to `git config`, so an unsandboxed test reads
// (and could write) the developer's own identity.
func gitConfig(t *testing.T, root, key, value string) {
	t.Helper()
	cmd := exec.Command("git", "config", key, value)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config %s: %v\n%s", key, err, out)
	}
}

// readRecordingFromDisk re-reads .recording.json. Reading the FILE is the point:
// session stop runs in a different process, so the reverse link exists only if
// it reached disk. An assertion on an in-memory slice proves nothing about that.
func readRecordingFromDisk(t *testing.T, sessionPath string) *session.RecordingState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sessionPath, ".recording.json"))
	if err != nil {
		t.Fatalf("read .recording.json: %v", err)
	}
	var st session.RecordingState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("parse .recording.json: %v", err)
	}
	return &st
}

// TestSavePlanArtifacts_LinksBothDirectionsWithoutAgentID is THE regression gate
// for this PR. It runs the exact configuration all three defects required —
// SAGEOX_AGENT_ID unset, a live recording findable only by workspace — and
// asserts the four durable facts a save must leave behind.
//
// Red-first, three independent mutations giving three distinct failures:
//   - restore `if agentID == "" { return nil, nil }` at the top of
//     resolvePlanProvenance → all four assertions fail
//   - replace loadPlanRecordingState's workspace branch with `st = nil` →
//     session_name, session_id and produced_plans fail, author stays GREEN
//     (proving the assertions are independent, not one guard in a trench coat)
//   - revert appendProducedPlan to re-derive by agent id → produced_plans alone
func TestSavePlanArtifacts_LinksBothDirectionsWithoutAgentID(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "") // the condition under which every defect fired

	rec := startFakeRecording(t, root, session.RecordingState{
		AgentID:   "Oxcap1",
		SessionID: "ses_01920000-0000-7000-8000-00000000cap1",
	})
	wantSession := session.GetSessionName(rec.SessionPath)

	dir := savePlanArtifacts(root, plan.Input{Raw: "# Capture Composition Root\n"}, plan.Result{}, nil, "")
	if dir == "" {
		t.Fatal("savePlanArtifacts returned empty dir — the plan was not saved at all")
	}

	meta, err := plan.LoadMeta(dir)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.Provenance == nil {
		t.Fatal("meta.json carries no provenance block: the whole forward link was discarded")
	}
	if meta.Provenance.SessionName != wantSession {
		t.Errorf("provenance.session_name = %q, want %q — the session was resolvable by workspace and got dropped",
			meta.Provenance.SessionName, wantSession)
	}
	if meta.Provenance.SessionID != rec.SessionID {
		t.Errorf("provenance.session_id = %q, want %q", meta.Provenance.SessionID, rec.SessionID)
	}

	// events.jsonl is the PERMANENCE vector: BackfillSessionID matches on
	// SessionName, so an event line written without one can never be repaired.
	// This is the 4.7% half of the loss, and the half the PR's first regression
	// test did not cover.
	events, err := plan.LoadEvents(dir)
	if err != nil || len(events) == 0 {
		t.Fatalf("LoadEvents: err=%v n=%d", err, len(events))
	}
	if got := events[len(events)-1].SessionName; got != wantSession {
		t.Errorf("events.jsonl session_name = %q, want %q — unrepairable: BackfillSessionID matches on this field", got, wantSession)
	}

	// Reverse link, read back from the file session stop will read.
	onDisk := readRecordingFromDisk(t, rec.SessionPath)
	if len(onDisk.ProducedPlans) != 1 || onDisk.ProducedPlans[0] != meta.Slug {
		t.Errorf("on-disk produced_plans = %v, want [%s] — this is how it stayed unset on 2887 sessions",
			onDisk.ProducedPlans, meta.Slug)
	}
}

// TestSavePlanArtifacts_ReverseLinkSurvivesConcurrentHookUpdate pins the
// lost-update fix. .recording.json is a whole-file rewrite shared with the hook
// path; writing back a struct captured before plan.Save reverts every field a
// concurrent hook touched. Because the ledger auto-resolves sessions/ conflicts
// to the local side, that stripping propagates to origin and erases the fields
// for the whole team — the failure make check-session-meta-rmw already guards
// on sessions/*/meta.json (ox-q42i, GH #710).
//
// Red-first: have appendProducedPlan write back the caller's struct via
// session.SaveRecordingState instead of delegating by path → EntryCount and
// ProducedCommits revert to their pre-save values.
func TestSavePlanArtifacts_ReverseLinkSurvivesConcurrentHookUpdate(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	rec := startFakeRecording(t, root, session.RecordingState{
		AgentID:   "Oxcap2",
		SessionID: "ses_01920000-0000-7000-8000-00000000cap2",
	})

	// Simulate a hook that fires while the plan save is in flight: it loads
	// fresh, bumps its own fields, and saves. A stale write-back loses these.
	if err := session.UpdateRecordingStateForAgent(root, "Oxcap2", func(s *session.RecordingState) {
		s.EntryCount = 42
		s.SourceOffset = 4096
		s.ProducedCommits = []string{"abc1234"}
	}); err != nil {
		t.Fatalf("simulated hook update: %v", err)
	}

	if err := appendProducedPlan(root, rec, "concurrent-slug"); err != nil {
		t.Fatalf("appendProducedPlan: %v", err)
	}

	onDisk := readRecordingFromDisk(t, rec.SessionPath)
	if onDisk.EntryCount != 42 {
		t.Errorf("entry_count = %d, want 42 — a concurrent hook update was clobbered", onDisk.EntryCount)
	}
	if onDisk.SourceOffset != 4096 {
		t.Errorf("source_offset = %d, want 4096 — rewinding this makes the reader re-ingest captured bytes", onDisk.SourceOffset)
	}
	if len(onDisk.ProducedCommits) != 1 || onDisk.ProducedCommits[0] != "abc1234" {
		t.Errorf("produced_commits = %v, want [abc1234] — commit attribution lost, same class as the bug being fixed", onDisk.ProducedCommits)
	}
	if len(onDisk.ProducedPlans) != 1 || onDisk.ProducedPlans[0] != "concurrent-slug" {
		t.Errorf("produced_plans = %v, want [concurrent-slug]", onDisk.ProducedPlans)
	}
}

// TestAppendProducedPlan_DoesNotResurrectStoppedRecording pins the
// write-after-delete fix. SaveRecordingState does an unconditional MkdirAll +
// write, so handing it a state captured before `ox session stop` recreates the
// .recording.json stop deleted. The zombie is permanent — ghost cleanup skips
// sessions whose raw.jsonl is an LFS pointer, which is exactly what a finalized
// session has — and StartRecording then refuses that agent forever with
// ErrAlreadyRecording.
//
// Red-first: revert appendProducedPlan to SaveRecordingState(projectRoot, st) →
// the state file exists again after the call.
func TestAppendProducedPlan_DoesNotResurrectStoppedRecording(t *testing.T) {
	root := newPlanCaptureTestRepo(t)

	rec := startFakeRecording(t, root, session.RecordingState{
		AgentID:   "Oxcap3",
		SessionID: "ses_01920000-0000-7000-8000-00000000cap3",
	})
	statePath := filepath.Join(rec.SessionPath, ".recording.json")

	// the session stops while the plan save is mid-flight
	if err := session.ClearRecordingStateForAgent(root, "Oxcap3"); err != nil {
		t.Fatalf("clear recording state: %v", err)
	}

	if err := appendProducedPlan(root, rec, "post-stop-slug"); err != nil {
		t.Errorf("append after stop should be a silent no-op, got %v", err)
	}

	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("`.recording.json` was recreated after session stop (err=%v): the recording is now a permanent zombie — IsRecording stays true and StartRecording returns ErrAlreadyRecording for this agent forever", err)
	}
}

// TestResolvePlanProvenance_SessionSurvivesMissingAgentID covers the half of the
// bug the first regression test missed. Proven by mutation: re-adding
// `if agentID == "" { return nil }` inside loadPlanRecordingState left the whole
// suite green, because asserting AuthorName alone keeps prov non-nil.
//
// This is the permanent half — with SessionName blank, plan.BackfillSessionID
// (which matches on SessionName) can never attach the ses_ id afterwards.
func TestResolvePlanProvenance_SessionSurvivesMissingAgentID(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	rec := startFakeRecording(t, root, session.RecordingState{
		AgentID:   "Oxcap4",
		SessionID: "ses_01920000-0000-7000-8000-00000000cap4",
	})

	prov, st := resolvePlanProvenance(root)
	if prov == nil {
		t.Fatal("provenance is nil despite a live recording resolvable by workspace")
	}
	if st == nil {
		t.Fatal("recording state not returned: deriveCollabSignals and the reverse link both get nothing")
	}
	want := session.GetSessionName(rec.SessionPath)
	if prov.SessionName != want {
		t.Errorf("SessionName = %q, want %q — agent detection is not a precondition for the session", prov.SessionName, want)
	}
	if prov.SessionID != rec.SessionID {
		t.Errorf("SessionID = %q, want %q", prov.SessionID, rec.SessionID)
	}
	if prov.SessionOutcome != plan.SessionOutcomeActive {
		t.Errorf("SessionOutcome = %q, want %q", prov.SessionOutcome, plan.SessionOutcomeActive)
	}
}

// TestResolvePlanProvenance_SubagentPrefersParentAndAgreesWithRenderLink pins
// the three-way contract: the session stamped into provenance, the session the
// reverse link lands on, and the /c/ id embedded in the rendered artifact must
// all name ONE session.
//
// FIXTURE ORDERING IS LOAD-BEARING. LoadRecordingStateForWorkspace returns the
// FIRST directory matching the workspace, in name order, and session dirs are
// timestamp-prefixed. So the subagent is seeded with the EARLIER timestamp: the
// workspace lookup then hands back the child, and parent-preference is the only
// thing that can produce the parent. With the parent seeded first the lookup
// returns it directly and the test passes without exercising the branch at all
// — which is exactly how the first version of this test passed under a mutation
// that deleted parent-preference outright.
//
// Red-first: delete the ParentAgentID block in loadPlanRecordingState → the
// provenance session id becomes the subagent's.
//
// The link-agreement assertion below is deliberately a STRUCTURAL guard, not a
// behavioral one: liveSessionConversationURL is now a two-line wrapper over the
// same resolver, so the ids cannot disagree by construction. It fails only if
// someone reintroduces a second resolution path — which is the drift that made
// LintSessionLink false-warn in the first place.
func TestResolvePlanProvenance_SubagentPrefersParentAndAgreesWithRenderLink(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	ctxPath := session.GetContextPath(captureTestRepoID)
	startFakeRecording(t, root, session.RecordingState{
		AgentID:       "Oxsub1",
		ParentAgentID: "Oxpar1",
		SessionID:     "ses_01920000-0000-7000-8000-00000000sub1",
		SessionPath:   filepath.Join(ctxPath, "sessions", "2026-08-10T09-00-person-a-Oxsub1"),
	})
	parent := startFakeRecording(t, root, session.RecordingState{
		AgentID:     "Oxpar1",
		SessionID:   "ses_01920000-0000-7000-8000-0000000parent",
		SessionPath: filepath.Join(ctxPath, "sessions", "2026-08-10T09-30-person-a-Oxpar1"),
	})

	// Fixture self-check: if the workspace lookup already returns the parent,
	// parent-preference is never exercised and this test proves nothing.
	if direct, _ := session.LoadRecordingStateForWorkspace(root, root); direct == nil || direct.AgentID != "Oxsub1" {
		t.Fatalf("fixture broken: workspace lookup returned %+v, want the SUBAGENT first so parent-preference is load-bearing", direct)
	}

	prov, _ := resolvePlanProvenance(root)
	if prov == nil {
		t.Fatal("provenance is nil")
	}
	if prov.SessionID != parent.SessionID {
		t.Errorf("SessionID = %q, want the PARENT %q — a subagent's plan links the main session",
			prov.SessionID, parent.SessionID)
	}

	linkID, _ := planSessionLink(root)
	if linkID != prov.SessionID {
		t.Errorf("render link id %q != provenance session id %q — LintSessionLink compares exactly these two, so any gap false-warns on every render",
			linkID, prov.SessionID)
	}
}

// TestPlanSessionLink_DisabledAttributionExpectsNoLink is the false-warn fix.
//
// Provenance records the session for the ledger regardless, but when session
// attribution is turned off the render carries no /c/ link — and the save-path
// lint must therefore not run. Gating that lint on the stamped provenance
// instead of on this resolver is what made `plan-lint [session.link-missing]`
// print on every save once provenance started resolving without an agent id:
// a warning about a missing link that was never supposed to be there.
//
// Red-first: delete the `attr.Session == ""` gate in planSessionLink → a link id
// comes back for a user who disabled session attribution.
func TestPlanSessionLink_DisabledAttributionExpectsNoLink(t *testing.T) {
	root := newPlanCaptureTestRepo(t)
	t.Setenv("SAGEOX_AGENT_ID", "")

	cfg := `{"config_version":"2","repo_id":"` + captureTestRepoID + `","attribution":{"session":""}}`
	if err := os.WriteFile(filepath.Join(root, ".sageox", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := startFakeRecording(t, root, session.RecordingState{
		AgentID:   "Oxnolink",
		SessionID: "ses_01920000-0000-7000-8000-000000nolink",
	})

	// Provenance still records the session — the ledger link is not the page link.
	prov, _ := resolvePlanProvenance(root)
	if prov == nil || prov.SessionID != rec.SessionID {
		t.Fatalf("provenance should still carry the session for the ledger, got %+v", prov)
	}

	if id, url := planSessionLink(root); id != "" || url != "" {
		t.Errorf("planSessionLink = (%q, %q), want empty — no link is expected in the render, so the save-path lint must not run and warn about its absence", id, url)
	}
}

// TestSavePlanArtifacts_LargeRenderStaysPlainWhenLFSUnreachable is the #810
// composition-root fail-safe: driving the REAL savePlanArtifacts path (Save →
// DehydrateHTML → commit) with a >256KB render and no reachable LFS client, the
// committed plan.html must be PLAIN — retrievable and pushable, never a poisoned
// pointer. This test ledger has no configured remote/credentials, so planLFSClient
// resolves to nil and dehydration falls back to plain, exactly as an offline save.
//
// Failure prevented: a regression where the composition root writes (or leaves) a
// pointer with no uploaded blob — the wedge #810 was about — at the layer where
// the original bug actually shipped.
func TestSavePlanArtifacts_LargeRenderStaysPlainWhenLFSUnreachable(t *testing.T) {
	root := newPlanCaptureTestRepo(t)

	body := strings.Repeat("PRESERVE-ME ", 30000) // well over the 256KB threshold
	html := []byte("<html><head></head><body>" + body + "</body></html>")

	dir := savePlanArtifacts(root, plan.Input{Raw: "# Big render\n"}, plan.Result{}, html, plan.PrimaryHTML)
	if dir == "" {
		t.Fatal("savePlanArtifacts returned empty dir — the large plan was not saved")
	}

	path, _, isPointer, exists := plan.PlanHTMLPath(dir)
	if !exists {
		t.Fatalf("plan.html missing at %s", path)
	}
	if isPointer {
		t.Fatalf("large render was committed as an LFS pointer with no reachable store — a poisoned pointer (GH #810)")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read plan.html: %v", err)
	}
	// The FULL render must survive, not just a stray marker: the body repeats the
	// marker 30000 times and every one must be on disk, plain. dehydrate rewrites
	// plan.html BEFORE commitPlanToLedger stages the working tree, so this on-disk
	// file is exactly what a commit would carry.
	if n := bytes.Count(got, []byte("PRESERVE-ME")); n != 30000 {
		t.Fatalf("plan.html retained %d of 30000 markers — the render was truncated or replaced", n)
	}
}
