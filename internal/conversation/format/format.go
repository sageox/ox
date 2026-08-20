// Package format is the single, lenient implementation of "read a discussion
// folder" for the ox conversation command family. It absorbs every historical
// stratum of the on-disk discussion format — folder-form and flat-form layers,
// both conversation-manifest file names, pre-manifest legacy folders, legacy
// singular citation URIs — so the reader/query layer above it never sees the
// mess.
//
// Design contract (plan of record, decisions D4–D7, D10, D11, D13):
//   - Loaders are lenient: a missing file is nil, nil (absence is data, not an
//     error); empty titles are valid; a single malformed record is skipped and
//     surfaced in an invalid list, never fatal for its siblings.
//   - Layer discovery is a semantic port of the server's discovery rules:
//     recursive, both layouts, folder-wins dedup, path↔body id mismatch is
//     invalid, deterministic order.
//   - Distillation state is always projected: the base episode file is folded
//     with the edits / finalize / ttl_extends sidecars before anything is
//     served; atoms are bi-temporal and default to projected-current.
//
// The package owns no I/O policy beyond reading the paths it is handed, and
// never touches the network. Every read goes through an os.Root opened over
// the discussion root (openat-style, no-follow component resolution), so a
// symlink committed into the customer-writable tree can never pull content
// from outside the root: within-root links resolve, escaping links error.
package format

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// InvalidRecord surfaces one malformed or otherwise unusable record that a
// lenient loader skipped. Path is relative to the root the loader was given
// (or the loaded file itself); Line is the 1-based line or array index for
// line-oriented sources, 0 when not applicable.
type InvalidRecord struct {
	Path   string `json:"path"`
	Line   int    `json:"line,omitempty"`
	Reason string `json:"reason"`
}

func (r InvalidRecord) String() string {
	if r.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", r.Path, r.Line, r.Reason)
	}
	return fmt.Sprintf("%s: %s", r.Path, r.Reason)
}

// openOptionalRoot opens dir as an os.Root and returns (nil, nil) when the
// directory does not exist — a missing discussion root means every optional
// file inside it is absent, which is data, not an error. All confinement
// guarantees of this package hang off the returned root: every subsequent
// path component resolves no-follow relative to it.
func openOptionalRoot(dir string) (*os.Root, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	return root, nil
}

// readOptionalFileIn reads rel from root and returns (nil, nil) when the file
// does not exist. Every lenient loader in this package goes through it. A
// symlink escaping the root is an error (never followed), reported with the
// display path for context.
func readOptionalFileIn(root *os.Root, rel string) ([]byte, error) {
	data, err := root.ReadFile(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", filepath.Join(root.Name(), rel), err)
	}
	return data, nil
}

// decodeJSON unmarshals data into v, wrapping the error with the source path.
func decodeJSON(path string, data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
