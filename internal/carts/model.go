// Package carts provides work item tracking backed by DoltDB.
// Modeled after Claude Code's task system: simple CRUD, dependency graph, status machine.
package carts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// Issue represents a cart — a trackable work item.
type Issue struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	Status      Status    `json:"status"`
	Priority    int       `json:"priority"`
	IssueType   IssueType `json:"issue_type"`
	Assignee    string    `json:"assignee,omitempty"`
	Creator     string    `json:"creator,omitempty"`
	Source      string    `json:"source,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`

	// Populated on Show/Get for display
	Dependencies []*Dependency `json:"dependencies,omitempty"`
}

// Validate checks required fields and value constraints.
func (i *Issue) Validate() error {
	if i.Title == "" {
		return fmt.Errorf("title is required")
	}
	if len(i.Title) > 500 {
		return fmt.Errorf("title must be 500 characters or less (got %d)", len(i.Title))
	}
	if i.Priority < 0 || i.Priority > 4 {
		return fmt.Errorf("priority must be between 0 and 4 (got %d)", i.Priority)
	}
	if !i.Status.IsValid() {
		return fmt.Errorf("invalid status: %s", i.Status)
	}
	if !i.IssueType.IsValid() {
		return fmt.Errorf("invalid issue type: %s", i.IssueType)
	}
	return nil
}

// SetDefaults applies defaults for omitted fields.
func (i *Issue) SetDefaults() {
	if i.Status == "" {
		i.Status = StatusOpen
	}
	if i.IssueType == "" {
		i.IssueType = TypeTask
	}
	if i.Source == "" {
		i.Source = "cli"
	}
}

// Status represents the lifecycle state. Mirrors Claude Code: pending → in_progress → completed.
type Status string

const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusClosed     Status = "closed"
)

func (s Status) IsValid() bool {
	switch s {
	case StatusOpen, StatusInProgress, StatusClosed:
		return true
	}
	return false
}

// IssueType categorizes the work item.
type IssueType string

const (
	TypeBug     IssueType = "bug"
	TypeFeature IssueType = "feature"
	TypeTask    IssueType = "task"
	TypeEpic    IssueType = "epic"
	TypeChore   IssueType = "chore"
)

func (t IssueType) IsValid() bool {
	switch t {
	case TypeBug, TypeFeature, TypeTask, TypeEpic, TypeChore:
		return true
	}
	return false
}

// Dependency represents a relationship between two carts (the dependency graph).
type Dependency struct {
	IssueID     string         `json:"issue_id"`
	DependsOnID string         `json:"depends_on_id"`
	Type        DependencyType `json:"type"`
	CreatedAt   time.Time      `json:"created_at"`
}

// DependencyType categorizes dependency relationships.
type DependencyType string

const (
	DepBlocks        DependencyType = "blocks"
	DepRelated       DependencyType = "related"
	DepDiscoveredFrom DependencyType = "discovered-from"
)

func (d DependencyType) IsValid() bool {
	switch d {
	case DepBlocks, DepRelated, DepDiscoveredFrom:
		return true
	}
	return false
}

// GenerateID creates a deterministic content-based hash ID.
func GenerateID(title, description string, createdAt time.Time) string {
	h := sha256.New()
	h.Write([]byte(title))
	h.Write([]byte(description))
	h.Write([]byte(createdAt.Format(time.RFC3339Nano)))
	return hex.EncodeToString(h.Sum(nil))
}
