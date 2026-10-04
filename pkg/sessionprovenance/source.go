// Package sessionprovenance defines the versioned, content-free native session
// identity contract shared by capture, import, and Ledger deletion.
package sessionprovenance

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Range struct {
	Extra map[string]json.RawMessage `json:"-"`
	Start int64                      `json:"start"`
	End   int64                      `json:"end"`
}
type Source struct {
	Extra           map[string]json.RawMessage `json:"-"`
	Version         int                        `json:"version"`
	Agent           string                     `json:"agent"`
	NativeSessionID string                     `json:"native_session_id"`
	Generation      string                     `json:"generation"`
	SnapshotDigest  string                     `json:"snapshot_digest,omitempty"`
	Ranges          []Range                    `json:"ranges"`
	ParserVersion   string                     `json:"parser_version"`
	ParentSessionID string                     `json:"parent_session_id,omitempty"`
	CapturedAt      time.Time                  `json:"captured_at"`
	ImportedAt      *time.Time                 `json:"imported_at,omitempty"`
}
type Coverage struct {
	Extra       map[string]json.RawMessage `json:"-"`
	Start       int64                      `json:"start"`
	End         int64                      `json:"end"`
	SessionName string                     `json:"session_name"`
	RawOID      string                     `json:"raw_oid"`
}
type Exclusion struct {
	Extra     map[string]json.RawMessage `json:"-"`
	Start     int64                      `json:"start"`
	End       int64                      `json:"end"`
	Reason    string                     `json:"reason"`
	CreatedAt time.Time                  `json:"created_at"`
}
type Record struct {
	Version            int                        `json:"version"`
	Agent              string                     `json:"agent"`
	NativeSessionID    string                     `json:"native_session_id"`
	Generation         string                     `json:"generation"`
	Coverage           []Coverage                 `json:"coverage"`
	Exclusions         []Exclusion                `json:"exclusions"`
	Projections        map[string]Projection      `json:"projections,omitempty"`
	ProjectionRevision string                     `json:"projection_revision,omitempty"`
	UpdatedAt          time.Time                  `json:"updated_at"`
	Extra              map[string]json.RawMessage `json:"-"`
}

// Preserve fields added by newer servers: losing a future exclusion field is
// worse than refusing an import, so writers also validate the version.
func (r *Record) UnmarshalJSON(data []byte) error {
	type plain Record
	var decoded plain
	extra, err := decodeReceiptObject(data, &decoded, "version", "agent", "native_session_id", "generation", "coverage", "exclusions", "projections", "projection_revision", "updated_at")
	if err != nil {
		return err
	}
	*r = Record(decoded)
	r.Extra = extra
	return nil
}
func (r Record) MarshalJSON() ([]byte, error) {
	type plain Record
	return encodeReceiptObject(plain(r), r.Extra)
}

var nativeSessionIDPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func Path(nativeID string) (string, error) { return SessionSourcePath("codex", nativeID) }

// SessionSourcePath is the Ledger location of one native session's receipt.
// The agent is a directory element, so it is confined to a single flat path
// segment; the native ID is pinned to canonical UUID form. Those are the only
// two inputs that reach the path, which is what keeps a receipt from escaping
// data/session-sources/.
func SessionSourcePath(agent, nativeID string) (string, error) {
	if !ValidAgent(agent) {
		return "", fmt.Errorf("invalid source agent")
	}
	if !nativeSessionIDPattern.MatchString(nativeID) {
		return "", fmt.Errorf("invalid native session identity")
	}
	return "data/session-sources/" + agent + "/" + nativeID + ".json", nil
}

// validPathSegment reports whether s is safe as exactly one path element.
func validPathSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}
func ValidSessionName(name string) bool { return validPathSegment(name) }

// ValidAgent reports whether agent may name a receipt namespace. The contract
// checks shape only; which agents a consumer will process is that consumer's
// policy, so supporting a new agent needs no contract change.
func ValidAgent(agent string) bool { return validPathSegment(agent) }

