package githubmirror

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/fileutil"
)

// StateVersion is the current on-disk state format.
const StateVersion = 1

// ItemState is what the daemon remembers about one relayed item.
type ItemState struct {
	UpdatedAt            time.Time `json:"updated_at"` // GitHub updated_at when last processed
	ChangeHash           string    `json:"change_hash"`
	LastMaterialChangeAt time.Time `json:"last_material_change_at"`
	Status               string    `json:"status"` // ResultAccepted, ResultCurrent, ResultRejected
	Reason               string    `json:"reason,omitempty"`
	RelayedAt            time.Time `json:"relayed_at"`
}

// State is the daemon's local, derived relay state for one repo. It lives in
// the Ledger's gitignored .sageox/cache/ and is never committed; losing it only
// costs repeat relays the server ignores.
type State struct {
	Version       int                  `json:"version"`
	Repo          string               `json:"repo"`   // owner/name
	Cursor        time.Time            `json:"cursor"` // list items updated at/after this
	ColdStartDone bool                 `json:"cold_start_done"`
	LastAttemptAt time.Time            `json:"last_attempt_at"`
	LastSuccessAt time.Time            `json:"last_success_at"`
	LastError     string               `json:"last_error,omitempty"`
	LastErrorAt   time.Time            `json:"last_error_at"`
	NextAllowedAt time.Time            `json:"next_allowed_at"` // backoff
	RepoStatus    string               `json:"repo_status,omitempty"`
	RepoMeta      *Repo                `json:"repo_meta,omitempty"`
	RepoMetaAt    time.Time            `json:"repo_meta_at"`
	Items         map[string]ItemState `json:"items"` // keyed by SourceKey
}

// StatePath is <ledger>/.sageox/cache/github_mirror/state.json.
func StatePath(ledgerPath string) string {
	return filepath.Join(ledgerPath, ".sageox", "cache", "github_mirror", "state.json")
}

// newState is the starting point for a repo with no usable history.
func newState() *State {
	return &State{Version: StateVersion, Items: map[string]ItemState{}}
}

// LoadState reads the state file. A missing file is an empty State, not an
// error; a corrupt file is reported so the caller can start fresh.
//
// The returned State is never nil and Items is never nil, including alongside a
// non-nil error: the caller logs the error and carries on with the fresh State.
// Losing state is cheap (the server ignores repeat relays), so a bad file must
// never wedge the mirror.
func LoadState(ledgerPath string) (*State, error) {
	if ledgerPath == "" {
		// an empty path would resolve against the working directory
		return newState(), errors.New("load github mirror state: empty ledger path")
	}

	path := StatePath(ledgerPath)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return newState(), nil
	}
	if err != nil {
		return newState(), fmt.Errorf("read github mirror state %s: %w", path, err)
	}

	// decode into a scratch value so a half-parsed file cannot leak partial
	// fields into the fresh state we hand back.
	var loaded State
	if err := json.Unmarshal(data, &loaded); err != nil {
		return newState(), fmt.Errorf("parse github mirror state %s: %w", path, err)
	}
	if loaded.Version > StateVersion {
		// written by a newer ox; saving over it would silently drop fields
		return newState(), fmt.Errorf("github mirror state %s has version %d, newer than supported %d", path, loaded.Version, StateVersion)
	}

	loaded.Version = StateVersion
	if loaded.Items == nil {
		loaded.Items = map[string]ItemState{}
	}
	return &loaded, nil
}

// SaveState writes the state file atomically (temp file + rename).
func SaveState(ledgerPath string, s *State) error {
	if ledgerPath == "" {
		return errors.New("save github mirror state: empty ledger path")
	}
	if s == nil {
		return errors.New("save github mirror state: nil state")
	}

	s.Version = StateVersion
	if s.Items == nil {
		s.Items = map[string]ItemState{}
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode github mirror state: %w", err)
	}
	data = append(data, '\n')

	path := StatePath(ledgerPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create github mirror state dir: %w", err)
	}
	if err := fileutil.AtomicWriteBytes(path, data, 0o600); err != nil {
		return fmt.Errorf("write github mirror state %s: %w", path, err)
	}
	return nil
}

// Prune drops items whose last material change is older than Window. Those
// posts have expired on the server and the daemon will never relay the item
// again, so remembering it only grows the file. An item with no recorded
// material change falls back to when it was relayed.
func (s *State) Prune(now time.Time) {
	for key, item := range s.Items {
		changedAt := item.LastMaterialChangeAt
		if changedAt.IsZero() {
			changedAt = item.RelayedAt
		}
		if changedAt.Add(Window).Before(now) {
			delete(s.Items, key)
		}
	}
}
