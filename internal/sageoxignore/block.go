package sageoxignore

import (
	"fmt"
	"os"
	"strings"
)

// Managed-block markers. ox owns everything BETWEEN them and nothing outside.
//
// A marked block rather than repeated line appends is the whole design, and it
// exists because of a specific failure: HasEntry deliberately skips comment lines
// so a documentary "# kb/" is never mistaken for a live rule. Feeding a header
// comment to EnsureEntry therefore reports "not present" on every single call, and
// appends it again — at prime, at doctor, and on the daemon's 30-minute tick, into
// a COMMITTED file. The ignore file itself would then become exactly the kind of
// churn this whole mechanism exists to eliminate.
//
// With markers, idempotency is structural: we find the block, compare its body,
// and rewrite only on a real change.
const (
	BlockBegin = "# >>> ox-managed — installed locally by ox, do not edit >>>"
	BlockEnd   = "# <<< ox-managed <<<"
)

// EnsureBlock makes the ox-managed block in the .gitignore at path contain
// exactly entries, creating the file if needed.
//
// Returns changed=false when the block is already byte-identical, which is the
// overwhelmingly common case and the reason this is safe to call on a hot path.
//
// Guarantees, in order of how badly their absence would hurt:
//   - Nothing outside the markers is read, reordered, rewritten, or removed. The
//     user's own rules — and their ordering, which is semantically significant in
//     gitignore — are untouched.
//   - Calling it repeatedly with the same entries is a no-op, so a committed file
//     never grows.
//   - A file that lacked a trailing newline gets one before the block is appended,
//     rather than having its last rule silently merged with our first line.
//
// blockFile is the read/write pair the block writer operates through.
//
// It exists so the writer can be anchored to a trusted *os.Root instead of
// resolving a path through the ambient filesystem on every call. Between an
// Lstat check and a path-based write there is a window in which the directory or
// the .gitignore can be replaced by a symlink, redirecting ox's write outside the
// repository. Root-relative operations close that window: the kernel resolves
// each component against the held root directory.
type blockFile struct {
	name  string // for error messages only
	read  func() ([]byte, error)
	write func([]byte) error
}

// EnsureBlockInRoot is EnsureBlock anchored to a trusted repository root.
// name is relative to root; every read and write stays inside it.
func EnsureBlockInRoot(root *os.Root, name string, entries []string) (changed bool, created bool, err error) {
	return ensureBlock(blockFile{
		name:  name,
		read:  func() ([]byte, error) { return root.ReadFile(name) },
		write: func(b []byte) error { return root.WriteFile(name, b, 0o644) },
	}, entries)
}

func EnsureBlock(path string, entries []string) (changed bool, created bool, err error) {
	return ensureBlock(blockFile{
		name:  path,
		read:  func() ([]byte, error) { return os.ReadFile(path) },
		write: func(b []byte) error { return os.WriteFile(path, b, 0o644) },
	}, entries)
}

func ensureBlock(f blockFile, entries []string) (changed bool, created bool, err error) {
	path := f.name
	existing, err := f.read()
	if err != nil {
		if !os.IsNotExist(err) {
			return false, false, fmt.Errorf("read %s: %w", path, err)
		}
		existing = nil
		created = true
	}

	want := renderBlock(entries)
	content := string(existing)

	if begin, end, ok := findBlock(content); ok {
		if content[begin:end] == want {
			return false, false, nil
		}
		updated := content[:begin] + want + content[end:]
		if err := f.write([]byte(updated)); err != nil {
			return false, false, fmt.Errorf("write %s: %w", path, err)
		}
		return true, false, nil
	}
	// No well-formed block. A begin marker with no end marker is a truncated or
	// hand-mangled block: do NOT guess where it ended and splice, because a wrong
	// guess silently eats the user's rules. Appending a fresh, complete block is
	// recoverable and leaves the damaged text visible.

	var buf strings.Builder
	buf.WriteString(content)
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		buf.WriteString("\n")
	}
	if len(content) > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString(want)
	if err := f.write([]byte(buf.String())); err != nil {
		return false, false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, created, nil
}

// RemoveBlock deletes the ox-managed block, leaving everything else byte-identical.
// Used by uninstall. A missing file or missing block is not an error.
func RemoveBlock(path string) (removed bool, err error) {
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	content := string(existing)
	begin := strings.Index(content, BlockBegin)
	if begin < 0 {
		return false, nil
	}
	endIdx := strings.Index(content[begin:], BlockEnd)
	if endIdx < 0 {
		return false, nil
	}
	end := begin + endIdx + len(BlockEnd)
	if end < len(content) && content[end] == '\n' {
		end++
	}
	// Also absorb the single blank separator line we inserted before the block.
	if begin >= 2 && content[begin-1] == '\n' && content[begin-2] == '\n' {
		begin--
	}
	updated := content[:begin] + content[end:]
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

func renderBlock(entries []string) string {
	var b strings.Builder
	b.WriteString(BlockBegin)
	b.WriteString("\n")
	for _, e := range entries {
		b.WriteString(e)
		b.WriteString("\n")
	}
	b.WriteString(BlockEnd)
	b.WriteString("\n")
	return b.String()
}

// findBlock locates a WELL-FORMED managed block: a begin marker and the first end
// marker after it, with no second begin marker in between.
//
// The "no second begin" rule is what makes a damaged file safe. A file containing
// an orphaned begin marker gets a fresh complete block appended after it; on the
// NEXT run, a naive "first begin, first end" search would pair the ORPHAN with
// the new block's end marker and replace everything between them — silently
// deleting every user rule that sat after the damaged marker. Skipping to the
// last begin marker before the end marker keeps that region untouched.
//
// end is the index just past the end marker's terminating newline, so replacing
// the block cannot accumulate blank lines across runs.
func findBlock(content string) (begin, end int, ok bool) {
	search := 0
	for {
		b := strings.Index(content[search:], BlockBegin)
		if b < 0 {
			return 0, 0, false
		}
		b += search
		e := strings.Index(content[b:], BlockEnd)
		if e < 0 {
			return 0, 0, false // orphaned begin marker: no well-formed block
		}
		e += b
		// A second begin marker before this end marker means b is the orphan.
		if next := strings.Index(content[b+len(BlockBegin):e], BlockBegin); next >= 0 {
			search = b + len(BlockBegin) + next
			continue
		}
		end = e + len(BlockEnd)
		if end < len(content) && content[end] == '\n' {
			end++
		}
		return b, end, true
	}
}
