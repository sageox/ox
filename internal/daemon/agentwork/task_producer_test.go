package agentwork

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agenttask"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/markers"
)

func newProducerManager(t *testing.T, projectRoot string) *Manager {
	t.Helper()
	return &Manager{
		logger:      slog.Default(),
		projectRoot: projectRoot,
	}
}

// TestProduceAgentTasks_DoctorMarker verifies the daemon converts a
// .needs-doctor-agent marker into a deduped doctor task for live agents.
// Failure prevented: incomplete sessions stay stranded because the daemon
// can't fork its own LLM worker and nothing hands the work to a live agent.
func TestProduceAgentTasks_DoctorMarker(t *testing.T) {
	root := t.TempDir()
	sageox := filepath.Join(root, ".sageox")
	if err := os.MkdirAll(sageox, 0o755); err != nil {
		t.Fatal(err)
	}

	m := newProducerManager(t, root)

	// no marker → no task
	m.produceAgentTasks(m.loadConfig())
	store, _ := agenttask.NewStore(root)
	tasks, _ := store.List(false)
	if len(tasks) != 0 {
		t.Fatalf("expected no tasks without marker, got %d", len(tasks))
	}

	// drop the marker → one doctor task
	if err := os.WriteFile(filepath.Join(sageox, markers.NeedsDoctorAgent), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m.produceAgentTasks(m.loadConfig())
	tasks, _ = store.List(false)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 doctor task, got %d", len(tasks))
	}
	if tasks[0].Kind != "doctor" || tasks[0].Source != "daemon" || tasks[0].DedupKey != "doctor-agent" {
		t.Fatalf("unexpected task: %+v", tasks[0])
	}

	// running again must not duplicate (dedup key still active)
	m.produceAgentTasks(m.loadConfig())
	tasks, _ = store.List(false)
	if len(tasks) != 1 {
		t.Fatalf("expected dedup to keep 1 task, got %d", len(tasks))
	}
}

// TestProduceAgentTasks_NoProjectRoot verifies the producer is a no-op without
// a project root (e.g. ledger-only daemon configuration).
func TestProduceAgentTasks_NoProjectRoot(t *testing.T) {
	m := newProducerManager(t, "")
	m.produceAgentTasks(m.loadConfig()) // must not panic
}

