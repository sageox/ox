package planreview

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

var (
	errNotDir  = errors.New("not a directory")
	errSwapped = errors.New("changed while it was opened")
)

// beforeOpen runs between checking a directory and opening it; tests use it to
// swap the directory for a symlink in that window.
var beforeOpen = func(string) {}

// openSub opens directory name inside r without following a symlink: it must
// be a directory by Lstat, and both the directory opened and a second Lstat
// must be that same directory, so a symlink swapped in at any point fails
// instead of being followed. (os.Root alone follows a symlink that stays
// inside the root.) With create, a missing directory is made first.
func openSub(r *os.Root, name string, create bool) (*os.Root, error) {
	fi, err := r.Lstat(name)
	if create && errors.Is(err, fs.ErrNotExist) {
		if err = r.Mkdir(name, 0o755); err == nil || errors.Is(err, fs.ErrExist) {
			fi, err = r.Lstat(name)
		}
	}
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s: %w", name, errNotDir)
	}
	beforeOpen(name)
	sub, err := r.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	got, err := sub.Stat(".")
	again, err2 := r.Lstat(name)
	if err != nil || err2 != nil || !os.SameFile(fi, got) || !os.SameFile(fi, again) {
		_ = sub.Close()
		return nil, fmt.Errorf("%s: %w", name, errSwapped)
	}
	return sub, nil
}

// Publish writes rec into the worktree at planDir, confined and create-only:
// each directory below planDir down to the record's is opened without
// following a symlink (missing ones are made; planDir itself is trusted), the bytes go to a dot-prefixed
// temporary that readers skip, and the temporary is hard-linked to the record
// name, which fails rather than replace any existing name, a dangling symlink
// included. When the name exists, the receipt is ReceiptAlreadyPublished for
// equal bytes and ReceiptConflict otherwise; nothing is replaced. Publish
// does not look the operation up; Prior does.
func Publish(planDir string, rec File) (Receipt, error) {
	k, want := classify(rec.Path)
	if k != roundFile && k != resolutionFile && k != remapFile || want == "" || want != digest(rec.Data) {
		return Receipt{}, fmt.Errorf("publish %q: not the immutable record name of these bytes", rec.Path)
	}
	if len(rec.Data) > MaxRecordBytes {
		return Receipt{}, fmt.Errorf("publish %q: %d bytes, over the %d-byte limit", rec.Path, len(rec.Data), MaxRecordBytes)
	}
	root, err := os.OpenRoot(planDir)
	if err != nil {
		return Receipt{}, fmt.Errorf("publish %q: %w", rec.Path, err)
	}
	defer func() { _ = root.Close() }()
	dir := root
	for _, name := range strings.Split(path.Dir(rec.Path), "/") {
		sub, err := openSub(dir, name, true)
		if dir != root {
			_ = dir.Close()
		}
		if err != nil {
			return Receipt{}, fmt.Errorf("publish %q: %w", rec.Path, err)
		}
		dir = sub
	}
	defer func() { _ = dir.Close() }()

	name := path.Base(rec.Path)
	tag := make([]byte, 8)
	_, _ = rand.Read(tag) // crypto/rand.Read does not fail
	tmp := "." + name + "." + hex.EncodeToString(tag) + ".tmp"
	f, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return Receipt{}, fmt.Errorf("publish %q: %w", rec.Path, err)
	}
	_, werr := f.Write(rec.Data)
	serr := f.Sync()
	if err := errors.Join(werr, serr, f.Close()); err != nil {
		_ = dir.Remove(tmp)
		return Receipt{}, fmt.Errorf("publish %q: %w", rec.Path, err)
	}
	lerr := dir.Link(tmp, name)
	_ = dir.Remove(tmp)
	switch {
	case lerr == nil:
		if err := syncDir(dir); err != nil {
			return Receipt{}, fmt.Errorf("publish %q: sync directory: %w", rec.Path, err)
		}
		return Receipt{State: ReceiptCreated, Path: rec.Path}, nil
	case !errors.Is(lerr, fs.ErrExist):
		return Receipt{}, fmt.Errorf("publish %q: %w", rec.Path, lerr)
	}
	equal, err := holds(dir, name, rec.Data)
	if err != nil {
		return Receipt{}, fmt.Errorf("publish %q: %w", rec.Path, err)
	}
	if equal {
		return Receipt{State: ReceiptAlreadyPublished, Path: rec.Path}, nil
	}
	return Receipt{State: ReceiptConflict, Path: rec.Path}, nil
}

