package carts

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	_ "github.com/go-sql-driver/mysql"
)

// Store wraps a DoltDB connection for carts CRUD operations.
type Store struct {
	db             *sql.DB
	database       string
	committerName  string
	committerEmail string
	closed         atomic.Bool
}

// Config holds the store configuration.
type Config struct {
	CartsDir       string
	Database       string
	CommitterName  string
	CommitterEmail string
	ServerHost     string
	ServerPort     int
}

// New creates or connects to a carts DoltDB store.
func New(ctx context.Context, cfg *Config) (*Store, error) {
	if cfg.ServerHost == "" {
		cfg.ServerHost = "127.0.0.1"
	}
	if cfg.Database == "" {
		cfg.Database = "carts"
	}

	if err := os.MkdirAll(cfg.CartsDir, 0o750); err != nil {
		return nil, fmt.Errorf("create carts dir: %w", err)
	}

	port := cfg.ServerPort
	if port == 0 {
		var err error
		port, err = EnsureServer(cfg.CartsDir)
		if err != nil {
			return nil, fmt.Errorf("ensure dolt server: %w", err)
		}
	}

	user := cfg.CommitterName
	if user == "" {
		user = "root"
	}
	dsn := fmt.Sprintf("%s@tcp(%s:%d)/?parseTime=true&interpolateParams=true&timeout=5s&readTimeout=30s&writeTimeout=30s",
		user, cfg.ServerHost, port)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql connection: %w", err)
	}
	db.SetMaxOpenConns(10)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping dolt server: %w", err)
	}

	if _, err := db.ExecContext(ctx, "CREATE DATABASE IF NOT EXISTS "+cfg.Database); err != nil {
		db.Close()
		return nil, fmt.Errorf("create database: %w", err)
	}
	if _, err := db.ExecContext(ctx, "USE "+cfg.Database); err != nil {
		db.Close()
		return nil, fmt.Errorf("use database: %w", err)
	}

	s := &Store{
		db:             db,
		database:       cfg.Database,
		committerName:  cfg.CommitterName,
		committerEmail: cfg.CommitterEmail,
	}

	if err := s.initSchema(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}

	return s, nil
}

// initSchema creates tables and views if they don't exist.
func (s *Store) initSchema(ctx context.Context) error {
	version, _ := s.getMetadata(ctx, "schema_version")
	if version == strconv.Itoa(currentSchemaVersion) {
		return nil
	}

	for _, stmt := range splitStatements(schema) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" || strings.HasPrefix(stmt, "--") {
			continue
		}
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("execute DDL %q: %w", truncate(stmt, 80), err)
		}
	}

	if view := strings.TrimSpace(readyIssuesView); view != "" {
		if _, err := s.db.ExecContext(ctx, view); err != nil {
			slog.Warn("create view failed (non-fatal)", "err", err)
		}
	}

	s.setMetadata(ctx, "schema_version", strconv.Itoa(currentSchemaVersion))
	s.doltCommit(ctx, "schema: init carts v"+strconv.Itoa(currentSchemaVersion))
	return nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	return s.db.Close()
}

// Transaction wraps a SQL transaction for Dolt commit tracking.
type Transaction struct {
	tx    *sql.Tx
	store *Store
	dirty bool
}

// RunInTransaction executes fn within a SQL transaction, then creates a Dolt commit.
func (s *Store) RunInTransaction(ctx context.Context, commitMsg string, fn func(tx *Transaction) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "USE "+s.database); err != nil {
		return fmt.Errorf("use database: %w", err)
	}

	sqlTx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	dtx := &Transaction{tx: sqlTx, store: s}

	if err := fn(dtx); err != nil {
		sqlTx.Rollback()
		return err
	}

	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("commit sql: %w", err)
	}

	if dtx.dirty {
		if _, err := conn.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
			slog.Warn("dolt add failed", "err", err)
		}
		author := fmt.Sprintf("%s <%s>", s.committerName, s.committerEmail)
		if s.committerName == "" {
			author = "carts <carts@sageox.ai>"
		}
		if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)", commitMsg, author); err != nil {
			if !strings.Contains(err.Error(), "nothing to commit") {
				slog.Warn("dolt commit failed", "err", err)
			}
		}
	}

	return nil
}

// Exec executes a query within the transaction and marks it dirty.
func (tx *Transaction) Exec(ctx context.Context, _ string, query string, args ...any) (sql.Result, error) {
	tx.dirty = true
	return tx.tx.ExecContext(ctx, query, args...)
}

// doltCommit creates a Dolt commit outside a transaction (for schema init).
func (s *Store) doltCommit(ctx context.Context, msg string) {
	if _, err := s.db.ExecContext(ctx, "CALL DOLT_ADD('-A')"); err != nil {
		slog.Debug("dolt add all failed", "err", err)
		return
	}
	author := fmt.Sprintf("%s <%s>", s.committerName, s.committerEmail)
	if s.committerName == "" {
		author = "carts <carts@sageox.ai>"
	}
	if _, err := s.db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)", msg, author); err != nil {
		if !strings.Contains(err.Error(), "nothing to commit") {
			slog.Debug("dolt commit failed", "err", err)
		}
	}
}

func (s *Store) getMetadata(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE `key` = ?", key).Scan(&value)
	return value, err
}

func (s *Store) setMetadata(ctx context.Context, key, value string) {
	s.db.ExecContext(ctx, "REPLACE INTO metadata (`key`, value) VALUES (?, ?)", key, value)
}

func splitStatements(sql string) []string {
	var stmts []string
	for _, s := range strings.Split(sql, ";") {
		s = strings.TrimSpace(s)
		if s != "" {
			stmts = append(stmts, s)
		}
	}
	return stmts
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Init initializes a new carts store in the given directory.
func Init(ctx context.Context, cartsDir, committerName, committerEmail string) (*Store, error) {
	return New(ctx, &Config{
		CartsDir:       cartsDir,
		Database:       "carts",
		CommitterName:  committerName,
		CommitterEmail: committerEmail,
	})
}

// OpenFromTeamContext resolves the carts directory from team context and opens the store.
func OpenFromTeamContext(ctx context.Context, teamContextDir, committerName, committerEmail string) (*Store, error) {
	cartsDir := filepath.Join(teamContextDir, "carts")
	return New(ctx, &Config{
		CartsDir:       cartsDir,
		Database:       "carts",
		CommitterName:  committerName,
		CommitterEmail: committerEmail,
	})
}
