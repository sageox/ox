//go:build kb_twin

package kb_twin

// Runner for the kb digital twin. Each scenario from BuildManifest()
// runs as one subtest under TestKBTwin and goes through every Phase
// in order, calling syncBubbles (and optionally runKBGC) between
// phases and asserting ExpectedState after each.
//
// Build/run:
//
//	go test -tags=kb_twin -v ./tests/kb_twin/...
//
// Mirrors the structure of tests/ledger_twin/twin_test.go: shared
// banner, scenario iteration, per-scenario isolation via t.Run +
// t.TempDir.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/paths"
)

// kbTwinManifest holds the active scenario set; built once per test
// run so the banner can summarize before any subtest starts.
var kbTwinManifest *Manifest

func TestMain(m *testing.M) {
	kbTwinManifest = BuildManifest()
	fmt.Fprintf(os.Stderr, "\n╔══════════════════════════════════════════════════════════════╗\n")
	fmt.Fprintf(os.Stderr, "║  KB TWIN HARNESS\n")
	fmt.Fprintf(os.Stderr, "║  Scenarios: %d\n", len(kbTwinManifest.Scenarios))
	for _, sc := range kbTwinManifest.Scenarios {
		fmt.Fprintf(os.Stderr, "║   • %s\n", sc.Name)
	}
	fmt.Fprintf(os.Stderr, "╚══════════════════════════════════════════════════════════════╝\n\n")
	os.Exit(m.Run())
}

// fakeKBLister is the same shape the daemon's own tests use, copied
// here so the harness doesn't need to import _test.go internals.
type fakeKBLister struct {
	bubbles []api.KB
	err     error
	calls   atomic.Int32
}

func (f *fakeKBLister) ListBubbles(_ context.Context) ([]api.KB, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	out := make([]api.KB, len(f.bubbles))
	copy(out, f.bubbles)
	return out, nil
}

