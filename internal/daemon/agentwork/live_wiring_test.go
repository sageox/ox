package agentwork

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// drainLog records the drains a manager asks for once it has spooled a batch.
type drainLog struct {
	mu   sync.Mutex
	dirs []string
}

func (d *drainLog) drain(dir string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dirs = append(d.dirs, dir)
}

func (d *drainLog) all() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.dirs...)
}

// wireLiveStreaming wires m's capture paths into live streaming as the daemon does
// (daemon.go): committed batches go through the shared live-spool step, and
// each drain it asks for is recorded.
func wireLiveStreaming(m *SessionWatcherManager) *drainLog {
	d := &drainLog{}
	m.SetLiveDrainer(d.drain)
	return d
}

// spoolOf is the live spool of the session in dir; "" when none was written.
func spoolOf(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "spool.jsonl"))
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(b)
}
