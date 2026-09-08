package teamskills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sageox/ox/internal/fileutil"
)

// Approval records a human's decision to materialize one executable team skill,
// pinned to the exact bytes they approved.
type Approval struct {
	Name       string    `json:"name"`
	Digest     string    `json:"digest"`
	ApprovedAt time.Time `json:"approved_at"`
	// Capabilities is what was approved, recorded so a later reader can see what
	// the decision covered without re-deriving it from content that may since have
	// changed.
	Capabilities []Capability `json:"capabilities,omitempty"`
	// AllowScripts records that the approver explicitly accepted bundled scripts
	// becoming executable on disk. Without it, scripts materialize non-executable.
	AllowScripts bool `json:"allow_scripts,omitempty"`
}

// ApprovalStore is the committed record of which executable team skills this
// project has accepted.
//
// It lives in the project's .sageox/ directory and is COMMITTED on purpose: the
// decision is a property of the project, not of one developer's machine, and a
// teammate pulling the repository should inherit it rather than be prompted
// again. That also makes the decision reviewable in a pull request, which is the
// only place a human is likely to notice a skill quietly gaining a script.
type ApprovalStore struct {
	SchemaVersion int        `json:"schema_version"`
	Approvals     []Approval `json:"approvals,omitempty"`
}

const approvalSchemaVersion = 1

// ApprovalPath is where the store lives inside a project.
func ApprovalPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".sageox", "team-skills.approvals.json")
}

// LoadApprovals reads the store. A missing file is an empty store, not an error:
// a project that has never approved an executable team skill is the normal case.
//
// A CORRUPT file, by contrast, IS an error. Treating it as empty would silently
// revoke every approval and re-prompt, and — worse — a caller that ignored the
// error would read "no approvals" as "nothing was ever approved" rather than "I
// could not tell", which is the fail-open shape this codebase keeps finding.
func LoadApprovals(projectRoot string) (*ApprovalStore, error) {
	path := ApprovalPath(projectRoot)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &ApprovalStore{SchemaVersion: approvalSchemaVersion}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read team skill approvals: %w", err)
	}
	var store ApprovalStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("parse team skill approvals at %s: %w", path, err)
	}
	if store.SchemaVersion > approvalSchemaVersion {
		return nil, fmt.Errorf("team skill approvals use schema %d; this ox supports %d",
			store.SchemaVersion, approvalSchemaVersion)
	}
	return &store, nil
}

// Save writes the store atomically, sorted, so a committed file does not churn.
func (s *ApprovalStore) Save(projectRoot string) error {
	s.SchemaVersion = approvalSchemaVersion
	sort.Slice(s.Approvals, func(i, j int) bool {
		if s.Approvals[i].Name == s.Approvals[j].Name {
			return s.Approvals[i].Digest < s.Approvals[j].Digest
		}
		return s.Approvals[i].Name < s.Approvals[j].Name
	})
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode team skill approvals: %w", err)
	}
	path := ApprovalPath(projectRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create .sageox for approvals: %w", err)
	}
	return fileutil.AtomicWriteBytes(path, append(data, '\n'), 0o644)
}

// Approve records a decision for the exact bytes in v.
func (s *ApprovalStore) Approve(name string, v Verdict, allowScripts bool) {
	for i, a := range s.Approvals {
		if a.Name == name {
			s.Approvals[i] = Approval{
				Name: name, Digest: v.Digest, ApprovedAt: time.Now().UTC(),
				Capabilities: v.Capabilities, AllowScripts: allowScripts,
			}
			return
		}
	}
	s.Approvals = append(s.Approvals, Approval{
		Name: name, Digest: v.Digest, ApprovedAt: time.Now().UTC(),
		Capabilities: v.Capabilities, AllowScripts: allowScripts,
	})
}

// Decision is what ox should do with one discovered team skill.
type Decision int

const (
	// DecisionMaterialize — prose, or executable content with a current approval.
	DecisionMaterialize Decision = iota
	// DecisionNeedsApproval — executable, and no approval matches these bytes.
	DecisionNeedsApproval
)

// Decide applies the trust model to one classified skill.
//
// The digest comparison is the whole mechanism: an approval names bytes, so a
// skill that gains a script, an allowed-tools grant, or an inline command after
// approval no longer matches and returns to needing one. Approving a NAME would
// let the remote change what runs without anyone deciding again — and any
// teammate can push to that remote.
func (s *ApprovalStore) Decide(name string, v Verdict) Decision {
	if !v.Executable {
		return DecisionMaterialize
	}
	for _, a := range s.Approvals {
		if a.Name == name && a.Digest == v.Digest {
			return DecisionMaterialize
		}
	}
	return DecisionNeedsApproval
}

// ScriptsExecutable reports whether bundled scripts may be written with the
// executable bit set.
//
// Separate from Decide on purpose. Approving a skill so an agent can READ its
// instructions is a smaller decision than making its scripts directly runnable,
// and collapsing the two would mean the smaller decision silently grants the
// larger one.
func (s *ApprovalStore) ScriptsExecutable(name string, v Verdict) bool {
	for _, a := range s.Approvals {
		if a.Name == name && a.Digest == v.Digest {
			return a.AllowScripts
		}
	}
	return false
}