// holds reports whether name in dir is a regular file holding exactly data.
func holds(dir *os.Root, name string, data []byte) (bool, error) {
	fi, err := dir.Lstat(name)
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() || fi.Size() != int64(len(data)) {
		return false, nil
	}
	f, err := dir.Open(name)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(io.LimitReader(f, int64(len(data))+1))
	if err != nil {
		return false, err
	}
	return bytes.Equal(got, data), nil
}

// syncDir makes a new name durable. Windows cannot sync a directory handle;
// there the link is durable once NTFS commits its journal.
func syncDir(dir *os.Root) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := dir.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// recordDirs are the directories a capture descends into; any other directory
// under feedback/ is unrecognized.
var recordDirs = []string{"feedback", "feedback/remaps", "feedback/resolutions"}

// afterRead runs between reading a capture and listing it again; tests use it
// to change the subtree mid-capture.
var afterRead = func() {}

// Capture reads the review records of the worktree at planDir: every regular
// file under feedback/, byte-exact and in path order, bounded by limits, never
// through a symlink below planDir (a symlinked or otherwise non-regular entry
// is a failure, not followed; planDir itself is trusted). Records not yet committed or pushed are included.
//
// gitDir, when set, is the worktree's git directory: while a rebase or merge
// is in progress there the worktree lacks commits being replayed, so the
// capture is incomplete.
//
// A capture is complete when nothing failed, the listing after the read
// matches the listing before it (names, sizes, modification times, file
// identity), and no replay was in progress before or after. After three
// attempts the last one is returned, carrying a ReasonChanged or ReasonReplay
// failure.
func Capture(planDir, gitDir string, limits Limits) Inventory {
	var inv Inventory
	for range 3 {
		var settled bool
		if inv, settled = captureOnce(planDir, gitDir, limits.orDefaults()); settled {
			break
		}
	}
	return inv
}

type entry struct {
	path string
	info fs.FileInfo
}

func captureOnce(planDir, gitDir string, l Limits) (Inventory, bool) {
	var inv Inventory
	replay := replaying(gitDir)
	root, err := os.OpenRoot(planDir)
	if err != nil {
		return Inventory{Failures: []Failure{{Reason: ReasonUnreadable, Detail: err.Error()}}}, !replay
	}
	defer func() { _ = root.Close() }()

	before, dirs := list(root, &inv.Failures)
	defer func() {
		for _, d := range dirs {
			_ = d.Close()
		}
	}()
	b := budget{Limits: l}
	for _, e := range before {
		k, _ := classify(e.path)
		switch {
		case e.info.IsDir():
			if !slices.Contains(recordDirs, e.path) {
				inv.Failures = append(inv.Failures, Failure{Path: e.path, Reason: ReasonUnrecognized})
			}
		case !e.info.Mode().IsRegular():
			inv.Failures = append(inv.Failures, Failure{Path: e.path, Reason: ReasonNotRegular})
		case k == unrecognized:
			inv.Failures = append(inv.Failures, Failure{Path: e.path, Reason: ReasonUnrecognized})
		case b.admit(&inv.Failures, e.path, e.info.Size()):
			data, fail := readEntry(dirs[path.Dir(e.path)], e)
			if fail != nil {
				inv.Failures = append(inv.Failures, *fail)
				continue
			}
			inv.Files = append(inv.Files, File{Path: e.path, Data: data})
		}
	}
	afterRead()

	var ignored []Failure
	after, afterDirs := list(root, &ignored)
	for _, d := range afterDirs {
		_ = d.Close()
	}
	switch {
	case replay || replaying(gitDir):
		inv.Failures = append(inv.Failures, Failure{Reason: ReasonReplay, Detail: "a rebase or merge is in progress in " + gitDir})
		return inv, false
	case !sameListing(before, after):
		inv.Failures = append(inv.Failures, Failure{Reason: ReasonChanged, Detail: "the review files changed while they were read"})
		return inv, false
	}
	return inv, !slices.ContainsFunc(inv.Failures, func(f Failure) bool { return f.Reason == ReasonChanged })
}

