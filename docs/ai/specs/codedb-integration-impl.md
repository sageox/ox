<!-- doc-audience: ai -->
# `ox codedb` Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add `ox codedb` commands that wrap the `codedb` binary as a subprocess, exposing `index`, `search`, and `sql` subcommands with full flag passthrough.

**Architecture:** Thin subprocess wrapper — ox finds `codedb` in `$PATH`, forwards all arguments, streams stdout/stderr directly. No CGO, no import of CodeDBGo internals.

**Tech Stack:** Go, cobra, os/exec

**Design doc:** `docs/plans/2026-03-07-ox-codedb-design.md`

---

### Task 1: Create parent command and shared helpers (`codedb.go`)

**Files:**
- Create: `cmd/ox/codedb.go`

**Step 1: Write the test**

Create `cmd/ox/codedb_test.go` with a test that verifies `findCodeDB` returns an error when the binary is not in PATH:

```go
// cmd/ox/codedb_test.go
package main

import (
	"os"
	"testing"
)

func TestFindCodeDB_NotInPath(t *testing.T) {
	// Save original PATH and set to empty
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", "")
	defer os.Setenv("PATH", origPath)

	_, err := findCodeDB()
	if err == nil {
		t.Fatal("expected error when codedb not in PATH")
	}
	if got := err.Error(); !containsAll(got, "codedb", "not found") {
		t.Errorf("error message should mention codedb not found, got: %s", got)
	}
}

func TestFindCodeDB_InPath(t *testing.T) {
	// Only run if codedb is actually installed
	path, err := findCodeDB()
	if err != nil {
		t.Skip("codedb not installed, skipping")
	}
	if path == "" {
		t.Fatal("expected non-empty path")
	}
}

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./cmd/ox/ -run TestFindCodeDB -v -count=1`
Expected: FAIL — `findCodeDB` not defined

**Step 3: Write the implementation**

```go
// cmd/ox/codedb.go
package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
)

var codedbCmd = &cobra.Command{
	Use:   "codedb",
	Short: "Code search and indexing (powered by CodeDB)",
	Long: `Sourcegraph-style code search across git repositories.

CodeDB indexes git repos (including full commit history) and supports
full-text search, symbol extraction, and commit/diff search.

Data is stored at ~/.local/share/sageox/codedb/ (managed by codedb).

Requires the codedb binary in PATH.`,
}

func init() {
	codedbCmd.GroupID = "dev"
	codedbCmd.AddCommand(codedbIndexCmd)
	codedbCmd.AddCommand(codedbSearchCmd)
	codedbCmd.AddCommand(codedbSQLCmd)
	rootCmd.AddCommand(codedbCmd)
}

// findCodeDB locates the codedb binary in PATH.
func findCodeDB() (string, error) {
	path, err := exec.LookPath("codedb")
	if err != nil {
		return "", fmt.Errorf(
			"codedb not found in PATH.\n\n" +
				"Install CodeDB:\n" +
				"  go install github.com/sageox/CodeDBGo/cmd/codedb@latest\n\n" +
				"Or build from source:\n" +
				"  cd CodeDBGo && make build && make install")
	}
	return path, nil
}

// runCodeDB executes the codedb binary with the given arguments,
// streaming stdout/stderr to the terminal.
func runCodeDB(bin string, args []string) error {
	c := exec.Command(bin, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		// If codedb exited with a non-zero status, propagate it
		// without wrapping (the error message was already printed to stderr)
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("failed to run codedb: %w", err)
	}
	return nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./cmd/ox/ -run TestFindCodeDB -v -count=1`
Expected: PASS (or skip if codedb not installed for the InPath test)

**Step 5: Commit**

```bash
git add cmd/ox/codedb.go cmd/ox/codedb_test.go
git commit -m "feat(codedb): add parent command and binary lookup helpers"
```

---

### Task 2: Add `index` subcommand (`codedb_index.go`)

**Files:**
- Create: `cmd/ox/codedb_index.go`

**Step 1: Write the implementation**

```go
// cmd/ox/codedb_index.go
package main

import (
	"github.com/spf13/cobra"
)

var codedbIndexCmd = &cobra.Command{
	Use:                "index [url] [flags...]",
	Short:              "Index a git repository",
	Long:               "Clone and index a git repository for code search.\n\nRun `codedb index --help` for full options.",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		bin, err := findCodeDB()
		if err != nil {
			return err
		}
		return runCodeDB(bin, append([]string{"index"}, args...))
	},
}
```

**Step 2: Verify it compiles**

Run: `go build ./cmd/ox/`
Expected: SUCCESS

**Step 3: Commit**

```bash
git add cmd/ox/codedb_index.go
git commit -m "feat(codedb): add index subcommand"
```

---

### Task 3: Add `search` subcommand (`codedb_search.go`)

**Files:**
- Create: `cmd/ox/codedb_search.go`

**Step 1: Write the implementation**

```go
// cmd/ox/codedb_search.go
package main

import (
	"github.com/spf13/cobra"
)

var codedbSearchCmd = &cobra.Command{
	Use:                "search [query] [flags...]",
	Short:              "Search indexed code (Sourcegraph-style queries)",
	Long:               "Search across indexed repositories using Sourcegraph-style query syntax.\n\nRun `codedb search --help` for full options and query syntax.",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		bin, err := findCodeDB()
		if err != nil {
			return err
		}
		return runCodeDB(bin, append([]string{"search"}, args...))
	},
}
```

**Step 2: Verify it compiles**

Run: `go build ./cmd/ox/`
Expected: SUCCESS

**Step 3: Commit**

```bash
git add cmd/ox/codedb_search.go
git commit -m "feat(codedb): add search subcommand"
```

---

### Task 4: Add `sql` subcommand (`codedb_sql.go`)

**Files:**
- Create: `cmd/ox/codedb_sql.go`

**Step 1: Write the implementation**

```go
// cmd/ox/codedb_sql.go
package main

import (
	"github.com/spf13/cobra"
)

var codedbSQLCmd = &cobra.Command{
	Use:                "sql [query]",
	Short:              "Run raw SQL against the metadata database",
	Long:               "Execute SQL queries directly against the CodeDB metadata database.\n\nRun `codedb sql --help` for full options.",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		bin, err := findCodeDB()
		if err != nil {
			return err
		}
		return runCodeDB(bin, append([]string{"sql"}, args...))
	},
}
```

**Step 2: Verify it compiles**

Run: `go build ./cmd/ox/`
Expected: SUCCESS

**Step 3: Commit**

```bash
git add cmd/ox/codedb_sql.go
git commit -m "feat(codedb): add sql subcommand"
```

---

### Task 5: Run full test suite and lint

**Step 1: Run tests**

Run: `make test`
Expected: All tests pass

**Step 2: Run linter**

Run: `make lint`
Expected: No lint errors

**Step 3: Build**

Run: `make build`
Expected: Binary builds successfully

**Step 4: Manual smoke test (if codedb is installed)**

Run:
```bash
./ox-tmp codedb --help
./ox-tmp codedb index --help
./ox-tmp codedb search --help
./ox-tmp codedb sql --help
```
Expected: Help text displayed for each command

---

### Task 6: Regenerate reference docs

**Step 1: Regenerate docs from cobra definitions**

Run: `go build -o ox-tmp ./cmd/ox && ./ox-tmp docs --output docs/reference && rm ox-tmp`
Expected: Reference docs updated with new `codedb` commands

**Step 2: Commit**

```bash
git add docs/reference/
git commit -m "docs: regenerate reference docs with codedb commands"
```
