// Package teamconverge coordinates delivery of typed Team Context artifacts
// into one product repository.
package teamconverge

import (
	"context"

	"github.com/sageox/ox/internal/teamdocs"
)

const ReportSchemaVersion = 1

type ArtifactKind string

const (
	KindSkill   ArtifactKind = "skill"
	KindRule    ArtifactKind = "rule"
	KindContext ArtifactKind = "context"
)

type OutcomeState string

const (
	StateApplied         OutcomeState = "applied"
	StateIndexed         OutcomeState = "indexed"
	StatePending         OutcomeState = "pending"
	StatePendingApproval OutcomeState = "pending_approval"
	StateUnsupported     OutcomeState = "unsupported"
	StateFiltered        OutcomeState = "filtered"
	StateConflict        OutcomeState = "conflict"
	StateError           OutcomeState = "error"
)

type OriginKind string

const (
	OriginLoose OriginKind = "loose"
)

// Origin identifies who owns the canonical Team Context artifact. Every
// artifact discovered today is loose (hand-authored); Pack/PackVersion/Digest
// stay part of the shape so a future Pack Catalog producer does not need a
// schema bump.
type Origin struct {
	Kind        OriginKind `json:"kind"`
	Pack        string     `json:"pack,omitempty"`
	PackVersion string     `json:"pack_version,omitempty"`
	Digest      string     `json:"digest,omitempty"`
}

// Artifact is one normalized item discovered from a Team Context snapshot.
type Artifact struct {
	Kind         ArtifactKind `json:"kind"`
	Name         string       `json:"name"`
	SourcePath   string       `json:"source_path"`
	Origin       Origin       `json:"origin"`
	Applicable   bool         `json:"applicable"`
	Required     bool         `json:"required"`
	Visibility   string       `json:"visibility,omitempty"`
	Description  string       `json:"description,omitempty"`
	Globs        []string     `json:"globs,omitempty"`
	FilterReason string       `json:"filter_reason,omitempty"`

	// rule carries the already-parsed Team Rule for KindRule artifacts, so
	// convergeRules does not re-read and re-join teamdocs.PublishedRules
	// against what discovery already parsed under the same snapshot lease.
	// Unexported: never part of the Artifact JSON shape.
	rule *teamdocs.TeamRule
}

// Snapshot pins every outcome in a report to one Team Context commit.
type Snapshot struct {
	Path   string `json:"path"`
	Commit string `json:"commit"`
}

type Mode string

const (
	ModeAutomatic Mode = "automatic"
	ModeExplicit  Mode = "explicit"
)

type Request struct {
	ProjectRoot string
	TeamPath    string
	RepoSlug    string
	TeamCommit  string
	Mode        Mode
}

// Outcome explains both the state and delivery mechanism for one artifact.
// SourceCommit makes a mixed-snapshot report detectable by callers.
type Outcome struct {
	Kind         ArtifactKind `json:"kind"`
	Name         string       `json:"name"`
	SourcePath   string       `json:"source_path"`
	SourceCommit string       `json:"source_commit"`
	Origin       Origin       `json:"origin"`
	State        OutcomeState `json:"state"`
	Delivery     string       `json:"delivery,omitempty"`
	InstalledAs  string       `json:"installed_as,omitempty"`
	Detail       string       `json:"detail,omitempty"`
	Required     bool         `json:"required"`
}

type Report struct {
	SchemaVersion int       `json:"schema_version"`
	ProjectRoot   string    `json:"project_root"`
	RepoSlug      string    `json:"repo_slug"`
	Snapshot      Snapshot  `json:"snapshot"`
	Outcomes      []Outcome `json:"outcomes"`
}

// Converged is false for every state that needs action or retry. A filtered
// artifact is intentionally absent here, and indexed content is a valid
// delivery mode rather than a lesser success.
func (r Report) Converged() bool {
	for _, outcome := range r.Outcomes {
		switch outcome.State {
		case StateApplied, StateIndexed, StateFiltered:
			continue
		case StateUnsupported:
			if !outcome.Required {
				continue
			}
			return false
		default:
			return false
		}
	}
	return true
}

// Discovery finds this repository's applicable Team Context artifacts.
// FilesystemDiscovery is the only production implementation; the interface
// exists so package tests can substitute fixed artifacts without a real git
// Team Context checkout.
type Discovery interface {
	Discover(context.Context, Request) (Snapshot, []Artifact, error)
}

// settledError marks a handler failure whose recovery requires a source or
// local configuration change. Unmarked errors remain retryable by default.
type settledError struct {
	State OutcomeState
	Err   error
}

func (e *settledError) Error() string { return e.Err.Error() }
func (e *settledError) Unwrap() error { return e.Err }