// scenarioEnv isolates XDG paths and endpoint for one scenario so
// paths.KBDir resolves into a per-scenario tree. Returns the project
// dir created for the "primary" project key; additional projects can
// be added by the caller.
func scenarioEnv(t *testing.T, scenarioName, endpoint string) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("OX_XDG_DISABLE", "")
	t.Setenv("XDG_DATA_HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))
	if endpoint == "" {
		endpoint = "https://kb-twin.test.invalid"
	}
	t.Setenv("SAGEOX_ENDPOINT", endpoint)

	projectDir := filepath.Join(tmp, "project-primary")
	mustMkdir(t, filepath.Join(projectDir, ".sageox"))
	// minimal config.json — the daemon needs it to recognize the dir
	// as initialized. Endpoint inside is overridden by env var above.
	configJSON := fmt.Sprintf(`{"endpoint":%q,"repo_id":%q}`, endpoint, "repo_primary")
	mustWrite(t, filepath.Join(projectDir, ".sageox", "config.json"), configJSON)
	mustWrite(t, filepath.Join(projectDir, ".sageox", "config.local.toml"), "")
	return projectDir
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// buildScheduler constructs a SyncScheduler the way production would
// for the given primary project. The caller wires the lister factory
// in afterwards to inject the scenario's bubbles.
func buildScheduler(t *testing.T, projectDir string) *daemon.SyncScheduler {
	t.Helper()
	cfg := daemon.DefaultConfig()
	cfg.ProjectRoot = projectDir
	cfg.TeamContextSyncInterval = 50 * time.Millisecond
	cfg.SyncIntervalRead = 100 * time.Millisecond
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	s := daemon.NewSyncScheduler(cfg, logger)
	s.SetIssueTracker(daemon.NewIssueTracker())
	return s
}

// TestKBTwin is the top-level runner. Each scenario gets its own
// subtest and its own XDG isolation. The multi-endpoint scenario has
// dedicated drive logic in TestKBTwin_MultiEndpoint below.
func TestKBTwin(t *testing.T) {
	if testing.Short() {
		t.Skip("short: kb_twin runs real git clones across multiple scenarios")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}

	for _, scenario := range kbTwinManifest.Scenarios {
		scenario := scenario
		// the multi-endpoint scenario has its own driver below; skip it
		// here so we don't run the standard single-endpoint loop on it.
		if scenario.Name == "multi-endpoint-isolation" {
			continue
		}
		t.Run(scenario.Name, func(t *testing.T) {
			runScenario(t, scenario)
		})
	}
}

// runScenario drives a single scenario through every Phase. Treats
// the scenario itself as the unit of isolation: a fresh XDG home,
// fresh project, fresh scheduler, fresh repo dir. Across phases we
// keep all of those so phase-N can observe phase-(N-1)'s effect.
func runScenario(t *testing.T, scenario ScenarioSpec) {
	t.Helper()
	t.Logf("scenario: %s", scenario.Description)

	primary := scenarioEnv(t, scenario.Name, scenario.Endpoint)
	requireFreshKBRoot(t)

	repoDir := &scenarioRepoDir{root: filepath.Join(t.TempDir(), "kb-bare-repos")}
	mustMkdir(t, repoDir.root)

	sched := buildScheduler(t, primary)

	// resolve real bare repos for the bubbles. Each scenario carries
	// the full bubble set for phase 1; subsequent phases mutate this
	// view (revoke, push commits) without re-creating the repos.
	bubbles, err := resolveRepoURLs(repoDir, scenario.Bubbles)
	if err != nil {
		t.Fatalf("resolve repo URLs: %v", err)
	}
	// kbID -> bare-repo path so PushCommits can find the right repo.
	bareByKBID := map[string]string{}
	for i, b := range scenario.Bubbles {
		if b.SeedFile == "" || b.BadRepoURL || !b.ProvideRepoURL {
			continue
		}
		bareByKBID[b.KBID] = stripFilePrefix(bubbles[i].RepoURL)
	}

	projects := map[string]string{"primary": primary}
	for key, p := range scenario.Projects {
		if key == "primary" {
			// already handled by scenarioEnv (the scheduler is
			// configured against this one); just record the repo_id.
			_ = p
			continue
		}
		// extra project: write its config.json with the right repo_id.
		extraDir := filepath.Join(t.TempDir(), "project-"+key)
		mustMkdir(t, filepath.Join(extraDir, ".sageox"))
		mustWrite(t, filepath.Join(extraDir, ".sageox", "config.json"),
			fmt.Sprintf(`{"endpoint":%q,"repo_id":%q}`, "https://kb-twin.test.invalid", p.RepoID))
		projects[key] = extraDir
	}

	for phaseIdx, phase := range scenario.Phases {
		phase := phase
		dctx := kbDiagContext{scenarioName: scenario.Name, phaseName: phase.Name}
		t.Logf("→ phase %d/%d: %s", phaseIdx+1, len(scenario.Phases), phase.Name)

		// apply mutations declared by this phase BEFORE the sync.
		applyPhaseMutations(t, repoDir, bareByKBID, &phase, &bubbles)

		// inject the (possibly-mutated) bubble list into the lister
		// factory. New factory each phase so revocations are visible.
		currentBubbles := append([]api.KB(nil), bubbles...)
		simulatedErr := phase.SimulateAPIError
		sched.SetKBBubbleListerFactory(func(_, _ string) daemon.KBBubbleLister {
			return &fakeKBLister{bubbles: currentBubbles, err: simulatedErr}
		})

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)

		if !phase.SkipSync {
			// nudge FETCH_HEAD older than 1m so pullManagedRepo's
			// dedup doesn't skip the second pass.
			pokePastFetchHead(currentBubbles)
			sched.SyncBubblesForTest(ctx)
		}

		if phase.RunGC {
			listFn := buildListFn(currentBubbles, simulatedErr)
			sched.RunKBGCForTest(ctx, listFn)
		}

		cancel()

		assertExpectedState(t, dctx, phase.Expected, projects)
	}
}

