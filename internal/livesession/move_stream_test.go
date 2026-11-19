package livesession_test

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sageox/ox/internal/livesession"
)

// moveFiles renames a stream's files, the stream state last (MoveStream's
// contract), as retiring a stream into .live-<ses> does.
func moveFiles(from, to string) func() error {
	return func() error {
		for _, name := range []string{"spool.jsonl", "stream-state.json"} {
			if err := os.Rename(filepath.Join(from, name), filepath.Join(to, name)); err != nil {
				return err
			}
		}
		return nil
	}
}

// A stream is trusted only in the folder its conversation is bound to, so
// retiring it into .live-<ses> must move the binding with the files. If the
// binding cannot move, nothing does and the stream keeps streaming where it
// is; if the files cannot move, the binding goes back. Either half alone
// strands the stream: its bytes stay local and it never closes.
func TestMoveStreamCarriesTheBindingOrMovesNothing(t *testing.T) {
	setup := func(t *testing.T) (*env, string, *bytes.Buffer) {
		t.Helper()
		e := newEnv(t)
		var log bytes.Buffer
		e.cfg.Logger = slog.New(slog.NewTextHandler(&log, nil))
		e.spool(lines(0, 2))
		e.mustDrain()
		dest := filepath.Join(t.TempDir(), ".live-retired")
		if err := os.MkdirAll(dest, 0o700); err != nil {
			t.Fatal(err)
		}
		return e, dest, &log
	}

	t.Run("moved: it streams and closes from its new folder", func(t *testing.T) {
		e, dest, _ := setup(t)
		if err := livesession.MoveStream(t.Context(), e.cfg, e.state(), e.dir, dest, moveFiles(e.dir, dest)); err != nil {
			t.Fatalf("move: %v", err)
		}
		if _, err := livesession.Spool(t.Context(), dest, lines(2, 3), livesession.SpoolOptions{}); err != nil {
			t.Fatal(err)
		}
		res, err := livesession.Close(t.Context(), e.cfg, dest, "", testBudget)
		if err != nil || res.Status != livesession.StatusClosed {
			t.Fatalf("close in the new folder: %+v %v", res, err)
		}
		st, _ := livesession.ReadState(dest)
		spool, _ := os.ReadFile(filepath.Join(dest, "spool.jsonl"))
		if !bytes.Equal(e.srv.Stored(t, st.ConversationID, st.LayerID), spool) {
			t.Fatal("the moved stream's bytes did not all reach the server")
		}
	})

	t.Run("binding cannot be updated: nothing moves", func(t *testing.T) {
		e, dest, log := setup(t)
		// a registry that cannot be read cannot be updated either
		registry := filepath.Join(e.cfg.DataDir, "live-conversations.json")
		saved, err := os.ReadFile(registry)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(registry); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(registry, 0o700); err != nil {
			t.Fatal(err)
		}
		moved := false
		err = livesession.MoveStream(t.Context(), e.cfg, e.state(), e.dir, dest, func() error { moved = true; return nil })
		if err == nil || moved {
			t.Fatalf("move with an unusable registry: err=%v moved=%v, want an error and nothing moved", err, moved)
		}
		if n := errorEvents(log, "live_session_rebind_failed"); n != 1 {
			t.Fatalf("live_session_rebind_failed logged at Error %d times, want once\n%s", n, log)
		}
		if err := os.Remove(registry); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(registry, saved, 0o600); err != nil {
			t.Fatal(err)
		}
		e.spool(lines(2, 3))
		e.mustDrain()
		if !bytes.Equal(e.stored(e.state()), e.spoolBytes()) {
			t.Fatal("the stream left where it was stopped streaming")
		}
	})

	t.Run("files cannot move: the binding goes back", func(t *testing.T) {
		e, dest, _ := setup(t)
		err := livesession.MoveStream(t.Context(), e.cfg, e.state(), e.dir, dest, func() error { return errors.New("disk full") })
		if err == nil {
			t.Fatal("a failed move reported success")
		}
		e.spool(lines(2, 3))
		e.mustDrain()
		if !bytes.Equal(e.stored(e.state()), e.spoolBytes()) {
			t.Fatal("the stream left where it was stopped streaming")
		}
	})
}
