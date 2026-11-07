package lfs_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitLabStore is a project's LFS store as the push path meets it: the Batch API
// for upload, verify and download, plus forget, which is what GitLab
// housekeeping does to an object no pushed ref points at.
type gitLabStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	present string // object list the remote's pre-receive hook reads
	url     string
}

func newGitLabStore(t *testing.T) *gitLabStore {
	t.Helper()
	s := &gitLabStore{objects: map[string][]byte{}, present: filepath.Join(t.TempDir(), "lfs-present")}
	s.publish(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.serve(t, w, r) }))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

func (s *gitLabStore) client() *lfs.Client { return lfs.NewClient(s.url, "oauth2", "token") }

func (s *gitLabStore) has(oid string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, ok := s.objects[oid]
	return content, ok
}

func (s *gitLabStore) forget(t *testing.T, oid string) {
	t.Helper()
	s.mu.Lock()
	delete(s.objects, oid)
	s.mu.Unlock()
	s.publish(t)
}

// publish writes the stored object ids for the pre-receive hook.
func (s *gitLabStore) publish(t *testing.T) {
	s.mu.Lock()
	oids := make([]string, 0, len(s.objects))
	for oid := range s.objects {
		oids = append(oids, oid)
	}
	s.mu.Unlock()
	sort.Strings(oids)
	require.NoError(t, os.WriteFile(s.present, []byte(strings.Join(oids, "\n")+"\n"), 0o644))
}

