package livesession_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/sessionid"
)

func TestKeyFileIs0600AndReusedAcrossCalls(t *testing.T) {
	cfg := livesessiontest.New(t).Config(t)
	k1, err := livesession.LoadOrCreateKey(t.Context(), cfg, cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := livesession.LoadOrCreateKey(t.Context(), cfg, cfg.Endpoint)
	if err != nil || !k1.Equal(k2) {
		t.Fatalf("second call returned a different key: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(cfg.KeyDir, "*.key"))
	if len(files) != 1 {
		t.Fatalf("key files = %v, want exactly one per endpoint", files)
	}
	info, _ := os.Stat(files[0])
	dirInfo, _ := os.Stat(cfg.KeyDir)
	if info.Mode().Perm() != 0o600 || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("key mode=%v dir mode=%v, want 0600 and 0700", info.Mode().Perm(), dirInfo.Mode().Perm())
	}
}

func TestOpenKeyConflictDisablesStreamingWithoutDeletingKey(t *testing.T) {
	srv := livesessiontest.New(t)
	cfg := srv.Config(t)
	srv.ForceKeyConflict(true)
	dir := t.TempDir()

	st, err := livesession.Open(t.Context(), cfg, dir, livesession.OpenParams{SessionID: sessionid.GenerateSessionID(), StartedAt: time.Now()})
	if !errors.Is(err, livesession.ErrKeyConflict) || st.Status != livesession.StatusDisabled {
		t.Fatalf("st=%+v err=%v, want disabled with ErrKeyConflict", st, err)
	}
	if files, _ := filepath.Glob(filepath.Join(cfg.KeyDir, "*.key")); len(files) != 1 {
		t.Fatalf("key files = %v, a conflict must not delete the key", files)
	}
	if _, err := livesession.Spool(t.Context(), dir, lines(0, 1), livesession.SpoolOptions{}); !errors.Is(err, livesession.ErrNotStreaming) {
		t.Fatalf("a disabled session accepted spool: %v", err)
	}
	// nothing reached the server, so abort has nothing to delete
	if res, err := livesession.Abort(t.Context(), cfg, dir, testBudget); err != nil || res != (livesession.AbortResult{}) {
		t.Fatalf("abort of a disabled session: %+v %v", res, err)
	}
}

func TestOpenAgainResumesSameLayerKeepingAck(t *testing.T) {
	e := newEnv(t)
	e.spool(lines(0, 5))
	e.mustDrain()
	acked := e.state().Acked

	st, err := livesession.Open(t.Context(), e.cfg, e.dir, livesession.OpenParams{SessionID: e.st.SessionID})
	if err != nil || st.LayerID != e.st.LayerID || st.Acked != acked || acked == 0 {
		t.Fatalf("reopen: %+v %v (want layer %s acked %d)", st, err, e.st.LayerID, acked)
	}
}

func TestOpenFailureLeavesNoLiveState(t *testing.T) {
	srv := livesessiontest.New(t)
	cfg := srv.Config(t)
	srv.RefuseOpen(1) // the key registers, then the open itself fails
	dir := t.TempDir()
	if _, err := livesession.Open(t.Context(), cfg, dir, livesession.OpenParams{SessionID: sessionid.GenerateSessionID()}); err == nil {
		t.Fatal("open succeeded though the server refused it")
	}
	if _, err := livesession.ReadState(dir); !errors.Is(err, livesession.ErrNotStreaming) {
		t.Fatalf("failed open left state behind: %v", err)
	}
}
