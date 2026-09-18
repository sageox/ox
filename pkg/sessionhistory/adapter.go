// Package sessionhistory defines the streaming boundary for local coding history.
// Adapters inspect identity cheaply, then validate the complete immutable snapshot
// while emitting normalized records; no adapter returns a whole transcript array.
package sessionhistory

import (
	"context"
	"time"

	"github.com/sageox/ox/pkg/adapterprotocol"
)

type Snapshot struct {
	NativeID     string    `json:"native_session_id"`
	CWD          string    `json:"-"`
	Path         string    `json:"-"`
	Generation   string    `json:"generation"`
	Digest       string    `json:"-"`
	Size         int64     `json:"size"`
	ModifiedAt   time.Time `json:"-"`
	StartedAt    time.Time `json:"started_at"`
	LastActivity time.Time `json:"last_activity"`
	ParentID     string    `json:"parent_session_id,omitempty"`
	Internal     bool      `json:"-"`
	InFlight     bool      `json:"-"`
	Entries      int       `json:"entry_count"`
}

// Adapter discovery and inspection are observational. Stream must reject changed
// or malformed sources even if earlier records were emitted to a temporary sink.
type Adapter interface {
	Name() string
	ParserVersion() string
	Home() (string, error)
	Discover(home string) ([]string, error)
	Inspect(path string) (Snapshot, error)
	Stream(context.Context, string, func(adapterprotocol.RawEntry) error) (Snapshot, error)
}
