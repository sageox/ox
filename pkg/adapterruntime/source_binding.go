package adapterruntime

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"
)

// ErrUnboundSource means native discovery cannot establish source ownership.
var ErrUnboundSource = errors.New("native session source is not bound to the recording")

// ValidateSessionSource enforces the identity requirements for Gemini and Amp.
// An empty path checks discovery prerequisites; a discovered or persisted path
// must be checked again before use. Other adapters retain their own validators.
func ValidateSessionSource(adapter, path, nativeID string) error {
	switch adapter {
	case "gemini":
		// Gemini's projectHash is opaque: neither its bucket name, recency,
		// nor an Ox AgentID in conversation text proves repository ownership.
		// Until a trusted repo-to-hash mapping is available, even a matching
		// native UUID is insufficient to import this transcript.
		return fmt.Errorf("%w: Gemini repository ownership cannot be verified", ErrUnboundSource)
	case "amp":
		id := AmpThreadID(nativeID)
		if id == "" {
			return fmt.Errorf("%w: Amp requires a native thread ID", ErrUnboundSource)
		}
		if err := ValidateSessionID(id); err != nil {
			return fmt.Errorf("%w: %w", ErrUnboundSource, err)
		}
		if path != "" {
			if filepath.Base(path) != id+".jsonl" {
				return fmt.Errorf("%w: Amp sidecar does not match native thread ID", ErrUnboundSource)
			}
			resolved, err := filepath.EvalSymlinks(path)
			if errors.Is(err, fs.ErrNotExist) {
				// A removed sidecar yields no new entries; its name still binds
				// the turns captured from it before it disappeared.
				return nil
			}
			if err != nil {
				return fmt.Errorf("%w: resolve Amp sidecar: %w", ErrUnboundSource, err)
			}
			if filepath.Base(resolved) != id+".jsonl" {
				return fmt.Errorf("%w: Amp sidecar does not match native thread ID", ErrUnboundSource)
			}
		}
	}
	return nil
}

// AmpThreadID reduces AMP_THREAD_URL to the ID used to name its sidecar.
// Bare IDs pass through; callers must validate the result before using a path.
func AmpThreadID(id string) string {
	u, err := url.Parse(strings.TrimSpace(id))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return id
	}
	path := strings.TrimSuffix(u.Path, "/")
	i := strings.LastIndex(path, "/threads/")
	if i < 0 {
		return id
	}
	return path[i+len("/threads/"):]
}