func (r *Record) Validate() error {
	if r == nil || r.Version != 1 || !ValidAgent(r.Agent) {
		return fmt.Errorf("unsupported source record")
	}
	if _, err := SessionSourcePath(r.Agent, r.NativeSessionID); err != nil {
		return err
	}
	// A blank record precedes its first mutation (Exclude validates this prior
	// state before it sets UpdatedAt), so UpdatedAt is only required once the
	// record actually carries coverage, exclusions, or projections.
	hasContent := len(r.Coverage) > 0 || len(r.Exclusions) > 0 || len(r.Projections) > 0 || r.ProjectionRevision != ""
	if hasContent && r.UpdatedAt.IsZero() {
		return fmt.Errorf("invalid source updated_at")
	}
	// Privacy intent can precede capture, so a record without coverage or
	// projections may omit generation.
	hasGenerationBoundData := len(r.Coverage) > 0 || len(r.Projections) > 0 || r.ProjectionRevision != ""
	if (r.Generation != "" || hasGenerationBoundData) && !digestPattern.MatchString(r.Generation) {
		return fmt.Errorf("invalid source generation")
	}
	for _, c := range r.Coverage {
		if c.Start < 0 || c.End <= c.Start || !ValidSessionName(c.SessionName) || !digestPattern.MatchString(c.RawOID) {
			return fmt.Errorf("invalid source coverage")
		}
	}
	for _, e := range r.Exclusions {
		if e.CreatedAt.IsZero() || e.Start < 0 || (e.End != -1 && e.End <= e.Start) || e.Reason == "" {
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

// Projection is the backend processing receipt for one exported session.
type Projection struct {
	Extra          map[string]json.RawMessage `json:"-"`
	RawOID         string                     `json:"raw_oid"`
	LayerID        string                     `json:"layer_id"`
	SummaryOID     string                     `json:"summary_oid,omitempty"`
	SummaryLayerID string                     `json:"summary_layer_id,omitempty"`
}

// Backend names are aliases so all consumers use the same wire contract.
type SessionSource = Source
type SessionSourceRange = Range
type SessionSourceCoverage = Coverage
type SessionSourceExclusion = Exclusion
type SessionSourceRecord = Record
type SessionSourceProjection = Projection

// SessionProjectionInput contains only authenticated destination identifiers;
// the activity reads payload and provenance from the Ledger itself.
type SessionProjectionInput struct {
	RepoID        string `json:"repo_id"`
	GitLabProject string `json:"gitlab_project"`
	SessionName   string `json:"session_name"`
}

func (s *Source) Validate() error {
	if err := s.validateIdentityRanges(); err != nil {
		return err
	}
	if strings.TrimSpace(s.ParserVersion) == "" {
		return errors.New("missing source parser version")
	}
	if s.CapturedAt.IsZero() {
		return fmt.Errorf("missing source capture time")
	}
	if s.SnapshotDigest != "" && !digestPattern.MatchString(s.SnapshotDigest) {
		return fmt.Errorf("invalid source snapshot digest")
	}
	if s.ImportedAt != nil && s.ImportedAt.IsZero() {
		return fmt.Errorf("invalid source import time")
	}
	return nil
}

// Receipt scans and exclusions only need byte identity. Projection and
// readiness additionally require complete provenance through Validate.
func (s *Source) validateIdentityRanges() error {
	if s == nil || s.Version != 1 || !digestPattern.MatchString(s.Generation) || len(s.Ranges) == 0 {
		return errors.New("invalid session source version, generation or ranges")
	}
	if _, err := SessionSourcePath(s.Agent, s.NativeSessionID); err != nil {
		return err
	}
	for _, r := range s.Ranges {
		if r.Start < 0 || r.End <= r.Start {
			return errors.New("invalid native source range")
		}
	}
	return nil
}

func (r *Record) ValidateSource(s *Source) error {
	if err := s.validateIdentityRanges(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	// An exclusion-only receipt has no generation to match: privacy intent can
	// precede capture, so only a record with coverage must match the source.
	exclusionOnly := r.Generation == "" && len(r.Coverage) == 0
	generationMatches := exclusionOnly || r.Generation == s.Generation
	if r.Agent != s.Agent || r.NativeSessionID != s.NativeSessionID || !generationMatches {
		return errors.New("source record does not match session provenance")
	}
	return nil
}

func (r *Record) Excluded(s *Source) bool {
	if r == nil || s == nil {
		return false
	}
	for _, span := range s.Ranges {
		for _, x := range r.Exclusions {
			if span.End > x.Start && (x.End == -1 || span.Start < x.End) {
				return true
			}
		}
	}
	return false
}

func (r *Record) Covers(s *Source, name, oid string) bool {
	if r == nil || s == nil || len(s.Ranges) == 0 {
		return false
	}
	for _, span := range s.Ranges {
		found := false
		for _, c := range r.Coverage {
			if c.SessionName == name && c.RawOID == oid && c.Start <= span.Start && c.End >= span.End {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (r *Record) Exclude(s *Source, reason string, now time.Time) error {
	if reason == "" {
		return errors.New("missing exclusion reason")
	}
	if now.IsZero() {
		return errors.New("missing exclusion timestamp")
	}
	if err := r.ValidateSource(s); err != nil {
		return err
	}
	for _, span := range s.Ranges {
		r.Exclusions = append(r.Exclusions, Exclusion{Start: span.Start, End: span.End, Reason: reason, CreatedAt: now.UTC()})
	}
	r.UpdatedAt = now.UTC()
	return nil
}

// CheckCoverage is the shared eligibility rule for readers and writers. Exclusion
// wins over missing coverage: deleted history must never become importable again.
func (r *Record) CheckCoverage(s *Source, name, oid string) (excluded bool, err error) {
	if err := s.Validate(); err != nil {
		return false, err
	}
	if err := r.ValidateSource(s); err != nil {
		return false, err
	}
	if r.Excluded(s) {
		return true, nil
	}
	if !r.Covers(s, name, oid) {
		return false, errors.New("source record does not cover session export")
	}
	return false, nil
}

// Nested receipt fields evolve independently. Retain unknown keys during updates,
// while removing all known keys so clearing an optional field stays cleared.
func decodeReceiptObject(data []byte, value any, keys ...string) (map[string]json.RawMessage, error) {
	if err := json.Unmarshal(data, value); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for _, key := range keys {
		delete(fields, key)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return fields, nil
}
func encodeReceiptObject(value any, extra map[string]json.RawMessage) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key, value := range extra {
		if _, known := fields[key]; !known {
			fields[key] = value
		}
	}
	return json.Marshal(fields)
}

func (r *Coverage) UnmarshalJSON(data []byte) error {
	type plain Coverage
	var decoded plain
	extra, err := decodeReceiptObject(data, &decoded, "start", "end", "session_name", "raw_oid")
	if err != nil {
		return err
	}
	*r = Coverage(decoded)
	r.Extra = extra
	return nil
}
func (r Coverage) MarshalJSON() ([]byte, error) {
	type plain Coverage
	return encodeReceiptObject(plain(r), r.Extra)
}

func (r *Exclusion) UnmarshalJSON(data []byte) error {
	type plain Exclusion
	var decoded plain
	extra, err := decodeReceiptObject(data, &decoded, "start", "end", "reason", "created_at")
	if err != nil {
		return err
	}
	*r = Exclusion(decoded)
	r.Extra = extra
	return nil
}
func (r Exclusion) MarshalJSON() ([]byte, error) {
	type plain Exclusion
	return encodeReceiptObject(plain(r), r.Extra)
}

func (r *Projection) UnmarshalJSON(data []byte) error {
	type plain Projection
	var decoded plain
	extra, err := decodeReceiptObject(data, &decoded, "raw_oid", "layer_id", "summary_oid", "summary_layer_id")
	if err != nil {
		return err
	}
	*r = Projection(decoded)
	r.Extra = extra
	return nil
}
func (r Projection) MarshalJSON() ([]byte, error) {
	type plain Projection
	return encodeReceiptObject(plain(r), r.Extra)
}

func (r *Range) UnmarshalJSON(data []byte) error {
	type plain Range
	var decoded plain
	extra, err := decodeReceiptObject(data, &decoded, "start", "end")
	if err != nil {
		return err
	}
	*r = Range(decoded)
	r.Extra = extra
	return nil
}
func (r Range) MarshalJSON() ([]byte, error) {
	type plain Range
	return encodeReceiptObject(plain(r), r.Extra)
}

func (s *Source) UnmarshalJSON(data []byte) error {
	type plain Source
	var decoded plain
	extra, err := decodeReceiptObject(data, &decoded, "version", "agent", "native_session_id", "snapshot_digest", "generation", "ranges", "parser_version", "parent_session_id", "captured_at", "imported_at")
	if err != nil {
		return err
	}
	*s = Source(decoded)
	s.Extra = extra
	return nil
}
func (s Source) MarshalJSON() ([]byte, error) {
	type plain Source
	return encodeReceiptObject(plain(s), s.Extra)
}
