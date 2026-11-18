package livesession_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/sessionid"
)

const testBudget = 5 * time.Second

type env struct {
	t   *testing.T
	srv *livesessiontest.Server
	cfg livesession.Config
	dir string
	st  livesession.State
}

// newEnv opens a streamed session against a fresh fake server.
func newEnv(t *testing.T) *env {
	t.Helper()
	srv := livesessiontest.New(t)
	e := &env{t: t, srv: srv, cfg: srv.Config(t), dir: t.TempDir()}
	st, err := livesession.Open(t.Context(), e.cfg, e.dir, livesession.OpenParams{
		SessionID: sessionid.GenerateSessionID(), SessionName: "2026-03-04T13-41-person-a-Oxabc1",
		RepoID: "repo_test", Agent: "claude-code", StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	e.st = st
	return e
}

// lines builds NDJSON lines [from, to), about 1 KB each so a few hundred cross a batch boundary.
func lines(from, to int) []byte {
	var b bytes.Buffer
	for i := from; i < to; i++ {
		fmt.Fprintf(&b, `{"type":"msg","n":%d,"pad":"%s"}`+"\n", i, strings.Repeat("x", 1000))
	}
	return b.Bytes()
}

func (e *env) spool(batch []byte) {
	e.t.Helper()
	if _, err := livesession.Spool(e.t.Context(), e.dir, batch, livesession.SpoolOptions{}); err != nil {
		e.t.Fatalf("spool: %v", err)
	}
}

func (e *env) drain() (livesession.DrainResult, error) {
	e.t.Helper()
	return livesession.Drain(e.t.Context(), e.cfg, e.dir, testBudget)
}

func (e *env) mustDrain() livesession.DrainResult {
	e.t.Helper()
	res, err := e.drain()
	if err != nil {
		e.t.Fatalf("drain: %v", err)
	}
	return res
}

func (e *env) spoolBytes() []byte {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dir, "live.ndjson"))
	if err != nil {
		e.t.Fatalf("read spool: %v", err)
	}
	return b
}

func (e *env) state() livesession.State {
	e.t.Helper()
	st, err := livesession.ReadState(e.dir)
	if err != nil {
		e.t.Fatalf("read state: %v", err)
	}
	return st
}

func (e *env) stored(st livesession.State) []byte {
	e.t.Helper()
	return e.srv.Stored(e.t, st.ConversationID, st.LayerID)
}