// list walks feedback/ and the record directories under it without following
// a symlink, returning every entry but dot-prefixed temporaries, in path
// order, and the open record directories by path.
func list(root *os.Root, fails *[]Failure) ([]entry, map[string]*os.Root) {
	dirs := map[string]*os.Root{}
	var out []entry
	var walk func(r *os.Root, rel string)
	walk = func(r *os.Root, rel string) {
		dirs[rel] = r
		d, err := r.Open(".")
		if err != nil {
			*fails = append(*fails, Failure{Path: rel, Reason: ReasonUnreadable, Detail: err.Error()})
			return
		}
		names, err := d.Readdirnames(-1)
		_ = d.Close()
		if err != nil {
			*fails = append(*fails, Failure{Path: rel, Reason: ReasonUnreadable, Detail: err.Error()})
			return
		}
		for _, n := range names {
			if strings.HasPrefix(n, ".") {
				continue
			}
			p := rel + "/" + n
			fi, err := r.Lstat(n)
			if err != nil {
				*fails = append(*fails, Failure{Path: p, Reason: ReasonChanged, Detail: err.Error()})
				continue
			}
			out = append(out, entry{p, fi})
			if fi.IsDir() && slices.Contains(recordDirs, p) {
				if sub, err := openSub(r, n, false); err != nil {
					*fails = append(*fails, Failure{Path: p, Reason: ReasonChanged, Detail: err.Error()})
				} else {
					walk(sub, p)
				}
			}
		}
	}
	switch sub, err := openSub(root, "feedback", false); {
	case errors.Is(err, fs.ErrNotExist):
	case errors.Is(err, errNotDir):
		*fails = append(*fails, Failure{Path: "feedback", Reason: ReasonNotRegular, Detail: err.Error()})
	case err != nil:
		*fails = append(*fails, Failure{Path: "feedback", Reason: ReasonUnreadable, Detail: err.Error()})
	default:
		walk(sub, "feedback")
	}
	slices.SortFunc(out, func(a, b entry) int { return strings.Compare(a.path, b.path) })
	return out, dirs
}

// readEntry reads one listed file, refusing it when the file opened is not
// the one listed or its size changed since the listing (the size the budget
// admitted).
func readEntry(dir *os.Root, e entry) ([]byte, *Failure) {
	f, err := dir.Open(path.Base(e.path))
	if err != nil {
		return nil, &Failure{Path: e.path, Reason: ReasonChanged, Detail: err.Error()}
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err != nil || !os.SameFile(e.info, fi) {
		return nil, &Failure{Path: e.path, Reason: ReasonChanged, Detail: "replaced between listing and read"}
	}
	data, err := io.ReadAll(io.LimitReader(f, e.info.Size()+1))
	switch {
	case err != nil:
		return nil, &Failure{Path: e.path, Reason: ReasonUnreadable, Detail: err.Error()}
	case int64(len(data)) != e.info.Size():
		return nil, &Failure{Path: e.path, Reason: ReasonChanged, Detail: "size changed since the listing"}
	}
	return data, nil
}

func sameListing(a, b []entry) bool {
	return slices.EqualFunc(a, b, func(x, y entry) bool {
		return x.path == y.path && x.info.Size() == y.info.Size() &&
			x.info.ModTime().Equal(y.info.ModTime()) && os.SameFile(x.info, y.info)
	})
}

// replaying reports whether a rebase or merge is in progress in gitDir.
func replaying(gitDir string) bool {
	if gitDir == "" {
		return false
	}
	for _, n := range []string{"rebase-merge", "rebase-apply", "MERGE_HEAD"} {
		if _, err := os.Lstat(filepath.Join(gitDir, n)); err == nil {
			return true
		}
	}
	return false
}
