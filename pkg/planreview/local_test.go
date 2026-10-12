package planreview

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func roundRecord(t *testing.T, id, note string) File {
	t.Helper()
	q, err := ParseRoundRequest([]byte(`{"id":"` + id + `","items":[{"anchor":"h1","status":"flag","note":"` + note + `"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	f, err := q.Record("p", "Ana", at(1))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func write(t *testing.T, p string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// symlink makes a symlink or skips: Windows needs a privilege for it, and a
// test that cannot plant the link proves nothing about refusing one.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
}

// TestPublish_IsConfinedAndCreateOnly covers spec 3.5: a record is created
// once and never replaced, and nothing is written through a symlink at any
// component, inside the plan or out. Failure prevented: a retry overwrites a
// stored review, or a symlink in a teammate's Ledger steers a write onto
// another file.
func TestPublish_IsConfinedAndCreateOnly(t *testing.T) {
	t.Parallel()
	rec := roundRecord(t, "r-0001-abcd", "Why?")
	res, _ := ParseResolutionRequest([]byte(`{"id":"accept-0001","anchor":"h1","state":"verified"}`))
	resRec, _ := res.Record("Ana", at(2))
	for _, c := range []struct {
		name    string
		rec     File
		setup   func(t *testing.T, plan, outside string)
		state   string // "" means an error
		written string // the plan-relative path that must hold rec after Publish, if any
		intact  map[string]string
	}{
		{name: "new record", rec: rec, state: ReceiptCreated, written: rec.Path},
		{name: "same record again", rec: rec, setup: func(t *testing.T, plan, _ string) {
			write(t, filepath.Join(plan, rec.Path), string(rec.Data))
		}, state: ReceiptAlreadyPublished, written: rec.Path},
		{name: "other bytes at the name", rec: rec, setup: func(t *testing.T, plan, _ string) {
			write(t, filepath.Join(plan, rec.Path), "other")
		}, state: ReceiptConflict, intact: map[string]string{rec.Path: "other"}},
		{name: "dangling symlink at the name", rec: rec, setup: func(t *testing.T, plan, outside string) {
			symlink(t, filepath.Join(outside, "created"), filepath.Join(plan, rec.Path))
		}, state: ReceiptConflict, intact: map[string]string{"../outside/created": ""}},
		{name: "symlink at the name to a file outside", rec: rec, setup: func(t *testing.T, plan, outside string) {
			write(t, filepath.Join(outside, "target"), "keep")
			symlink(t, filepath.Join(outside, "target"), filepath.Join(plan, rec.Path))
		}, state: ReceiptConflict, intact: map[string]string{"../outside/target": "keep"}},
		{name: "feedback/ is a symlink into the plan", rec: rec, setup: func(t *testing.T, plan, _ string) {
			write(t, filepath.Join(plan, "companions", "a.html"), "page")
			symlink(t, "companions", filepath.Join(plan, "feedback"))
		}, intact: map[string]string{"companions/" + filepath.Base(rec.Path): ""}},
		{name: "resolutions/ is a symlink out of the plan", rec: resRec, setup: func(t *testing.T, plan, outside string) {
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatal(err)
			}
			symlink(t, outside, filepath.Join(plan, "feedback", "resolutions"))
		}, intact: map[string]string{"../outside/" + filepath.Base(resRec.Path): ""}},
		{name: "a traversal path", rec: File{Path: "feedback/../../" + filepath.Base(rec.Path), Data: rec.Data}},
		{name: "a name that is not these bytes", rec: File{Path: rec.Path, Data: []byte("other")}},
		{name: "not an operation's record", rec: File{Path: "feedback/resolutions.json", Data: []byte("[]")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			base := t.TempDir()
			plan, outside := filepath.Join(base, "plan"), filepath.Join(base, "outside")
			if err := os.MkdirAll(plan, 0o755); err != nil {
				t.Fatal(err)
			}
			if c.setup != nil {
				c.setup(t, plan, outside)
			}
			r, err := Publish(plan, c.rec)
			if c.state == "" {
				if err == nil {
					t.Fatalf("Publish = %+v, want an error", r)
				}
			} else if err != nil || r.State != c.state {
				t.Fatalf("Publish = %+v, %v, want %s", r, err, c.state)
			}
			if c.written != "" {
				if got, err := os.ReadFile(filepath.Join(plan, c.written)); err != nil || string(got) != string(c.rec.Data) {
					t.Errorf("%s does not hold the record: %v", c.written, err)
				}
			}
			for rel, want := range c.intact {
				got, err := os.ReadFile(filepath.Join(plan, filepath.FromSlash(rel)))
				if want == "" && !os.IsNotExist(err) {
					t.Errorf("%s was written through", rel)
				} else if want != "" && string(got) != want {
					t.Errorf("%s = %q, want %q unchanged", rel, got, want)
				}
			}
			if leftovers, _ := filepath.Glob(filepath.Join(plan, "feedback", ".*")); len(leftovers) > 0 {
				t.Errorf("temporaries left behind: %v", leftovers)
			}
		})
	}
}

// TestPublish_RefusesADirectorySwappedAfterItsCheck covers the window between
// checking a directory and opening it: a symlink swapped in there, to another
// directory or to the original moved aside, fails the publication. Failure
// prevented: a record lands wherever a racing symlink points.
func TestPublish_RefusesADirectorySwappedAfterItsCheck(t *testing.T) { // not parallel: sets beforeOpen
	rec := roundRecord(t, "r-0001-abcd", "Why?")
	t.Cleanup(func() { beforeOpen = func(string) {} })
	for _, target := range []string{"companions", "feedback.moved"} {
		plan := t.TempDir()
		write(t, filepath.Join(plan, "companions", "a.html"), "page")
		if err := os.Mkdir(filepath.Join(plan, "feedback"), 0o755); err != nil {
			t.Fatal(err)
		}
		beforeOpen = func(name string) {
			if name != "feedback" {
				return
			}
			beforeOpen = func(string) {}
			if err := os.Rename(filepath.Join(plan, "feedback"), filepath.Join(plan, "feedback.moved")); err != nil {
				t.Error(err)
			}
			symlink(t, target, filepath.Join(plan, "feedback"))
		}
		if r, err := Publish(plan, rec); err == nil {
			t.Errorf("swap to %s: Publish = %+v, want an error", target, r)
		}
		if found, _ := filepath.Glob(filepath.Join(plan, "*", filepath.Base(rec.Path))); len(found) > 0 {
			t.Errorf("swap to %s: the record was written through the symlink: %v", target, found)
		}
	}
}

// TestPublish_ConcurrentPublishersLeaveOneRecord: sixteen writers of one
// record race; one creates it, the rest find it already published.
func TestPublish_ConcurrentPublishersLeaveOneRecord(t *testing.T) {
	t.Parallel()
	plan := t.TempDir()
	rec := roundRecord(t, "r-0001-abcd", "Why?")
	states := make([]string, 16)
	var wg sync.WaitGroup
	for i := range states {
		wg.Go(func() {
			r, err := Publish(plan, rec)
			if err != nil {
				t.Error(err)
			}
			states[i] = r.State
		})
	}
	wg.Wait()
	slices.Sort(states)
	if states[0] != ReceiptAlreadyPublished || states[14] != ReceiptAlreadyPublished || states[15] != ReceiptCreated {
		t.Errorf("receipts = %v, want one created and fifteen already published", states)
	}
	entries, _ := os.ReadDir(filepath.Join(plan, "feedback"))
	if len(entries) != 1 {
		t.Errorf("feedback/ holds %d entries, want the one record", len(entries))
	}
}

// TestCapture_ReportsWhatItCannotReadAndNeverFollowsALink covers the failure
// classes of spec 3.6 a local worktree can produce. Failure prevented: a
// symlink planted in a Ledger reads another file as a review, or an entry the
// reader skips leaves a capture that looks complete.
func TestCapture_ReportsWhatItCannotReadAndNeverFollowsALink(t *testing.T) {
	t.Parallel()
	rec := roundRecord(t, "r-0001-abcd", "Why?")
	for _, c := range []struct {
		name   string
		setup  func(t *testing.T, plan, outside string)
		want   []string
		files  int
		limits Limits
	}{
		{"no feedback yet", func(*testing.T, string, string) {}, []string{}, 0, Limits{}},
		{"feedback/ is a symlink", func(t *testing.T, plan, outside string) {
			write(t, filepath.Join(outside, rec.Path), string(rec.Data))
			symlink(t, filepath.Join(outside, "feedback"), filepath.Join(plan, "feedback"))
		}, []string{"feedback not_regular"}, 0, Limits{}},
		{"a record is a symlink", func(t *testing.T, plan, outside string) {
			write(t, filepath.Join(outside, "round.json"), string(rec.Data))
			symlink(t, filepath.Join(outside, "round.json"), filepath.Join(plan, rec.Path))
		}, []string{rec.Path + " not_regular"}, 0, Limits{}},
		{"an unknown file and directory", func(t *testing.T, plan, _ string) {
			write(t, filepath.Join(plan, rec.Path), string(rec.Data))
			write(t, filepath.Join(plan, "feedback", "notes.txt"), "x")
			write(t, filepath.Join(plan, "feedback", "attachments", "a.png"), "x")
			write(t, filepath.Join(plan, "feedback", ".round.json.1a2b.tmp"), "half")
		}, []string{"feedback/attachments unrecognized", "feedback/notes.txt unrecognized"}, 1, Limits{}},
		{"more entries than records allowed", func(t *testing.T, plan, _ string) {
			for _, n := range []string{"round-1.json", "round-2.json", "round-3.json", "round-4.json"} {
				write(t, filepath.Join(plan, "feedback", n), `{}`)
			}
		}, []string{" too_many_records", "feedback too_many_records"}, 1, Limits{Records: 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			base := t.TempDir()
			plan, outside := filepath.Join(base, "plan"), filepath.Join(base, "outside")
			if err := os.MkdirAll(plan, 0o755); err != nil {
				t.Fatal(err)
			}
			c.setup(t, plan, outside)
			inv := Capture(plan, "", c.limits)
			if got := pathsAndReasons(inv.Failures); !slices.Equal(got, c.want) || len(inv.Files) != c.files {
				t.Errorf("failures %v and %d files, want %v and %d", got, len(inv.Files), c.want, c.files)
			}
		})
	}
	if inv := Capture(filepath.Join(t.TempDir(), "missing"), "", Limits{}); pathsAndReasons(inv.Failures)[0] != " unreadable" {
		t.Errorf("a missing plan dir: failures %v, want unreadable", inv.Failures)
	}
}

// TestCapture_IsCompleteOnlyOnAStableListing covers spec 3.6's completeness
// rule: a change while reading is retried, sustained churn or a replay in
// progress yields an incomplete capture, never a quietly partial one.
// Failure prevented: a review read mid-sync shows an item resolved, or empty,
// as if the read were complete (H7).
func TestCapture_IsCompleteOnlyOnAStableListing(t *testing.T) { // not parallel: sets afterRead
	plan, gitDir := t.TempDir(), t.TempDir()
	write(t, filepath.Join(plan, "feedback", "round-1.json"), `{}`)

	changes := 0
	afterRead = func() {
		if changes < 1 {
			changes++
			write(t, filepath.Join(plan, "feedback", "round-2.json"), `{}`)
		}
	}
	t.Cleanup(func() { afterRead = func() {} })
	if inv := Capture(plan, gitDir, Limits{}); len(inv.Failures) != 0 || len(inv.Files) != 2 {
		t.Errorf("one change: failures %v, %d files; want the next attempt to read both", inv.Failures, len(inv.Files))
	}

	n := 2
	afterRead = func() {
		n++
		write(t, filepath.Join(plan, "feedback", "round-"+strings.Repeat("x", n)+".json"), `{}`)
	}
	if inv := Capture(plan, "", Limits{}); !slices.Contains(pathsAndReasons(inv.Failures), " changed") {
		t.Errorf("sustained churn: failures %v, want changed", inv.Failures)
	}
	afterRead = func() {}

	if err := os.Mkdir(filepath.Join(gitDir, "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if inv := Capture(plan, gitDir, Limits{}); !slices.Contains(pathsAndReasons(inv.Failures), " replay_in_progress") {
		t.Errorf("mid-rebase: failures %v, want replay_in_progress", inv.Failures)
	}
	if inv := Capture(plan, filepath.Join(plan, "feedback", "round-1.json"), Limits{}); !slices.Contains(pathsAndReasons(inv.Failures), " unreadable") {
		t.Errorf("a git dir that cannot be checked: failures %v, want unreadable", inv.Failures)
	}

	// feedback/ appears as a symlink after a read of a plan without one: the
	// second listing fails, and the capture must not settle as empty
	bare, outside := t.TempDir(), t.TempDir()
	afterRead = func() {
		afterRead = func() {}
		symlink(t, outside, filepath.Join(bare, "feedback"))
	}
	if inv := Capture(bare, "", Limits{}); !slices.Contains(pathsAndReasons(inv.Failures), "feedback not_regular") {
		t.Errorf("a failed second listing: failures %v, want feedback not_regular", inv.Failures)
	}
	afterRead = func() {}
}

// TestReadEntry_NeverWaitsOnAFIFO: a FIFO swapped in for a listed record is
// refused at once. Failure prevented: a capture blocks until something writes
// to the pipe.
func TestReadEntry_NeverWaitsOnAFIFO(t *testing.T) {
	t.Parallel()
	plan := t.TempDir()
	rec := filepath.Join(plan, "feedback", "round-1.json")
	write(t, rec, `{}`)
	root, err := os.OpenRoot(plan)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	var fails []Failure
	listed, dirs := list(root, DefaultLimits, &fails)
	defer func() {
		for _, d := range dirs {
			_ = d.Close()
		}
	}()
	if err := os.Remove(rec); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("mkfifo", rec).Run(); err != nil {
		t.Skipf("cannot make a FIFO here: %v", err)
	}
	done := make(chan *Failure, 1)
	go func() { _, f := readEntry(dirs["feedback"], listed[0]); done <- f }()
	select {
	case f := <-done:
		if f == nil || f.Reason != ReasonChanged {
			t.Errorf("readEntry = %+v, want changed", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO swapped in for a record blocked")
	}
}
