// Package store provides a SQLite-backed whisper store for the daemon.
//
// The store tracks whisper entries (inbound signals to agents), per-agent
// delivery cursors, and relayed murmur dedup state. It is designed as an
// ephemeral local cache — all data is rebuildable from the ledger's murmur
// files. If corrupt, the store auto-recovers by deleting and recreating.
//
// See docs/ai/specs/daemon-state-principles.md for design principles.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Importance levels (sender sets this).
type Importance string

const (
	ImportanceCritical Importance = "critical"
	ImportanceNormal   Importance = "normal"
	ImportanceAmbient  Importance = "ambient"
)

// Attention levels (receiver configures this).
type Attention string

const (
	AttentionAll     Attention = "all"     // hear everything
	AttentionNormal  Attention = "normal"  // critical + normal (default)
	AttentionFocused Attention = "focused" // critical only
)

// WhisperType categorizes the origin of a whisper.
type WhisperType string

const (
	WhisperStructural WhisperType = "structural"
	WhisperTimeBased  WhisperType = "time-based"
	WhisperTrigger    WhisperType = "trigger"
)

// WhisperEntry is a single whisper destined for agent delivery.
type WhisperEntry struct {
	ID            string            `json:"id"`
	Scope         string            `json:"scope"`                    // "ledger" or "team"
	Type          WhisperType       `json:"type"`
	Source        string            `json:"source"`
	Topic         string            `json:"topic"`
	Content       string            `json:"content"`
	Importance    Importance        `json:"importance"`
	CreatedAt     time.Time         `json:"created_at"`
	AgentID       string            `json:"agent_id,omitempty"`
	PrincipalID   string            `json:"principal_id,omitempty"`   // who the agent works for (optional)
	PrincipalType string            `json:"principal_type,omitempty"` // "human" for now (optional, extensible)
	TeamID        string            `json:"team_id,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// Store wraps a SQLite database for whisper state.
// All write access goes through the daemon (single writer).
// Multiple readers (CLI via IPC) are safe via WAL mode.
type Store struct {
	db        *sql.DB
	dbPath    string
	closeOnce sync.Once
}

// Open opens (or creates) a whisper store at the given path.
// Auto-recovers from corruption by deleting and recreating the DB.
// Callers never see corruption errors — the store silently rebuilds.
func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, fmt.Errorf("create whisper db dir: %w", err)
	}

	db, err := openSQLite(dbPath)
	if err != nil {
		slog.Warn("whisper db open failed, recreating", "path", dbPath, "err", err)
		removeSQLiteFiles(dbPath)
		db, err = openSQLite(dbPath)
		if err != nil {
			return nil, fmt.Errorf("open whisper db after recovery: %w", err)
		}
	}

	if err := checkIntegrity(db); err != nil {
		db.Close()
		slog.Warn("whisper db corrupt, recreating", "path", dbPath, "err", err)
		removeSQLiteFiles(dbPath)
		db, err = openSQLite(dbPath)
		if err != nil {
			return nil, fmt.Errorf("open whisper db after corruption recovery: %w", err)
		}
	}

	if err := CreateSchema(db); err != nil {
		db.Close()
		slog.Warn("whisper db schema failed, recreating", "path", dbPath, "err", err)
		removeSQLiteFiles(dbPath)
		db, err = openSQLite(dbPath)
		if err != nil {
			return nil, fmt.Errorf("open whisper db after schema recovery: %w", err)
		}
		if err := CreateSchema(db); err != nil {
			db.Close()
			return nil, fmt.Errorf("create whisper schema: %w", err)
		}
	}

	return &Store{db: db, dbPath: dbPath}, nil
}

func openSQLite(dbPath string) (*sql.DB, error) {
	dsn := dbPath + "?" + strings.Join([]string{
		"_pragma=journal_mode(WAL)",
		"_pragma=foreign_keys(1)",
		"_pragma=busy_timeout(5000)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=cache_size(-8192)", // 8 MB — whisper DB is small
		"_pragma=temp_store(MEMORY)",
	}, "&")
	return sql.Open("sqlite", dsn)
}

func checkIntegrity(db *sql.DB) error {
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("integrity_check query: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check: %s", result)
	}
	return nil
}

func removeSQLiteFiles(dbPath string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := dbPath + suffix
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("failed to remove whisper db file", "path", p, "err", err)
		}
	}
}

// Close closes the store. Safe to call multiple times.
func (s *Store) Close() error {
	var firstErr error
	s.closeOnce.Do(func() {
		if s.db != nil {
			firstErr = s.db.Close()
		}
	})
	return firstErr
}

// Add inserts whisper entries. Duplicate IDs are silently ignored (upsert).
func (s *Store) Add(entries ...WhisperEntry) error {
	if len(entries) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO whispers
		(id, scope, type, source, topic, content, importance, created_at,
		 agent_id, principal_id, principal_type, team_id, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, e := range entries {
		var metadataJSON *string
		if len(e.Metadata) > 0 {
			b, _ := json.Marshal(e.Metadata)
			s := string(b)
			metadataJSON = &s
		}
		_, err := stmt.Exec(
			e.ID, e.Scope, string(e.Type), e.Source, e.Topic,
			e.Content, string(e.Importance),
			e.CreatedAt.UTC().Format(time.RFC3339Nano),
			nilIfEmpty(e.AgentID), nilIfEmpty(e.PrincipalID),
			nilIfEmpty(e.PrincipalType), nilIfEmpty(e.TeamID),
			metadataJSON,
		)
		if err != nil {
			return fmt.Errorf("insert whisper %s: %w", e.ID, err)
		}
	}

	return tx.Commit()
}

// GetWhispers returns whisper entries for an agent, filtered by attention and topics.
// Updates the agent's cursor so subsequent calls only return new entries.
// On first call for an unknown agent, returns all current entries.
func (s *Store) GetWhispers(agentID string, attention Attention, topics []string) ([]WhisperEntry, error) {
	now := time.Now().UTC()

	// read current cursor
	var cursor time.Time
	var cursorStr sql.NullString
	err := s.db.QueryRow("SELECT last_seen FROM cursors WHERE agent_id = ?", agentID).Scan(&cursorStr)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read cursor: %w", err)
	}
	if cursorStr.Valid {
		cursor, _ = time.Parse(time.RFC3339Nano, cursorStr.String)
	}

	// build query with filters
	query := `SELECT id, scope, type, source, topic, content, importance, created_at,
		agent_id, principal_id, principal_type, team_id, metadata
		FROM whispers WHERE created_at > ?`
	args := []any{cursor.UTC().Format(time.RFC3339Nano)}

	// attention filter
	allowedImportance := importanceForAttention(attention)
	if len(allowedImportance) > 0 && len(allowedImportance) < 3 {
		placeholders := make([]string, len(allowedImportance))
		for i, imp := range allowedImportance {
			placeholders[i] = "?"
			args = append(args, string(imp))
		}
		query += " AND importance IN (" + strings.Join(placeholders, ",") + ")"
	}

	// topic filter
	if len(topics) > 0 {
		placeholders := make([]string, len(topics))
		for i, t := range topics {
			placeholders[i] = "?"
			args = append(args, t)
		}
		query += " AND topic IN (" + strings.Join(placeholders, ",") + ")"
	}

	// order: critical first, then normal, then ambient; within each, by time
	query += ` ORDER BY CASE importance
		WHEN 'critical' THEN 0
		WHEN 'normal' THEN 1
		WHEN 'ambient' THEN 2
		ELSE 3 END, created_at`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query whispers: %w", err)
	}
	defer rows.Close()

	var entries []WhisperEntry
	for rows.Next() {
		var e WhisperEntry
		var createdStr string
		var entryAgentID, principalID, principalType, teamID, metadataJSON sql.NullString
		var typ, imp string

		if err := rows.Scan(
			&e.ID, &e.Scope, &typ, &e.Source, &e.Topic, &e.Content, &imp, &createdStr,
			&entryAgentID, &principalID, &principalType, &teamID, &metadataJSON,
		); err != nil {
			return nil, fmt.Errorf("scan whisper: %w", err)
		}

		e.Type = WhisperType(typ)
		e.Importance = Importance(imp)
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdStr)
		e.AgentID = entryAgentID.String
		e.PrincipalID = principalID.String
		e.PrincipalType = principalType.String
		e.TeamID = teamID.String

		if metadataJSON.Valid && metadataJSON.String != "" {
			var m map[string]string
			if json.Unmarshal([]byte(metadataJSON.String), &m) == nil {
				e.Metadata = m
			}
		}

		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate whispers: %w", err)
	}

	// update cursor
	nowStr := now.Format(time.RFC3339Nano)
	_, err = s.db.Exec(
		`INSERT INTO cursors (agent_id, last_seen, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(agent_id) DO UPDATE SET last_seen = excluded.last_seen, updated_at = excluded.updated_at`,
		agentID, nowStr, nowStr,
	)
	if err != nil {
		return nil, fmt.Errorf("update cursor: %w", err)
	}

	if entries == nil {
		entries = []WhisperEntry{} // ensure JSON array, not null
	}
	return entries, nil
}

// IsRelayed checks if a murmur has already been relayed into the store.
func (s *Store) IsRelayed(murmurID, scope string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM relayed_murmurs WHERE murmur_id = ? AND scope = ?",
		murmurID, scope,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check relayed: %w", err)
	}
	return count > 0, nil
}

// MarkRelayed records that a murmur has been relayed into the store.
func (s *Store) MarkRelayed(murmurID, scope string) error {
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO relayed_murmurs (murmur_id, scope, relayed_at) VALUES (?, ?, ?)",
		murmurID, scope, time.Now().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("mark relayed: %w", err)
	}
	return nil
}

// RemoveCursor removes a specific agent's cursor.
// Called when an agent is cleaned up as stale.
func (s *Store) RemoveCursor(agentID string) error {
	_, err := s.db.Exec("DELETE FROM cursors WHERE agent_id = ?", agentID)
	if err != nil {
		return fmt.Errorf("remove cursor: %w", err)
	}
	return nil
}

// PruneResult holds counts from a prune operation.
type PruneResult struct {
	WhispersDeleted int64
	RelayedDeleted  int64
	CursorsDeleted  int64
	Vacuumed        bool
}

// Prune removes entries older than the given retention duration.
// Also prunes stale cursors (not updated in 7 days) and old relayed records.
func (s *Store) Prune(retention time.Duration) (PruneResult, error) {
	var result PruneResult
	cutoff := time.Now().Add(-retention).Format(time.RFC3339Nano)
	cursorCutoff := time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano)

	// delete old whispers
	res, err := s.db.Exec("DELETE FROM whispers WHERE created_at < ?", cutoff)
	if err != nil {
		return result, fmt.Errorf("prune whispers: %w", err)
	}
	result.WhispersDeleted, _ = res.RowsAffected()

	// delete old relayed records
	res, err = s.db.Exec("DELETE FROM relayed_murmurs WHERE relayed_at < ?", cutoff)
	if err != nil {
		return result, fmt.Errorf("prune relayed: %w", err)
	}
	result.RelayedDeleted, _ = res.RowsAffected()

	// delete stale cursors
	res, err = s.db.Exec("DELETE FROM cursors WHERE updated_at < ?", cursorCutoff)
	if err != nil {
		return result, fmt.Errorf("prune cursors: %w", err)
	}
	result.CursorsDeleted, _ = res.RowsAffected()

	// vacuum if enough was deleted
	totalDeleted := result.WhispersDeleted + result.RelayedDeleted + result.CursorsDeleted
	if totalDeleted > 100 {
		if _, err := s.db.Exec("VACUUM"); err != nil {
			slog.Warn("whisper db vacuum failed", "err", err)
		} else {
			result.Vacuumed = true
		}
	}

	return result, nil
}

// EnforceMaxSize checks the DB file size and aggressively prunes if over maxBytes.
func (s *Store) EnforceMaxSize(maxBytes int64) error {
	info, err := os.Stat(s.dbPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat whisper db: %w", err)
	}

	if info.Size() <= maxBytes {
		return nil
	}

	slog.Warn("whisper db over size limit, aggressive prune", "size", info.Size(), "limit", maxBytes)

	// keep only last 6 hours
	cutoff := time.Now().Add(-6 * time.Hour).Format(time.RFC3339Nano)
	if _, err := s.db.Exec("DELETE FROM whispers WHERE created_at < ?", cutoff); err != nil {
		return fmt.Errorf("aggressive prune whispers: %w", err)
	}
	if _, err := s.db.Exec("DELETE FROM relayed_murmurs WHERE relayed_at < ?", cutoff); err != nil {
		return fmt.Errorf("aggressive prune relayed: %w", err)
	}
	if _, err := s.db.Exec("VACUUM"); err != nil {
		return fmt.Errorf("vacuum after aggressive prune: %w", err)
	}

	// check if still over after prune
	info, err = os.Stat(s.dbPath)
	if err != nil {
		return nil
	}
	if info.Size() > maxBytes {
		slog.Warn("whisper db still over limit after prune, nuclear cleanup", "size", info.Size())
		if _, err := s.db.Exec("DELETE FROM whispers"); err != nil {
			return fmt.Errorf("nuclear prune whispers: %w", err)
		}
		if _, err := s.db.Exec("DELETE FROM relayed_murmurs"); err != nil {
			return fmt.Errorf("nuclear prune relayed: %w", err)
		}
		if _, err := s.db.Exec("VACUUM"); err != nil {
			return fmt.Errorf("vacuum after nuclear prune: %w", err)
		}
	}

	return nil
}

// CheckIntegrity validates that the whisper DB is healthy.
// Returns nil if OK, an error describing the problem otherwise.
func (s *Store) CheckIntegrity() error {
	return checkIntegrity(s.db)
}

// DBPath returns the path to the SQLite database file.
func (s *Store) DBPath() string {
	return s.dbPath
}

// importanceForAttention returns which importance levels pass the attention filter.
func importanceForAttention(attention Attention) []Importance {
	switch attention {
	case AttentionFocused:
		return []Importance{ImportanceCritical}
	case AttentionNormal:
		return []Importance{ImportanceCritical, ImportanceNormal}
	case AttentionAll:
		return []Importance{ImportanceCritical, ImportanceNormal, ImportanceAmbient}
	default:
		// default to normal
		return []Importance{ImportanceCritical, ImportanceNormal}
	}
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