// seedStaleSession writes an abandoned recording (raw.jsonl + a >24h-old
// .recording.json) into ledgerPath/sessions/<name> so SessionFinalizeHandler
// detects it as needing finalization.
func seedStaleSession(t *testing.T, ledgerPath, name string) string {
	t.Helper()
	dir := filepath.Join(ledgerPath, "sessions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"metadata":{"agent_id":"OxFIN","agent_type":"claude","version":"1.0"},"type":"header"}
{"type":"user","content":"do the thing","seq":0,"timestamp":"2026-01-10T09:00:01Z"}
{"type":"assistant","content":"done","seq":1,"timestamp":"2026-01-10T09:00:08Z"}
`
	if err := os.WriteFile(filepath.Join(dir, "raw.jsonl"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, _ := json.Marshal(map[string]any{
		"started_at": time.Now().Add(-25 * time.Hour).Format(time.RFC3339),
		"agent_id":   "OxFIN",
		"session_id": "stale-" + name,
	})
	if err := os.WriteFile(filepath.Join(dir, recordingMarker), rec, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func managerWithFinalizeHandler(projectRoot, ledgerPath string, cfg func() *config.AgentWorkerConfig) *Manager {
	return &Manager{
		logger:       slog.Default(),
		projectRoot:  projectRoot,
		ledgerPath:   ledgerPath,
		handlers:     map[string]WorkHandler{sessionFinalizeType: NewSessionFinalizeHandler(slog.Default())},
		configLoader: cfg,
	}
}

// TestProduceFinalizeTasks_NoWorkerEnqueues verifies that when no local LLM
// worker is authed, the daemon hands stale-session finalization to a live agent
// as a task instead of dropping it.
// Failure prevented: anti-entropy silently strands sessions for 24h when the
// daemon can't run its own claude -p worker.
func TestProduceFinalizeTasks_NoWorkerEnqueues(t *testing.T) {
	ledger := t.TempDir()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	name := "2026-01-10T09-00-testuser-OxFIN"
	seedStaleSession(t, ledger, name)

	m := managerWithFinalizeHandler(project, ledger, nil) // nil cfg => no worker
	m.produceFinalizeTasks(m.loadConfig())

	store, _ := agenttask.NewStore(project)
	tasks, _ := store.List(false)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 finalize task, got %d", len(tasks))
	}
	got := tasks[0]
	if got.Kind != "session-finalize" || got.Source != "daemon" {
		t.Fatalf("unexpected task: %+v", got)
	}
	if got.DedupKey != sessionFinalizeType+":"+name {
		t.Fatalf("unexpected dedup key: %q", got.DedupKey)
	}
	if got.Payload["session"] != name {
		t.Fatalf("expected session payload %q, got %q", name, got.Payload["session"])
	}
}

// TestProduceFinalizeTasks_WorkerEnabledSkips verifies that when a local worker
// IS available, the daemon does NOT also enqueue a task — the normal queue
// forks the worker (delegated mode) and duplicating it would be wasteful.
func TestProduceFinalizeTasks_WorkerEnabledSkips(t *testing.T) {
	ledger := t.TempDir()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedStaleSession(t, ledger, "2026-01-10T09-00-testuser-OxFIN")

	enabled := func() *config.AgentWorkerConfig { return enabledConfigWith(1, 100) }
	m := managerWithFinalizeHandler(project, ledger, enabled)
	m.produceFinalizeTasks(m.loadConfig())

	store, _ := agenttask.NewStore(project)
	tasks, _ := store.List(false)
	if len(tasks) != 0 {
		t.Fatalf("expected 0 tasks when worker enabled, got %d", len(tasks))
	}
}

// TestProduceFinalizeTasks_UsesPassedSnapshot verifies the producer decides on
// the config snapshot it is GIVEN, not a fresh configLoader read. This is the
// load-bearing property behind the single-snapshot-per-tick fix: even though the
// manager's loader reports an enabled worker, passing a nil snapshot makes the
// producer enqueue — proving the two decisions can't straddle a config flip.
// Failure prevented: a disabled→enabled flip between the producer and the worker
// fork double-runs session finalize.
func TestProduceFinalizeTasks_UsesPassedSnapshot(t *testing.T) {
	ledger := t.TempDir()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".sageox"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedStaleSession(t, ledger, "2026-01-10T09-00-testuser-OxFIN")

	// loader says ENABLED, but we hand the producer a nil snapshot.
	enabled := func() *config.AgentWorkerConfig { return enabledConfigWith(1, 100) }
	m := managerWithFinalizeHandler(project, ledger, enabled)
	m.produceFinalizeTasks(nil)

	store, _ := agenttask.NewStore(project)
	tasks, _ := store.List(false)
	if len(tasks) != 1 {
		t.Fatalf("producer must honor the passed snapshot (nil=no worker) and enqueue; got %d tasks", len(tasks))
	}
}

func TestFinalizeTaskFields(t *testing.T) {
	// payload-derived name + missing
	item := &WorkItem{
		DedupKey: "session-finalize:S1",
		Payload:  &SessionFinalizePayload{SessionDir: "/ledger/sessions/S1", Missing: []string{"summary.md", "session.md"}},
	}
	name, missing := finalizeTaskFields(item)
	if name != "S1" || missing != "summary.md, session.md" {
		t.Fatalf("unexpected fields: name=%q missing=%q", name, missing)
	}

	// fallback to dedup-key suffix when payload lacks a dir
	item2 := &WorkItem{DedupKey: "session-finalize:S2", Payload: &SessionFinalizePayload{}}
	if name, _ := finalizeTaskFields(item2); name != "S2" {
		t.Fatalf("expected fallback name S2, got %q", name)
	}
}