func (s *gitLabStore) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(body)
		oid := strings.TrimPrefix(r.URL.Path, "/objects/")
		if hex.EncodeToString(sum[:]) != oid {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		s.mu.Lock()
		s.objects[oid] = body
		s.mu.Unlock()
		s.publish(t)
		w.WriteHeader(http.StatusOK)
	case strings.HasSuffix(r.URL.Path, "/verify"):
		var obj struct {
			OID string `json:"oid"`
		}
		_ = json.NewDecoder(r.Body).Decode(&obj)
		if _, ok := s.has(obj.OID); !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		var req struct {
			Operation string            `json:"operation"`
			Objects   []lfs.BatchObject `json:"objects"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp := lfs.BatchResponse{Transfer: "basic"}
		for _, o := range req.Objects {
			item := lfs.BatchResponseObject{OID: o.OID, Size: o.Size}
			_, stored := s.has(o.OID)
			switch {
			case req.Operation == "download" && stored:
				item.Actions = &lfs.Actions{Download: &lfs.Action{Href: s.url + "/objects/" + o.OID}}
			case req.Operation == "download":
				item.Error = &lfs.ObjectError{Code: http.StatusNotFound, Message: "Object does not exist on the server"}
			case !stored:
				item.Actions = &lfs.Actions{
					Upload: &lfs.Action{Href: s.url + "/objects/" + o.OID},
					Verify: &lfs.Action{Href: s.url + "/verify"},
				}
			}
			resp.Objects = append(resp.Objects, item)
		}
		w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// gitLabPreReceive refuses a push that introduces an LFS pointer whose object
// the store does not hold, with GitLab's own message, as GitLab's pre-receive
// LFS integrity check does.
const gitLabPreReceive = `#!/bin/sh
present='PRESENT'
zero=0000000000000000000000000000000000000000
while read old new ref; do
  if [ "$old" = "$zero" ]; then range="$new"; else range="$old..$new"; fi
  for obj in $(git rev-list --objects $range | cut -d' ' -f1); do
    [ "$(git cat-file -t "$obj")" = blob ] || continue
    [ "$(git cat-file -s "$obj")" -le 200 ] || continue
    git cat-file -p "$obj" | head -n 1 | grep -q '^version https://git-lfs.github.com/spec/v1$' || continue
    oid=$(git cat-file -p "$obj" | sed -n 's/^oid sha256://p')
    if ! grep -qx "$oid" "$present"; then
      echo 'GitLab: LFS objects are missing. Ensure LFS is properly set up or try a manual "git lfs push --all".' >&2
      exit 1
    fi
  done
done
exit 0
`

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// newGitLabLedger is a Ledger clone whose remote checks LFS pointers like GitLab.
func newGitLabLedger(t *testing.T, store *gitLabStore) (ledger, remote string) {
	t.Helper()
	remote = t.TempDir()
	run(t, remote, "init", "--bare", "--quiet", "--initial-branch=main")
	hook := strings.Replace(gitLabPreReceive, "PRESENT", filepath.ToSlash(store.present), 1)
	require.NoError(t, os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte(hook), 0o755))
	ledger = t.TempDir()
	out, err := exec.Command("git", "clone", "--quiet", remote, ledger).CombinedOutput()
	require.NoError(t, err, "clone: %s", out)
	run(t, ledger, "config", "user.email", "person@test.sageox.ai")
	run(t, ledger, "config", "user.name", "Person A")
	run(t, ledger, "symbolic-ref", "HEAD", "refs/heads/main")
	commitFiles(t, ledger, "init", map[string][]byte{"sessions/.gitkeep": nil})
	run(t, ledger, "push", "--quiet", "-u", "origin", "main")
	return ledger, remote
}

func commitFiles(t *testing.T, ledger, message string, files map[string][]byte) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(ledger, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, content, 0o644))
		run(t, ledger, "add", "--", rel)
	}
	run(t, ledger, "commit", "--quiet", "--no-verify", "-m", message)
}

// pushLedger is the push the CLI and daemon run: retry, and on "LFS objects are
// missing", the reconcile, then one more push.
func pushLedger(t *testing.T, ledger string, client *lfs.Client) error {
	t.Helper()
	ctx := context.Background()
	return gitutil.PushWithRetry(ctx, ledger, gitutil.PushOpts{
		ImmutablePaths: true,
		ReconcileLFS: func(repoPath string) (bool, error) {
			result, err := lfs.ReconcileWithClient(ctx, repoPath, client, slog.Default())
			if err != nil {
				return false, err
			}
			return result.Changed(), nil
		},
	})
}

// a page past the 1 MiB threshold, so the save stores it as an LFS pointer
func largePage() []byte {
	return []byte("<!doctype html><html><head><title>Launch mockup</title></head><body>" +
		strings.Repeat("<p>mockup direction</p>", 60000) + "</body></html>")
}

// TestPlanPage_StoreDropsTheBlobBeforeThePush_PushLandsWithThePage is the root
// cause, end to end. `ox plan save` uploads the page, verifies it, and commits
// its pointer; the push lags; GitLab housekeeping deletes the object because no
// pushed ref points at it yet. The push is refused, and the repair must restore
// the page from this machine. Failure prevented: the save left no local copy,
// so the pointer could never be satisfied and every later push was refused
// (the 2026-10-24 wedge).
func TestPlanPage_StoreDropsTheBlobBeforeThePush_PushLandsWithThePage(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories and a mocked LFS store")
	}
	lfs.UseBundledSessionRules(t)
	store := newGitLabStore(t)
	ledger, remote := newGitLabLedger(t, store)
	page := largePage()
	ref, ok, err := plan.UploadHTML(store.client(), page)
	require.NoError(t, err)
	require.True(t, ok, "the page must take the LFS path")
	dir := filepath.Join(ledger, "data", "plans", "2026-10-24-launch-mockup")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plan.html"), page, 0o644))
	swapped, err := plan.PointerizeHTML(dir, ref)
	require.NoError(t, err)
	require.True(t, swapped)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plan.md"), []byte("# Launch mockup\n"), 0o644))
	run(t, ledger, "add", "--", "data/plans")
	run(t, ledger, "commit", "--quiet", "--no-verify", "-m", "plan: 2026-10-24-launch-mockup")

	store.forget(t, ref.BareOID())

	require.NoError(t, pushLedger(t, ledger, store.client()))
	assert.Equal(t, run(t, ledger, "rev-parse", "HEAD"), run(t, remote, "rev-parse", "main"), "the push landed")
	pushedPage := run(t, remote, "show", "main:data/plans/2026-10-24-launch-mockup/plan.html") + "\n"
	assert.Equal(t, lfs.FormatPointer(ref.Ref().OID, ref.Ref().Size), pushedPage, "teammates get the page, not a tombstone")
	stored, ok := store.has(ref.BareOID())
	require.True(t, ok, "the page was uploaded again")
	assert.True(t, bytes.Equal(page, stored))
}

// TestLostPlanPage_WedgedLedgerPushesAgain is the self-heal, end to end, on a
// Ledger an older build already poisoned: the page's pointer is committed, the
// object exists nowhere, and later work is queued behind it. Failure prevented:
// the clone refusing every push until a human rewrote its history.
func TestLostPlanPage_WedgedLedgerPushesAgain(t *testing.T) {
	if testing.Short() {
		t.Skip("short: real git repositories and a mocked LFS store")
	}
	lfs.UseBundledSessionRules(t)
	store := newGitLabStore(t)
	ledger, remote := newGitLabLedger(t, store)
	pushedBefore := run(t, remote, "rev-parse", "main")
	planDir := "data/plans/2026-10-24-launch-mockup/"
	lost := lfs.NewFileRef(largePage())
	text := []byte("# Launch mockup\n")
	commitFiles(t, ledger, "plan: 2026-10-24-launch-mockup", map[string][]byte{
		planDir + "plan.md":      text,
		planDir + "events.jsonl": []byte(`{"plan_id":"pln_01","event":"created"}` + "\n"),
		planDir + "plan.html":    []byte(lfs.FormatPointer(lost.OID, lost.Size)),
	})
	commitFiles(t, ledger, "murmur: queued", map[string][]byte{"data/murmurs/m1.json": []byte(`{"text":"queued"}` + "\n")})
	commitFiles(t, ledger, "plan: queued", map[string][]byte{"data/plans/2026-11-02-next/plan.md": []byte("# next\n")})
	out, err := exec.Command("git", "-C", ledger, "push", "--quiet").CombinedOutput()
	require.Error(t, err, "the remote must refuse the dead pointer like GitLab")
	require.Contains(t, string(out), "LFS objects are missing")

	require.NoError(t, pushLedger(t, ledger, store.client()))

	assert.Equal(t, run(t, ledger, "rev-parse", "HEAD"), run(t, remote, "rev-parse", "main"), "the push landed")
	run(t, remote, "merge-base", "--is-ancestor", pushedBefore, "main") // fast-forward: pushed history untouched
	assert.Contains(t, run(t, remote, "show", "main:"+planDir+"plan.html"), "sageox:render-lost")
	assert.Equal(t, strings.TrimSpace(string(text)), run(t, remote, "show", "main:"+planDir+"plan.md"))
	assert.Equal(t, `{"text":"queued"}`, run(t, remote, "show", "main:data/murmurs/m1.json"))
	assert.Equal(t, "# next", run(t, remote, "show", "main:data/plans/2026-11-02-next/plan.md"))
}
