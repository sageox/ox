package carts

const currentSchemaVersion = 2

// schema defines the database tables. Two tables: issues + dependencies.
const schema = `
CREATE TABLE IF NOT EXISTS issues (
    id VARCHAR(255) PRIMARY KEY,
    title VARCHAR(500) NOT NULL,
    description TEXT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'open',
    priority INT NOT NULL DEFAULT 2,
    issue_type VARCHAR(32) NOT NULL DEFAULT 'task',
    assignee VARCHAR(255) DEFAULT '',
    creator VARCHAR(255) DEFAULT '',
    source VARCHAR(32) NOT NULL DEFAULT 'cli',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    closed_at DATETIME,
    INDEX idx_issues_status (status),
    INDEX idx_issues_priority (priority)
);

CREATE TABLE IF NOT EXISTS dependencies (
    issue_id VARCHAR(255) NOT NULL,
    depends_on_id VARCHAR(255) NOT NULL,
    type VARCHAR(32) NOT NULL DEFAULT 'blocks',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (issue_id, depends_on_id),
    CONSTRAINT fk_dep_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS metadata (
    ` + "`key`" + ` VARCHAR(255) PRIMARY KEY,
    value TEXT NOT NULL
);
`

// readyIssuesView: open issues not blocked by any open dependency.
const readyIssuesView = `
CREATE OR REPLACE VIEW ready_issues AS
SELECT i.*
FROM issues i
WHERE i.status = 'open'
  AND NOT EXISTS (
    SELECT 1 FROM dependencies d
    JOIN issues blocker ON blocker.id = d.depends_on_id
    WHERE d.issue_id = i.id
      AND d.type = 'blocks'
      AND blocker.status != 'closed'
  );
`
