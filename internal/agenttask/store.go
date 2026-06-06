package agenttask

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/proc"
	_ "modernc.org/sqlite"
)

// ErrTaskNotFound is returned when an operation targets an unknown task id.
var ErrTaskNotFound = errors.New("task not found")

const (
	sageoxDir = ".sageox"
	subDir    = "agent_tasks"
	fileName  = "agent_tasks.db"

	// maxActive caps the number of non-terminal tasks retained. Producers are
	// deduped, so this is a safety valve against runaway enqueueing, not a
	// normal operating point.
	maxActive = 500

	// terminalRetention is how long completed/canceled tasks stay listable
	// before being pruned. Long enough for an agent to read back the outcome
	// of work it just finished, short enough to keep the queue small.
	terminalRetention = 1 * time.Hour
)

// taskColumns is the canonical SELECT column order; scanTask reads in this order.
const taskColumns = `id, title, body, kind, priority, status, source, target_agent,
	dedup_key, payload, created_at, expires_at, claimed_by_agent_id, claimed_by_pid,
	claimed_host, claimed_at, lease_expires_at, attempts, completed_at, result`

// Store is an embedded-SQLite queue of agent tasks, shared per repo (not per
// user): the queue exists so the next available agent — whoever that is — can
// pick up work. Atomicity, dedup, and lease reclaim are enforced by the
// database, replacing the hand-rolled flock + JSONL rewrite of earlier versions.
//
// The store is local-only and ephemeral; it is gitignored and rebuildable. NFS
// is unsupported (embedded SQLite locking is undefined there) — the same
// local-working-dir assumption the JSONL store carried.
type Store struct {
	db   *sql.DB
	host string
}

// QueuePath returns the absolute path to a project's task queue database.
func QueuePath(projectRoot string) string {
	return filepath.Join(projectRoot, sageoxDir, subDir, fileName)
}

// QueueExists reports whether a project has a task queue yet. Read-only callers
// (the prompt hook, ox status) use it to avoid NewStore's MkdirAll side effect —
// a status/surface read must not materialize the queue directory.
func QueueExists(projectRoot string) bool {
	if projectRoot == "" {
		return false
	}
	_, err := os.Stat(QueuePath(projectRoot))
	if err == nil {
		return true
	}
	// A permission/IO fault is not "absent" — treat only true not-exist as no
	// queue, so an operational fault surfaces rather than silently suppressing
	// task surfacing.
	return !errors.Is(err, os.ErrNotExist)
}

// NewStore opens (or creates) a task store for the given project root, creating
// the .sageox/agent_tasks/ directory if needed. Callers should Close the store;
// for one-shot producers prefer Enqueue.
func NewStore(projectRoot string) (*Store, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("project root cannot be empty")
	}
	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve project root: %w", err)
	}
	storePath := filepath.Join(absRoot, sageoxDir, subDir)
	if err := os.MkdirAll(storePath, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create task directory: %w", err)
	}
	dbPath := filepath.Join(storePath, fileName)

	db, err := openDB(dbPath)
	if err != nil {
		// Corrupt or unreadable DB: the queue is ephemeral, so rebuild it
		// rather than wedging every command behind a CANTOPEN error.
		removeDBFiles(dbPath)
		db, err = openDB(dbPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open task db: %w", err)
		}
	}
	if err := CreateSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create task schema: %w", err)
	}

	host, _ := os.Hostname()
	return &Store{db: db, host: host}, nil
}

func openDB(dbPath string) (*sql.DB, error) {
	dsn := dbPath + "?" + strings.Join([]string{
		"_pragma=journal_mode(WAL)",   // concurrent readers (hook, status) during a writer's claim
		"_pragma=busy_timeout(5000)",  // serialize concurrent writers instead of failing fast
		"_pragma=synchronous(NORMAL)", // ephemeral queue — durability is not worth the fsync cost
		"_pragma=foreign_keys(1)",
		"_pragma=temp_store(MEMORY)",
		// Begin transactions in IMMEDIATE mode so a read-then-write txn (Add's
		// dedup check + insert) takes the write lock upfront. Deferred txns would
		// deadlock under concurrent producers (SQLITE_BUSY on lock upgrade);
		// IMMEDIATE makes them queue on busy_timeout instead.
		"_txlock=immediate",
	}, "&")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	var integ string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integ); err != nil || integ != "ok" {
		db.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("integrity_check: %s", integ)
	}
	// Schema-drift guard: a structurally-valid DB from an older schema passes
	// integrity_check but would fail queries referencing new columns. user_version
	// 0 = fresh/uninitialized (CreateSchema stamps it next); any other non-current
	// value means a stale generation → error so NewStore recreates it.
	var uv int
	if err := db.QueryRow("PRAGMA user_version").Scan(&uv); err != nil {
		db.Close()
		return nil, err
	}
	if uv != 0 && uv != SchemaVersion {
		db.Close()
		return nil, fmt.Errorf("schema version mismatch: db=%d want=%d", uv, SchemaVersion)
	}
	return db, nil
}

func removeDBFiles(dbPath string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(dbPath + suffix)
	}
}

// Close releases the database handle. Safe to call on a nil-db store.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Add inserts a new task. The id, created-at, and status are filled in when
// empty. If the task carries a DedupKey and an active (non-terminal) task with
// that key already exists, Add is a no-op and reports added=false. The active
// cap and dedup are enforced inside one transaction (plus a DB unique index),
// so concurrent producers cannot exceed the cap or double-enqueue a key.
func (s *Store) Add(task *Task) (added bool, err error) {
	if task == nil {
		return false, fmt.Errorf("task cannot be nil")
	}
	if task.Title == "" {
		return false, fmt.Errorf("task title cannot be empty")
	}
	if !ValidKind(task.Kind) {
		return false, fmt.Errorf("unknown task kind %q (allowed: doctor, session-finalize, anti-entropy, custom)", task.Kind)
	}
	if len(task.Title) > MaxTitleLen {
		return false, fmt.Errorf("task title exceeds %d bytes", MaxTitleLen)
	}
	if len(task.Body) > MaxBodyLen {
		return false, fmt.Errorf("task body exceeds %d bytes", MaxBodyLen)
	}
	if payloadSize(task.Payload) > MaxPayloadLen {
		return false, fmt.Errorf("task payload exceeds %d bytes", MaxPayloadLen)
	}
	// payload is surfaced verbatim to the model on claim; reject keys that look
	// like they carry a credential so a producer can't leak a secret into the
	// agent's context window.
	if k := sensitivePayloadKey(task.Payload); k != "" {
		return false, fmt.Errorf("task payload key %q looks sensitive; payload is surfaced to an AI coworker and must not carry secrets", k)
	}

	ctx := context.Background()
	if err := s.reconcile(ctx); err != nil {
		return false, err
	}

	if task.ID == "" {
		task.ID = newTaskID()
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now()
	}
	// New tasks must start ready. A caller-supplied in_progress/terminal status
	// (with no lease) would be neither claimable nor reclaimable — permanently
	// wedged. Producers schedule ready work; the store owns every later state.
	if task.Status != "" && task.Status != StatusReady {
		return false, fmt.Errorf("new tasks must start %q, got %q", StatusReady, task.Status)
	}
	task.Status = StatusReady
	// Normalize the target on write so the claim filter is a plain equality.
	target := ""
	if task.TargetAgent != "" {
		target = NormalizeAgentType(task.TargetAgent)
	}
	payload, err := payloadToDB(task.Payload)
	if err != nil {
		return false, fmt.Errorf("failed to encode payload: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	if task.DedupKey != "" {
		var exists int
		err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM tasks WHERE dedup_key = ? AND status NOT IN ('completed','canceled') LIMIT 1`,
			task.DedupKey).Scan(&exists)
		if err == nil {
			return false, nil // active duplicate
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}

	if err := evictForCapTx(ctx, tx); err != nil {
		return false, err
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO tasks (`+taskColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		task.ID, task.Title, task.Body, task.Kind, task.Priority, string(task.Status),
		task.Source, target, task.DedupKey, payload,
		tsToDB(task.CreatedAt), tsToDB(task.ExpiresAt),
		task.ClaimedByAgentID, task.ClaimedByPID, task.ClaimedHost,
		tsToDB(task.ClaimedAt), tsToDB(task.LeaseExpiresAt), task.Attempts,
		tsToDB(task.CompletedAt), task.Result)
	if err != nil {
		// The partial unique index is the race-proof backstop to the dedup
		// pre-check above: a producer that raced us to the same key fails here.
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return false, nil
		}
		return false, fmt.Errorf("failed to insert task: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// evictForCapTx keeps the active (non-terminal) task count under maxActive by
// deleting the LEAST urgent ready tasks (highest Priority number, then oldest).
// It never evicts an in_progress task (in-flight work) nor a terminal one
// (those age out via retention). If the active set is entirely non-ready the
// cap may be exceeded rather than lose work — matching the JSONL store.
func evictForCapTx(ctx context.Context, tx *sql.Tx) error {
	var active int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE status NOT IN ('completed','canceled')`).Scan(&active); err != nil {
		return err
	}
	// +1: make room for the row about to be inserted.
	over := active - maxActive + 1
	if over <= 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`DELETE FROM tasks WHERE id IN (
			SELECT id FROM tasks WHERE status = 'ready'
			ORDER BY priority DESC, created_at ASC LIMIT ?)`, over)
	return err
}

// List returns active tasks priority-sorted (lower first), then oldest-first.
// When includeTerminal is false, terminal tasks are omitted. Reconciles stale
// leases and prunes expired/old-terminal rows first.
func (s *Store) List(includeTerminal bool) ([]*Task, error) {
	ctx := context.Background()
	if err := s.reconcile(ctx); err != nil {
		return nil, err
	}
	query := `SELECT ` + taskColumns + ` FROM tasks`
	if !includeTerminal {
		query += ` WHERE status NOT IN ('completed','canceled')`
	}
	query += ` ORDER BY priority ASC, created_at ASC`
	return s.queryTasks(ctx, query)
}

// ListView is retained for API compatibility. With the SQLite store the
// reconcile is a cheap indexed UPDATE/DELETE (it writes only when something
// actually expired), so the read-only fast path collapsed into List.
func (s *Store) ListView(includeTerminal bool) ([]*Task, error) {
	return s.List(includeTerminal)
}

// Ready returns ready tasks claimable by the given agent type, priority-sorted.
// An empty agentType only matches untargeted tasks.
func (s *Store) Ready(agentType string) ([]*Task, error) {
	ctx := context.Background()
	if err := s.reconcile(ctx); err != nil {
		return nil, err
	}
	return s.queryTasks(ctx,
		`SELECT `+taskColumns+` FROM tasks
		 WHERE status = 'ready' AND (target_agent = '' OR target_agent = ?)
		 ORDER BY priority ASC, created_at ASC`,
		NormalizeAgentType(agentType))
}

// ReadyView is retained for API compatibility; see ListView.
func (s *Store) ReadyView(agentType string) ([]*Task, error) {
	return s.Ready(agentType)
}

// Get returns a single task by id.
func (s *Store) Get(id string) (*Task, error) {
	ctx := context.Background()
	if err := s.reconcile(ctx); err != nil {
		return nil, err
	}
	tasks, err := s.queryTasks(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrTaskNotFound, id)
	}
	return tasks[0], nil
}

// ClaimOptions parameterizes Claim.
type ClaimOptions struct {
	AgentID   string        // ox internal agent id of the claimer
	AgentType string        // claimer's agent type (for target matching)
	PID       int           // claimer's process id (host-local liveness)
	Lease     time.Duration // how long to hold the claim; defaults to DefaultLease
}

// Claim atomically pops the highest-priority ready task the claimer is eligible
// for, marks it in_progress, and stamps the lease. Returns (nil, nil) when no
// eligible task is available. The guarded UPDATE (WHERE status='ready') makes a
// concurrent double-claim impossible: a racing claimer affects zero rows and
// moves to the next candidate.
func (s *Store) Claim(opts ClaimOptions) (*Task, error) {
	if opts.AgentID == "" {
		return nil, fmt.Errorf("claim requires an agent id")
	}
	lease := opts.Lease
	if lease <= 0 {
		lease = DefaultLease
	}
	ctx := context.Background()
	if err := s.reconcile(ctx); err != nil {
		return nil, err
	}
	target := NormalizeAgentType(opts.AgentType)

	// Bounded by the ready-set size: each iteration either claims a task or
	// loses a race and drops that candidate.
	for attempts := 0; attempts < maxActive+1; attempts++ {
		var id string
		err := s.db.QueryRowContext(ctx,
			`SELECT id FROM tasks
			 WHERE status = 'ready' AND (target_agent = '' OR target_agent = ?)
			 ORDER BY priority ASC, created_at ASC LIMIT 1`, target).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}

		now := time.Now()
		res, err := s.db.ExecContext(ctx,
			`UPDATE tasks SET status='in_progress', claimed_by_agent_id=?, claimed_by_pid=?,
				claimed_host=?, claimed_at=?, lease_expires_at=?, attempts=attempts+1
			 WHERE id=? AND status='ready'`,
			opts.AgentID, opts.PID, s.host, tsToDB(now), tsToDB(now.Add(lease)), id)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			continue // lost the race for this id; try the next candidate
		}
		// Raw read (no reconcile): the row we just stamped in_progress must be
		// returned as-claimed. A reconcile here could immediately reclaim it
		// (e.g. a dead claimer PID) and hand back a ready, claim-cleared row.
		return s.getRaw(context.Background(), id)
	}
	return nil, nil
}

// getRaw returns a task by id without running reconcile — for callers that must
// observe the row exactly as a preceding write left it.
func (s *Store) getRaw(ctx context.Context, id string) (*Task, error) {
	tasks, err := s.queryTasks(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrTaskNotFound, id)
	}
	return tasks[0], nil
}

// Complete marks a task completed with an optional result note. Idempotent on
// already-terminal tasks.
func (s *Store) Complete(id, result string) error {
	return s.terminate(id, StatusCompleted, result)
}

// Cancel marks a task canceled with an optional reason. Idempotent on
// already-terminal tasks.
func (s *Store) Cancel(id, reason string) error {
	return s.terminate(id, StatusCanceled, reason)
}

func (s *Store) terminate(id string, status Status, note string) error {
	ctx := context.Background()
	if err := s.reconcile(ctx); err != nil {
		return err
	}
	// idempotent: terminating an already-terminal task is a no-op success.
	var cur string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, id).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, id)
	}
	if err != nil {
		return err
	}
	if cur == string(StatusCompleted) || cur == string(StatusCanceled) {
		return nil
	}
	// The status guard makes the write race-safe: if a concurrent terminate (or
	// a reclaim) wrote a terminal/changed state between the check above and this
	// UPDATE, this affects zero rows rather than clobbering the first writer.
	_, err = s.db.ExecContext(ctx,
		`UPDATE tasks SET status=?, completed_at=?, result=CASE WHEN ?='' THEN result ELSE ? END,
			claimed_by_agent_id='', claimed_by_pid=0, claimed_host='', claimed_at=0, lease_expires_at=0
		 WHERE id=? AND status NOT IN ('completed','canceled')`,
		string(status), tsToDB(time.Now()), note, note, id)
	return err
}

// ExtendLease pushes out the lease deadline of an in_progress task, letting a
// long-running agent keep its claim. The task must still be in_progress.
func (s *Store) ExtendLease(id string, lease time.Duration) error {
	if lease <= 0 {
		lease = DefaultLease
	}
	ctx := context.Background()
	if err := s.reconcile(ctx); err != nil {
		return err
	}
	var cur string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, id).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrTaskNotFound, id)
	}
	if err != nil {
		return err
	}
	if cur != string(StatusInProgress) {
		return fmt.Errorf("task %s is not in progress (status=%s)", id, cur)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET lease_expires_at=? WHERE id=? AND status='in_progress'`,
		tsToDB(time.Now().Add(lease)), id)
	if err != nil {
		return err
	}
	// 0 rows means the lease lapsed and reconcile reset the task to ready in the
	// window between the check and this UPDATE. Surface it: the caller must NOT
	// keep working as if it still holds the claim (a second agent may have it).
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("task %s: lease not extended (claim lost while extending)", id)
	}
	return nil
}

// reconcile self-heals the queue on every read/mutation: reclaim lease-expired
// in_progress tasks, reclaim tasks whose claiming process is dead on THIS host,
// drop expired non-in_progress tasks, and prune terminal tasks past retention.
//
// In-progress tasks are never expiry-dropped — a claimed task must not vanish
// out from under the agent executing it (its later `tasks done` would 404).
func (s *Store) reconcile(ctx context.Context) error {
	now := tsToDB(time.Now())

	// lease-expired in_progress -> ready
	if _, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status='ready', claimed_by_agent_id='', claimed_by_pid=0,
			claimed_host='', claimed_at=0, lease_expires_at=0
		 WHERE status='in_progress' AND lease_expires_at != 0 AND lease_expires_at < ?`,
		now); err != nil {
		return err
	}

	// dead-claimer reclaim — PID liveness is only meaningful for a non-empty
	// host equal to ours. Cross-host or unknown-host claims are left to lease
	// expiry above; this is the same-host guard that the bare PID check lacked.
	if err := s.reclaimDeadClaimers(ctx); err != nil {
		return err
	}

	// drop expired tasks that are not being worked
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM tasks WHERE expires_at != 0 AND expires_at < ? AND status != 'in_progress'`,
		now); err != nil {
		return err
	}

	// prune terminal tasks past the retention window
	cutoff := tsToDB(time.Now().Add(-terminalRetention))
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM tasks WHERE status IN ('completed','canceled') AND completed_at != 0 AND completed_at < ?`,
		cutoff); err != nil {
		return err
	}
	return nil
}

func (s *Store) reclaimDeadClaimers(ctx context.Context) error {
	if s.host == "" {
		return nil // can't trust a PID without knowing our own host
	}
	// Collect (id, pid) fully and close the cursor BEFORE any liveness syscall:
	// proc.IsAlive can block on a kill(0)/proc read, and holding an open SQLite
	// statement handle across it is unnecessary and can stall the connection.
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, claimed_by_pid FROM tasks
		 WHERE status='in_progress' AND claimed_host = ? AND claimed_by_pid > 0`, s.host)
	if err != nil {
		return err
	}
	type claim struct {
		id  string
		pid int
	}
	var claims []claim
	for rows.Next() {
		var c claim
		if err := rows.Scan(&c.id, &c.pid); err != nil {
			rows.Close()
			return err
		}
		claims = append(claims, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	var dead []string
	for _, c := range claims {
		if !proc.IsAlive(c.pid) {
			dead = append(dead, c.id)
		}
	}

	for _, id := range dead {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE tasks SET status='ready', claimed_by_agent_id='', claimed_by_pid=0,
				claimed_host='', claimed_at=0, lease_expires_at=0
			 WHERE id=? AND status='in_progress'`, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) queryTasks(ctx context.Context, query string, args ...any) ([]*Task, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*Task, 0)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTask(r rowScanner) (*Task, error) {
	var (
		t                                                Task
		status, payload                                  string
		createdAt, expiresAt, claimedAt, leaseAt, doneAt int64
	)
	if err := r.Scan(
		&t.ID, &t.Title, &t.Body, &t.Kind, &t.Priority, &status, &t.Source, &t.TargetAgent,
		&t.DedupKey, &payload, &createdAt, &expiresAt, &t.ClaimedByAgentID, &t.ClaimedByPID,
		&t.ClaimedHost, &claimedAt, &leaseAt, &t.Attempts, &doneAt, &t.Result,
	); err != nil {
		return nil, err
	}
	t.Status = Status(status)
	t.Payload = payloadFromDB(payload)
	t.CreatedAt = tsFromDB(createdAt)
	t.ExpiresAt = tsFromDB(expiresAt)
	t.ClaimedAt = tsFromDB(claimedAt)
	t.LeaseExpiresAt = tsFromDB(leaseAt)
	t.CompletedAt = tsFromDB(doneAt)
	return &t, nil
}

// tsToDB encodes a time as unix-nanoseconds, mapping the zero time to 0 so
// "unset" is distinguishable and sorts before any real deadline.
func tsToDB(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func tsFromDB(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

func payloadToDB(p map[string]string) (string, error) {
	if len(p) == 0 {
		return "", nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func payloadFromDB(s string) map[string]string {
	if s == "" {
		return nil
	}
	m := map[string]string{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

// sensitivePayloadKeyParts are case-insensitive substrings that mark a payload
// key as credential-bearing. "session" is intentionally absent — it is a
// legitimate payload key (e.g. the session-finalize producer) and only the
// compound "session_token" form is sensitive.
var sensitivePayloadKeyParts = []string{
	"password", "passwd", "secret", "token", "apikey", "api_key",
	"access_key", "private_key", "credential", "bearer", "client_secret",
}

// sensitivePayloadKey returns the first payload key whose name suggests it
// carries a credential, or "" if none do.
func sensitivePayloadKey(p map[string]string) string {
	for k := range p {
		lk := strings.ToLower(k)
		for _, part := range sensitivePayloadKeyParts {
			if strings.Contains(lk, part) {
				return k
			}
		}
	}
	return ""
}

// payloadSize returns the total bytes across a payload map's keys and values.
func payloadSize(p map[string]string) int {
	n := 0
	for k, v := range p {
		n += len(k) + len(v)
	}
	return n
}

// newTaskID returns a time-sortable UUIDv7 string, falling back to v4.
func newTaskID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.New().String()
}

// Enqueue is a convenience wrapper that opens a store for projectRoot, adds a
// single task, and closes the store. Intended for producers (the daemon,
// doctor) that schedule work without holding a long-lived store handle.
func Enqueue(projectRoot string, task *Task) (bool, error) {
	store, err := NewStore(projectRoot)
	if err != nil {
		return false, err
	}
	defer store.Close()
	return store.Add(task)
}
