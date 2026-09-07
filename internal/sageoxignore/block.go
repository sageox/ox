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
func EnsureBlock(path string, entries []string) (changed bool, created bool, err error) {
	existing, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, false, fmt.Errorf("read %s: %w", path, err)
		}
		existing = nil
		created = true
	}

	want := renderBlock(entries)
	content := string(existing)

	begin := strings.Index(content, BlockBegin)
	if begin >= 0 {
		endIdx := strings.Index(content[begin:], BlockEnd)
		if endIdx >= 0 {
			end := begin + endIdx + len(BlockEnd)
			// Absorb the newline that terminates the end marker so replacing the
			// block cannot accumulate blank lines across runs.
			if end < len(content) && content[end] == '\n' {
				end++
			}
			if content[begin:end] == want {
				return false, false, nil
			}
			updated := content[:begin] + want + content[end:]
			if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
				return false, false, fmt.Errorf("write %s: %w", path, err)
			}
			return true, false, nil
		}
		// Begin marker with no end marker: a truncated or hand-mangled block. Do
		// NOT try to guess where it ended and splice — appending a fresh, complete
		// block is recoverable and leaves the damaged text visible to the user,
		// whereas a wrong guess silently eats their rules.
	}

	var buf strings.Builder
	buf.WriteString(content)
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		buf.WriteString("\n")
	}
	if len(content) > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString(want)
	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
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