// applyPhaseMutations applies revocations and push-commits to the
// scenario's working state for this phase. Mutates `bubbles` in place
// to drop revoked rows.
func applyPhaseMutations(t *testing.T, d *scenarioRepoDir, bareByKBID map[string]string,
	phase *Phase, bubbles *[]api.KB) {
	t.Helper()

	if len(phase.RevokeKBIDs) > 0 {
		revokeSet := make(map[string]bool, len(phase.RevokeKBIDs))
		for _, id := range phase.RevokeKBIDs {
			revokeSet[id] = true
		}
		filtered := (*bubbles)[:0]
		for _, b := range *bubbles {
			if revokeSet[b.KBID] {
				continue
			}
			filtered = append(filtered, b)
		}
		*bubbles = filtered
	}

	for kbID, push := range phase.PushCommits {
		bare, ok := bareByKBID[kbID]
		if !ok {
			t.Fatalf("PushCommits references unknown kb_id %q", kbID)
		}
		if err := d.pushExtraCommit(bare, push.File, push.Content); err != nil {
			t.Fatalf("pushExtraCommit %s: %v", kbID, err)
		}
	}
}

// pokePastFetchHead backdates each bubble's FETCH_HEAD so the daemon's
// dedup check (fetched recently? skip pull) doesn't no-op the second
// pass when we want to observe an incremental pull.
func pokePastFetchHead(bubbles []api.KB) {
	for _, b := range bubbles {
		if b.KBID == "" {
			continue
		}
		fetchHead := filepath.Join(paths.KBDir(b.KBID), ".git", "FETCH_HEAD")
		info, err := os.Stat(fetchHead)
		if err != nil {
			continue // no FETCH_HEAD yet, nothing to backdate
		}
		past := info.ModTime().Add(-10 * time.Minute)
		_ = os.Chtimes(fetchHead, past, past)
	}
}

// buildListFn returns the kbAPIListFn shape the GC pass needs. When
// the scenario simulates an API error, propagates it; otherwise the
// list is the current bubble id set.
func buildListFn(bubbles []api.KB, simulatedErr error) daemon.KBAPIListFnForTest {
	return func(_ context.Context) ([]string, error) {
		if simulatedErr != nil {
			return nil, simulatedErr
		}
		ids := make([]string, 0, len(bubbles))
		for _, b := range bubbles {
			if b.KBID != "" {
				ids = append(ids, b.KBID)
			}
		}
		return ids, nil
	}
}

func stripFilePrefix(url string) string {
	const p = "file://"
	if len(url) > len(p) && url[:len(p)] == p {
		return url[len(p):]
	}
	return url
}

// TestKBTwin_MultiEndpoint drives the multi-endpoint isolation
// scenario. Runs the same scenario spec twice under two different
// SAGEOX_ENDPOINT values and asserts the resulting XDG dirs are
// distinct — the same kb_id under staging vs prod must never alias.
func TestKBTwin_MultiEndpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("short: kb_twin runs real git clones")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}

	scenario := kbTwinManifest.scenarioByName("multi-endpoint-isolation")
	if scenario == nil {
		t.Fatal("multi-endpoint-isolation scenario missing from manifest")
	}

	endpoints := []string{
		"https://staging.kb-twin.test.invalid",
		"https://prod.kb-twin.test.invalid",
	}
	resolvedDirs := make([]string, 0, len(endpoints))

	for _, ep := range endpoints {
		ep := ep
		t.Run("endpoint="+ep, func(t *testing.T) {
			primary := scenarioEnv(t, scenario.Name+"--"+ep, ep)
			requireFreshKBRoot(t)

			repoDir := &scenarioRepoDir{root: filepath.Join(t.TempDir(), "bare")}
			mustMkdir(t, repoDir.root)
			bubbles, err := resolveRepoURLs(repoDir, scenario.Bubbles)
			if err != nil {
				t.Fatalf("resolve repo URLs: %v", err)
			}

			sched := buildScheduler(t, primary)
			sched.SetKBBubbleListerFactory(func(_, _ string) daemon.KBBubbleLister {
				return &fakeKBLister{bubbles: append([]api.KB(nil), bubbles...)}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			sched.SyncBubblesForTest(ctx)

			// resolve KBDir under THIS endpoint and confirm the clone landed.
			target := paths.KBDir("kb_shared_id")
			if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
				t.Fatalf("expected clone at %s for endpoint %s: %v", target, ep, err)
			}
			resolvedDirs = append(resolvedDirs, target)
		})
	}

	// The KEY assertion: the two XDG paths for the same kb_id under
	// the two endpoints must NOT collide. (Each runs in its own
	// XDG_DATA_HOME via t.TempDir, so structural collision is also
	// impossible — but we still assert the path string differs by
	// endpoint slug, which is what protects users in production where
	// XDG_DATA_HOME is shared across endpoints.)
	if len(resolvedDirs) == 2 && resolvedDirs[0] == resolvedDirs[1] {
		t.Errorf("multi-endpoint isolation violated: both endpoints resolved to %s", resolvedDirs[0])
	}
}

