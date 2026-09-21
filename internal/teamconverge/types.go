// Package teamconverge coordinates delivery of typed Team Context artifacts
// into one product repository.
package teamconverge

import "context"

const ReportSchemaVersion = 1

type ArtifactKind string

const (
	KindSkill   ArtifactKind = "skill"
	KindRule    ArtifactKind = "rule"
	KindContext ArtifactKind = "context"
	KindTool    ArtifactKind = "tool"
)

type OutcomeState string

const (
	StateApplied         OutcomeState = "applied"
	StateIndexed         OutcomeState = "indexed"
	StateInjected        OutcomeState = "injected"
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
	OriginPack  OriginKind = "pack"
)

// Origin identifies who owns the canonical Team Context artifact. Pack origin
// affects diagnostics and update ownership only; delivery eligibility is
// determined from the same Artifact fields as a hand-authored loose artifact.
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
	ModeInspect   Mode = "inspect"
)

type Request struct {
	ProjectRoot string
	TeamPath    string
	RepoSlug    string
	TeamCommit  string
	Mode        Mode
	Additional  []Artifact
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
// artifact is intentionally absent here, and indexed/injected content is a
// valid delivery mode rather than a lesser success.
func (r Report) Converged() bool {
	for _, outcome := range r.Outcomes {
		switch outcome.State {
		case StateApplied, StateIndexed, StateInjected, StateFiltered:
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

type Discovery interface {
	Discover(context.Context, Request) (Snapshot, []Artifact, error)
}

type Handler interface {
	Kind() ArtifactKind
	Converge(context.Context, Request, Snapshot, []Artifact) ([]Outcome, error)
}

// RetryableError marks a handler failure that should remain pending and be
// retried without waiting for another Team Context commit.
type RetryableError struct {
	Err error
}

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }
