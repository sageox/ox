// Package sessionprovenance defines the versioned, content-free native session
// identity contract shared by capture, import, and Ledger deletion.
package sessionprovenance

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Range struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}
type Source struct {
	Version         int        `json:"version"`
	Agent           string     `json:"agent"`
	NativeSessionID string     `json:"native_session_id"`
	Generation      string     `json:"generation"`
	SnapshotDigest  string     `json:"snapshot_digest,omitempty"`
	Ranges          []Range    `json:"ranges"`
	ParserVersion   string     `json:"parser_version"`
	ParentSessionID string     `json:"parent_session_id,omitempty"`
	CapturedAt      time.Time  `json:"captured_at"`
	ImportedAt      *time.Time `json:"imported_at,omitempty"`
}
type Coverage struct {
	Start       int64  `json:"start"`
	End         int64  `json:"end"`
	SessionName string `json:"session_name"`
	RawOID      string `json:"raw_oid"`
}
type Exclusion struct {
	Start     int64     `json:"start"`
	End       int64     `json:"end"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}
type Record struct {
	Version            int                        `json:"version"`
	Agent              string                     `json:"agent"`
	NativeSessionID    string                     `json:"native_session_id"`
	Generation         string                     `json:"generation"`
	Coverage           []Coverage                 `json:"coverage"`
	Exclusions         []Exclusion                `json:"exclusions"`
	ProjectionRevision string                     `json:"projection_revision,omitempty"`
	UpdatedAt          time.Time                  `json:"updated_at"`
	Extra              map[string]json.RawMessage `json:"-"`
}

// Preserve fields added by newer servers: losing a future exclusion field is
// worse than refusing an import, so writers also validate the version.
func (r *Record) UnmarshalJSON(b []byte) error {
	type wire Record
	var w wire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*r = Record(w)
	if err := json.Unmarshal(b, &r.Extra); err != nil {
		return err
	}
	for _, k := range []string{"version", "agent", "native_session_id", "generation", "coverage", "exclusions", "projection_revision", "updated_at"} {
		delete(r.Extra, k)
	}
	return nil
}
func (r Record) MarshalJSON() ([]byte, error) {
	type wire Record
	b, err := json.Marshal(wire(r))
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err = json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	return json.Marshal(m)
}
func Path(nativeID string) (string, error) {
	id, err := uuid.Parse(nativeID)
	if err != nil || id.String() != nativeID {
		return "", fmt.Errorf("invalid Codex session identity")
	}
	return path.Join("data/session-sources/codex", nativeID+".json"), nil
}
func ValidSessionName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}
func (r *Record) Validate() error {
	if r.Version != 1 || r.Agent != "codex" {
		return fmt.Errorf("unsupported source record")
	}
	if _, err := Path(r.NativeSessionID); err != nil {
		return err
	}
	for _, c := range r.Coverage {
		if c.Start < 0 || c.End <= c.Start || !ValidSessionName(c.SessionName) || len(c.RawOID) != 64 {
			return fmt.Errorf("invalid source coverage")
		}
	}
	for _, e := range r.Exclusions {
		if e.Start < 0 || (e.End != -1 && e.End <= e.Start) || e.Reason == "" {
			return fmt.Errorf("invalid source exclusion")
		}
	}
	return nil
}
func (r *Record) Excludes(start, end int64) bool {
	for _, e := range r.Exclusions {
		if (e.End == -1 || e.End > start) && e.Start < end {
			return true
		}
	}
	return false
}
