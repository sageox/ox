package lfs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SessionMeta is the git-tracked metadata + OID manifest for a session.
// Stored as meta.json in each session folder. This is the ONLY file tracked by git;
// all content files are gitignored and stored in LFS blob storage.
type SessionMeta struct {
	Version     string             `json:"version"` // "1.0"
	SessionName string             `json:"session_name"`
	Username    string             `json:"username"` // email of author
	UserID      string             `json:"user_id,omitempty"`
	AgentID     string             `json:"agent_id"`
	AgentType   string             `json:"agent_type"` // "claude-code", "cursor", etc.
	Model       string             `json:"model,omitempty"`
	Title       string             `json:"title,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	EntryCount  int                `json:"entry_count,omitempty"`
	Summary     string             `json:"summary,omitempty"`
	RepoID      string             `json:"repo_id,omitempty"`
	Files       map[string]FileRef `json:"files"` // OID manifest: filename -> ref
}

// FileRef identifies a content file by its LFS OID and size.
type FileRef struct {
	OID  string `json:"oid"`  // "sha256:<hex>"
	Size int64  `json:"size"` // bytes
}

// HydrationStatus describes whether a session's content files are present locally.
type HydrationStatus string

const (
	// HydrationStatusHydrated means all content files are present locally.
	HydrationStatusHydrated HydrationStatus = "hydrated"
	// HydrationStatusDehydrated means no content files are present (only meta.json).
	HydrationStatusDehydrated HydrationStatus = "dehydrated"
	// HydrationStatusPartial means some content files are present.
	HydrationStatusPartial HydrationStatus = "partial"
)

// SessionMetaBuilder constructs SessionMeta with required fields and optional setters.
type SessionMetaBuilder struct {
	meta SessionMeta
}

// NewSessionMeta creates a builder with required fields pre-filled.
func NewSessionMeta(sessionName, username, agentID, agentType string, createdAt time.Time) *SessionMetaBuilder {
	return &SessionMetaBuilder{
		meta: SessionMeta{
			Version:     "1.0",
			SessionName: sessionName,
			Username:    username,
			AgentID:     agentID,
			AgentType:   agentType,
			CreatedAt:   createdAt,
			Files:       make(map[string]FileRef),
		},
	}
}

func (b *SessionMetaBuilder) Model(m string) *SessionMetaBuilder {
	b.meta.Model = m
	return b
}

func (b *SessionMetaBuilder) Title(t string) *SessionMetaBuilder {
	b.meta.Title = t
	return b
}

func (b *SessionMetaBuilder) Summary(s string) *SessionMetaBuilder {
	b.meta.Summary = s
	return b
}

func (b *SessionMetaBuilder) EntryCount(n int) *SessionMetaBuilder {
	b.meta.EntryCount = n
	return b
}

func (b *SessionMetaBuilder) UserID(id string) *SessionMetaBuilder {
	b.meta.UserID = id
	return b
}

func (b *SessionMetaBuilder) RepoID(id string) *SessionMetaBuilder {
	b.meta.RepoID = id
	return b
}

func (b *SessionMetaBuilder) WithFiles(f map[string]FileRef) *SessionMetaBuilder {
	b.meta.Files = f
	return b
}

// Build returns the constructed SessionMeta.
func (b *SessionMetaBuilder) Build() *SessionMeta {
	return &b.meta
}

const metaFilename = "meta.json"

// WriteSessionMeta writes meta.json to the given session directory.
func WriteSessionMeta(sessionPath string, meta *SessionMeta) error {
	if meta == nil {
		return fmt.Errorf("nil session meta")
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session meta: %w", err)
	}

	metaPath := filepath.Join(sessionPath, metaFilename)

	// atomic write
	tmpPath := metaPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write session meta: %w", err)
	}
	if err := os.Rename(tmpPath, metaPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename session meta: %w", err)
	}

	return nil
}

// ReadSessionMeta reads meta.json from the given session directory.
func ReadSessionMeta(sessionPath string) (*SessionMeta, error) {
	metaPath := filepath.Join(sessionPath, metaFilename)

	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("meta.json not found in %s", sessionPath)
		}
		return nil, fmt.Errorf("read session meta: %w", err)
	}

	var meta SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parse session meta: %w", err)
	}

	return &meta, nil
}

// CheckHydrationStatus checks which content files exist locally for a session.
// Returns hydrated if all files in the OID manifest are present,
// dehydrated if none are present, partial if some are present.
func CheckHydrationStatus(sessionPath string, meta *SessionMeta) HydrationStatus {
	if meta == nil || len(meta.Files) == 0 {
		return HydrationStatusDehydrated
	}

	present := 0
	total := len(meta.Files)

	for filename := range meta.Files {
		filePath := filepath.Join(sessionPath, filename)
		if _, err := os.Stat(filePath); err == nil {
			present++
		}
	}

	switch present {
	case 0:
		return HydrationStatusDehydrated
	case total:
		return HydrationStatusHydrated
	default:
		return HydrationStatusPartial
	}
}

// NewFileRef creates a FileRef from content bytes.
func NewFileRef(content []byte) FileRef {
	return FileRef{
		OID:  "sha256:" + ComputeOID(content),
		Size: int64(len(content)),
	}
}

// BareOID returns the hex digest without the "sha256:" prefix.
func (f FileRef) BareOID() string {
	if len(f.OID) > 7 && f.OID[:7] == "sha256:" {
		return f.OID[7:]
	}
	return f.OID
}
