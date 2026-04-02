package carts

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CreateOpts holds options for creating a new cart.
type CreateOpts struct {
	Title       string
	Description string
	IssueType   IssueType
	Priority    int
	Assignee    string
	Creator     string
	Source      string
}

// Create creates a new cart and returns it.
func (s *Store) Create(ctx context.Context, opts CreateOpts) (*Issue, error) {
	now := time.Now().UTC()
	hashFull := GenerateID(opts.Title, opts.Description, now)
	id := "cart-" + hashFull[:6]

	// Check for collision, extend hash if needed
	for length := 6; length <= 8; length++ {
		id = "cart-" + hashFull[:length]
		exists, err := s.issueExists(ctx, id)
		if err != nil {
			return nil, err
		}
		if !exists {
			break
		}
		if length == 8 {
			return nil, fmt.Errorf("hash collision after 8 chars for title %q", opts.Title)
		}
	}

	issueType := opts.IssueType
	if issueType == "" {
		issueType = TypeTask
	}
	source := opts.Source
	if source == "" {
		source = "cli"
	}

	issue := &Issue{
		ID:          id,
		Title:       opts.Title,
		Description: opts.Description,
		Status:      StatusOpen,
		Priority:    opts.Priority,
		IssueType:   issueType,
		Assignee:    opts.Assignee,
		Creator:     opts.Creator,
		Source:       source,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	err := s.RunInTransaction(ctx, fmt.Sprintf("create: %s", opts.Title), func(tx *Transaction) error {
		_, err := tx.Exec(ctx, "issues",
			`INSERT INTO issues (id, title, description, status, priority, issue_type,
				assignee, creator, source, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, opts.Title, opts.Description, string(StatusOpen), opts.Priority,
			string(issueType), opts.Assignee, opts.Creator, source, now, now,
		)
		return err
	})
	if err != nil {
		return nil, err
	}
	return issue, nil
}

// Get retrieves a cart by ID.
func (s *Store) Get(ctx context.Context, id string) (*Issue, error) {
	return scanIssue(s.db.QueryRowContext(ctx,
		`SELECT id, title, description, status, priority, issue_type,
			assignee, creator, source, created_at, updated_at, closed_at
		FROM issues WHERE id = ?`, id))
}

// IssueFilter holds filter criteria for listing carts.
type IssueFilter struct {
	Status       string     // single status or comma-separated
	Assignee     string
	Priority     *int
	IssueType    string
	CreatedSince *time.Time // created_at >= since
	CreatedUntil *time.Time // created_at <= until
	Limit        int
}

// List returns carts matching the filter.
func (s *Store) List(ctx context.Context, filter IssueFilter) ([]*Issue, error) {
	var where []string
	var args []any

	if filter.Status != "" {
		statuses := strings.Split(filter.Status, ",")
		placeholders := make([]string, len(statuses))
		for i, st := range statuses {
			placeholders[i] = "?"
			args = append(args, strings.TrimSpace(st))
		}
		where = append(where, "status IN ("+strings.Join(placeholders, ",")+")")
	}
	if filter.Assignee != "" {
		where = append(where, "assignee = ?")
		args = append(args, filter.Assignee)
	}
	if filter.Priority != nil {
		where = append(where, "priority = ?")
		args = append(args, *filter.Priority)
	}
	if filter.IssueType != "" {
		where = append(where, "issue_type = ?")
		args = append(args, filter.IssueType)
	}
	if filter.CreatedSince != nil {
		where = append(where, "created_at >= ?")
		args = append(args, *filter.CreatedSince)
	}
	if filter.CreatedUntil != nil {
		where = append(where, "created_at <= ?")
		args = append(args, *filter.CreatedUntil)
	}

	query := `SELECT id, title, description, status, priority, issue_type,
		assignee, creator, source, created_at, updated_at, closed_at
	FROM issues`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ") // #nosec G202 -- where clauses use ? placeholders
	}
	query += " ORDER BY priority ASC, created_at DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	defer rows.Close()

	return scanIssues(rows)
}

// Ready returns open, unblocked carts.
func (s *Store) Ready(ctx context.Context) ([]*Issue, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, title, description, status, priority, issue_type,
			assignee, creator, source, created_at, updated_at, closed_at
		FROM ready_issues ORDER BY priority ASC, created_at DESC`)
	if err != nil {
		// Fall back to simple query if view doesn't exist
		return s.List(ctx, IssueFilter{Status: "open"})
	}
	defer rows.Close()
	return scanIssues(rows)
}

// UpdateOpts holds options for updating a cart. Nil fields are not changed.
type UpdateOpts struct {
	Title       *string
	Description *string
	IssueType   *IssueType
	Priority    *int
	Status      *Status
	Assignee    *string
}

// Update modifies a cart's fields.
func (s *Store) Update(ctx context.Context, id string, opts UpdateOpts) (*Issue, error) {
	now := time.Now().UTC()

	err := s.RunInTransaction(ctx, fmt.Sprintf("update: %s", id), func(tx *Transaction) error {
		var sets []string
		var args []any

		if opts.Title != nil {
			sets = append(sets, "title = ?")
			args = append(args, *opts.Title)
		}
		if opts.Description != nil {
			sets = append(sets, "description = ?")
			args = append(args, *opts.Description)
		}
		if opts.IssueType != nil {
			sets = append(sets, "issue_type = ?")
			args = append(args, string(*opts.IssueType))
		}
		if opts.Priority != nil {
			sets = append(sets, "priority = ?")
			args = append(args, *opts.Priority)
		}
		if opts.Status != nil {
			sets = append(sets, "status = ?")
			args = append(args, string(*opts.Status))
			if *opts.Status == StatusClosed {
				sets = append(sets, "closed_at = ?")
				args = append(args, now)
			}
		}
		if opts.Assignee != nil {
			sets = append(sets, "assignee = ?")
			args = append(args, *opts.Assignee)
		}

		if len(sets) == 0 {
			return nil
		}

		sets = append(sets, "updated_at = ?")
		args = append(args, now)
		args = append(args, id)

		_, err := tx.Exec(ctx, "issues",
			"UPDATE issues SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// CloseIssue sets a cart's status to closed.
func (s *Store) CloseIssue(ctx context.Context, id string) error {
	now := time.Now().UTC()
	return s.RunInTransaction(ctx, fmt.Sprintf("close: %s", id), func(tx *Transaction) error {
		_, err := tx.Exec(ctx, "issues",
			`UPDATE issues SET status = ?, closed_at = ?, updated_at = ? WHERE id = ?`,
			string(StatusClosed), now, now, id,
		)
		return err
	})
}

// Reopen sets a closed cart back to open.
func (s *Store) Reopen(ctx context.Context, id string) error {
	now := time.Now().UTC()
	return s.RunInTransaction(ctx, fmt.Sprintf("reopen: %s", id), func(tx *Transaction) error {
		_, err := tx.Exec(ctx, "issues",
			`UPDATE issues SET status = ?, closed_at = NULL, updated_at = ? WHERE id = ?`,
			string(StatusOpen), now, id,
		)
		return err
	})
}

// StartIssue claims a cart: sets status to in_progress and assigns it.
func (s *Store) StartIssue(ctx context.Context, id, assignee string) error {
	now := time.Now().UTC()
	return s.RunInTransaction(ctx, fmt.Sprintf("start: %s", id), func(tx *Transaction) error {
		_, err := tx.Exec(ctx, "issues",
			`UPDATE issues SET status = ?, assignee = ?, updated_at = ? WHERE id = ?`,
			string(StatusInProgress), assignee, now, id,
		)
		return err
	})
}

// DropIssue abandons a cart: sets status back to open and clears assignee.
func (s *Store) DropIssue(ctx context.Context, id string) error {
	now := time.Now().UTC()
	return s.RunInTransaction(ctx, fmt.Sprintf("drop: %s", id), func(tx *Transaction) error {
		_, err := tx.Exec(ctx, "issues",
			`UPDATE issues SET status = ?, assignee = '', updated_at = ? WHERE id = ?`,
			string(StatusOpen), now, id,
		)
		return err
	})
}

// AddDep adds a dependency between two carts.
func (s *Store) AddDep(ctx context.Context, issueID, dependsOnID string, depType DependencyType) error {
	now := time.Now().UTC()
	return s.RunInTransaction(ctx, fmt.Sprintf("dep: %s -> %s", issueID, dependsOnID), func(tx *Transaction) error {
		_, err := tx.Exec(ctx, "dependencies",
			`INSERT INTO dependencies (issue_id, depends_on_id, type, created_at)
			VALUES (?, ?, ?, ?)`,
			issueID, dependsOnID, string(depType), now,
		)
		return err
	})
}

// RemoveDep removes a dependency.
func (s *Store) RemoveDep(ctx context.Context, issueID, dependsOnID string) error {
	return s.RunInTransaction(ctx, fmt.Sprintf("dep remove: %s -> %s", issueID, dependsOnID), func(tx *Transaction) error {
		_, err := tx.Exec(ctx, "dependencies",
			`DELETE FROM dependencies WHERE issue_id = ? AND depends_on_id = ?`,
			issueID, dependsOnID,
		)
		return err
	})
}

// GetDependencies returns dependencies for a cart.
func (s *Store) GetDependencies(ctx context.Context, issueID string) ([]*Dependency, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT issue_id, depends_on_id, type, created_at
		FROM dependencies WHERE issue_id = ? ORDER BY created_at ASC`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deps []*Dependency
	for rows.Next() {
		d := &Dependency{}
		if err := rows.Scan(&d.IssueID, &d.DependsOnID, &d.Type, &d.CreatedAt); err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	return deps, rows.Err()
}

// issueExists checks if an issue with the given ID exists.
func (s *Store) issueExists(ctx context.Context, id string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", id).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// scanIssue scans a single issue from a sql.Row.
func scanIssue(row *sql.Row) (*Issue, error) {
	i := &Issue{}
	var closedAt sql.NullTime
	err := row.Scan(
		&i.ID, &i.Title, &i.Description, &i.Status, &i.Priority, &i.IssueType,
		&i.Assignee, &i.Creator, &i.Source, &i.CreatedAt, &i.UpdatedAt, &closedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan issue: %w", err)
	}
	if closedAt.Valid {
		i.ClosedAt = &closedAt.Time
	}
	return i, nil
}

// scanIssues scans multiple issues from sql.Rows.
func scanIssues(rows *sql.Rows) ([]*Issue, error) {
	var issues []*Issue
	for rows.Next() {
		i := &Issue{}
		var closedAt sql.NullTime
		err := rows.Scan(
			&i.ID, &i.Title, &i.Description, &i.Status, &i.Priority, &i.IssueType,
			&i.Assignee, &i.Creator, &i.Source, &i.CreatedAt, &i.UpdatedAt, &closedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan issue: %w", err)
		}
		if closedAt.Valid {
			i.ClosedAt = &closedAt.Time
		}
		issues = append(issues, i)
	}
	return issues, rows.Err()
}