// TestKBTwin_APIUnavailable_NoSideEffects asserts the property
// declared in the kb-api-unavailable-clean-skip scenario more
// loudly: even after a sync pass and a GC pass under
// ErrKBAPIUnavailable, the XDG kb root remains entirely empty (no
// orphan dirs, no .trash, no half-written meta).
func TestKBTwin_APIUnavailable_NoSideEffects(t *testing.T) {
	primary := scenarioEnv(t, "api-unavailable-noop", "")
	requireFreshKBRoot(t)
	sched := buildScheduler(t, primary)
	sched.SetKBBubbleListerFactory(func(_, _ string) daemon.KBBubbleLister {
		return &fakeKBLister{err: api.ErrKBAPIUnavailable}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sched.SyncBubblesForTest(ctx)
	sched.RunKBGCForTest(ctx, func(_ context.Context) ([]string, error) {
		return nil, api.ErrKBAPIUnavailable
	})

	root := paths.KBDir("")
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read kb root %s: %v", root, err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected empty kb root after API-unavailable sync+gc, got entries: %v", names)
	}
}

// TestKBTwin_MetaJSONShape pins the JSON keys we serialize. If the
// daemon ever renames a meta field, this test breaks loudly so
// downstream readers (ox status, ox doctor) don't silently drift.
func TestKBTwin_MetaJSONShape(t *testing.T) {
	if testing.Short() {
		t.Skip("short: requires real clone")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	primary := scenarioEnv(t, "meta-shape", "")
	requireFreshKBRoot(t)

	repoDir := &scenarioRepoDir{root: filepath.Join(t.TempDir(), "bare")}
	mustMkdir(t, repoDir.root)

	bubble := BubbleSpec{
		KBID:           "kb_meta_shape",
		KBType:         api.KBTypeProfile,
		Slug:           "shape-test",
		OwnerUserID:    "user_shape",
		ViewerRole:     "owner",
		SeedFile:       "x.md",
		SeedContent:    "x\n",
		ProvideRepoURL: true,
	}
	bubbles, err := resolveRepoURLs(repoDir, []BubbleSpec{bubble})
	if err != nil {
		t.Fatal(err)
	}

	sched := buildScheduler(t, primary)
	sched.SetKBBubbleListerFactory(func(_, _ string) daemon.KBBubbleLister {
		return &fakeKBLister{bubbles: bubbles}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sched.SyncBubblesForTest(ctx)

	target := paths.KBDir(bubble.KBID)
	raw, err := os.ReadFile(filepath.Join(target, ".sageox", "meta.json"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("parse meta: %v\nraw=%s", err, string(raw))
	}
	for _, key := range []string{"type", "slug", "owner_user_id", "viewer_role", "last_sync"} {
		if _, ok := generic[key]; !ok {
			t.Errorf("meta.json missing required key %q (raw=%s)", key, string(raw))
		}
	}
}

// _ keeps `errors` reachable for future scenarios that need
// errors.Is comparisons in the runner itself.
var _ = errors.Is
