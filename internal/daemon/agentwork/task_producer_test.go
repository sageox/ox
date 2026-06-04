package agentwork

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/agenttask"
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
	m.produceAgentTasks()
	store, _ := agenttask.NewStore(root)
	tasks, _ := store.List(false)
	if len(tasks) != 0 {
		t.Fatalf("expected no tasks without marker, got %d", len(tasks))
	}

	// drop the marker → one doctor task
	if err := os.WriteFile(filepath.Join(sageox, needsDoctorAgentMarker), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m.produceAgentTasks()
	tasks, _ = store.List(false)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 doctor task, got %d", len(tasks))
	}
	if tasks[0].Kind != "doctor" || tasks[0].Source != "daemon" || tasks[0].DedupKey != "doctor-agent" {
		t.Fatalf("unexpected task: %+v", tasks[0])
	}

	// running again must not duplicate (dedup key still active)
	m.produceAgentTasks()
	tasks, _ = store.List(false)
	if len(tasks) != 1 {
		t.Fatalf("expected dedup to keep 1 task, got %d", len(tasks))
	}
}

// TestProduceAgentTasks_NoProjectRoot verifies the producer is a no-op without
// a project root (e.g. ledger-only daemon configuration).
func TestProduceAgentTasks_NoProjectRoot(t *testing.T) {
	m := newProducerManager(t, "")
	m.produceAgentTasks() // must not panic
}
