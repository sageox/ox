package sessionpublication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/pkg/sessionprovenance"
	"github.com/stretchr/testify/require"
)

func TestVerifyFreshRemoteReceiptAndBlob(t *testing.T) {
	ctx := context.Background()
	temp := t.TempDir()
	remote := filepath.Join(temp, "remote.git")
	local := filepath.Join(temp, "local")
	git := func(dir string, args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false", "-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git(temp, "init", "--bare", remote)
	git(temp, "clone", remote, local)
	name := "test-session"
	native := "0197d3f4-2c88-7a15-a9b0-4b5c6d7e8f04"
	raw := []byte("{\"type\":\"user\",\"content\":\"first middle last\"}\n")
	hash := sha256.Sum256(raw)
	oid := hex.EncodeToString(hash[:])
	source := &sessionprovenance.Source{Version: 1, Agent: "codex", NativeSessionID: native, Generation: strings.Repeat("a", 64), Ranges: []sessionprovenance.Range{{Start: 0, End: 100}}}
	meta := &lfs.SessionMeta{RepoID: "repo_test", Source: source, Files: map[string]lfs.FileRef{"raw.jsonl": {OID: "sha256:" + oid, Size: int64(len(raw))}}}
	record := &sessionprovenance.Record{Version: 1, Agent: "codex", NativeSessionID: native, Generation: source.Generation, Coverage: []sessionprovenance.Coverage{{Start: 0, End: 100, SessionName: name, RawOID: oid}}}
	write := func(path string, value any) {
		b, err := json.Marshal(value)
		require.NoError(t, err)
		p := filepath.Join(local, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0700))
		require.NoError(t, os.WriteFile(p, b, 0600))
	}
	path, err := sessionprovenance.Path(native)
	require.NoError(t, err)
	write("sessions/"+name+"/meta.json", meta)
	write(path, record)
	require.NoError(t, os.WriteFile(filepath.Join(local, "sessions", name, "raw.jsonl"), []byte(lfs.FormatPointer("sha256:"+oid, int64(len(raw)))), 0600))
	git(local, "add", ".")
	git(local, "commit", "-m", "fixture")
	git(local, "push", "-u", "origin", "HEAD")
	var available atomic.Bool
	available.Store(true)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/objects/batch") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"objects":[{"oid":%q,"size":%d,"actions":{"download":{"href":%q}}}]}`, oid, len(raw), server.URL+"/blob")
			return
		}
		if !available.Load() {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	client := lfs.NewClient(server.URL, "test", "test")
	require.NoError(t, Verify(ctx, local, name, meta, client))
	available.Store(false)
	require.ErrorContains(t, Verify(ctx, local, name, meta, client), "blob verification failed")
	available.Store(true)
	record.Exclusions = []sessionprovenance.Exclusion{{Start: 0, End: -1, Reason: "deleted"}}
	write(path, record)
	git(local, "add", ".")
	git(local, "commit", "-m", "exclude")
	git(local, "push")
	require.ErrorContains(t, Verify(ctx, local, name, meta, client), "excluded")
}

func TestVerifyInlineBlobChecksSizeWithoutReceiptLimit(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false", "-c", "user.name=Test", "-c", "user.email=test@example.com"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("init")
	f, err := os.Create(filepath.Join(dir, "large.md"))
	require.NoError(t, err)
	require.NoError(t, f.Truncate(5*1024*1024))
	require.NoError(t, f.Close())
	require.NoError(t, os.Symlink("large.md", filepath.Join(dir, "link.md")))
	git("add", ".")
	git("commit", "-m", "inline fixture")
	require.NoError(t, verifyInlineBlob(context.Background(), dir, "HEAD", "large.md", 5*1024*1024))
	require.ErrorContains(t, verifyInlineBlob(context.Background(), dir, "HEAD", "large.md", 1), "size differs")
	require.Error(t, verifyInlineBlob(context.Background(), dir, "HEAD", "missing.md", 0))
	require.Error(t, verifyInlineBlob(context.Background(), dir, "HEAD", "link.md", 8))
}
