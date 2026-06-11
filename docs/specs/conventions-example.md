---
audience: ai
ai_editing: allowed
refreshable: true
---

# Infrastructure Conventions

*Extracted from sageox repository analysis using 30 parallel agents*

---

## Go Project Structure

### Package Organization
- `cmd/ox/` contains executable entry points and CLI command implementations
- `internal/` houses reusable packages organized by domain: `auth/`, `config/`, `signature/`, `cli/`, `logger/`, `ui/`
- Each internal package is self-contained with exported public functions and unexported helpers

### Naming Conventions
- Type names: PascalCase reflecting purpose (`DeviceCodeResponse`, `ProjectConfig`)
- Constants: UPPER_SNAKE_CASE for module-level values (`DefaultAPIURL`, `ClientID`)
- Struct tags: lowercase snake_case JSON fields with `omitempty` for optional

### Error Handling
- Consistent `fmt.Errorf("descriptive message: %w", err)` for error wrapping
- Custom error types with semantic meaning: `TokenRefreshError`, `APIError`, `DeviceAuthError`
- Error messages: imperative style starting with action verbs ("failed to X", "access denied")

---

## CLI Framework (Cobra)

### Command Structure
- Commands grouped into semantic categories: "project", "auth", "diagnostics"
- Three-tier help text: `Short` (one-line), `Long` (detailed), examples in Long
- Silent error/usage handling for custom error presentation

### Flag Conventions
- Global flags: `-v/--verbose`, `-q/--quiet`, `--json`, `-c/--config`
- Flag names: kebab-case (`project-id`, `team-id`)
- Short flags for common operations only

### Custom Styling
- Brand colors: sage green (#6B8E6B primary), warm gold (#E8B563 secondary)
- Lipgloss for automatic terminal capability detection
- NO_COLOR environment variable respected automatically

---

## Configuration Management

### Dual-Level Architecture
- **Project config**: `.sageox/config.json` (repo root, committed to git)
- **User config**: `~/.config/sageox/` via XDG standard (auth tokens, machine identity)

### Environment Variables
- `OX_*` prefix for CLI settings
- `FEATURE_*` prefix for feature flags (accept "true"/"1"/"yes")
- `SAGEOX_API` for endpoint override

### Defaults Pattern
- Viper-based loading with `SetEnvPrefix()` + `AutomaticEnv()`
- Missing config files are non-fatal (graceful defaults)
- Helper functions for default construction: `GetDefaultProjectConfig()`

---

## Authentication

### OAuth 2.0 Device Flow
- RFC 8628 implementation with polling mechanism
- Exponential backoff (5-second minimum, increased on `slow_down`)
- User info fetched via `/oauth2/userinfo` with access token

### Token Storage
- Atomic writes (temp file + rename) with 0600 permissions
- `StoredToken` preserves `UserInfo` across refreshes
- XDG Base Directory compliance for cross-platform support

### Token Refresh Strategy
- Proactive: refresh tokens expiring within 5 minutes before requests
- Reactive: on 401 response, attempt refresh and retry original request
- Best-effort revocation: always remove local storage regardless of server response

---

## Security Patterns

### Credential Storage
- 0600 permissions for sensitive files (tokens, machine IDs, caches)
- 0700 for config directories
- Atomic writes prevent partial/corrupted files

### Signature Verification
- Ed25519 signatures with key rotation support (indexed by key ID)
- HMAC-SHA256 cache tamper detection using machine ID as key
- Invalid signatures trigger tamper warnings

### Secret Handling
- Never log credentials
- Graceful nil-return when credentials don't exist
- Doctor command validates and auto-fixes permission issues

---

## API Client Patterns

### HTTP Configuration
- Primary requests: 30-second timeout
- Token refresh: 30-second timeout
- Revocation/device flow: 10-second timeout

### Error Handling
- Custom error types: `APIError`, `AuthenticationError`
- Status-specific handling: 401 triggers refresh, 403 parses detailed errors
- Offline resilience: preserve local cache on network failure

### Request/Response
- Automatic JSON marshaling with Bearer token injection
- User-Agent headers included
- Graceful nil fallback on parse errors

---

## Testing Patterns

### Organization
- Test files co-located with implementation (`*_test.go`)
- Table-driven tests with `t.Run()` subtests
- Helper functions: `setupTestDir()`, `setupTestEnv()`, `createTestToken()`

### Mocking
- `httptest.NewServer()` for HTTP endpoint mocking
- Mock servers verify request details and return controlled responses
- Cleanup via `t.Cleanup()` and `t.TempDir()`

### Coverage
- Comprehensive edge cases: corrupted files, empty files, invalid inputs
- Permission errors, timeout scenarios, state transitions
- Cache hit/miss and tampering detection

---

## CI/CD (GitHub Actions)

### Workflow Structure
- Separate CI and release pipelines
- CI: build + lint in parallel on push/PR to main
- Release: triggered on `v*` tags with concurrency control

### Build Configuration
- Multi-platform: Linux amd64/arm64, macOS Intel/ARM, Windows amd64
- CGO_ENABLED=0 for static binaries
- Version metadata via ldflags

### Release Automation
- GoReleaser with conventional commit filtering
- Auto-categorize into Features/Bug Fixes
- SHA256 checksums, tar.gz + zip archives

---

## Logging

### Implementation
- Go standard library `log/slog` with wrapper layer
- Environment-based format: JSON in production, text in development
- Output to stderr

### Level Usage
- Debug: internal state, decision points, file paths
- Warn: user-facing security concerns only
- User messages: direct `fmt.Fprintf()` instead of logger

### Format
- Single-line for grepability
- Variadic args with format strings
- Never multiline log statements

---

## Versioning

### Semantic Versioning
- Keep a Changelog format (https://keepachangelog.com)
- Version tags: `v0.0.x` in git
- Categories: Added, Changed, Fixed

### Build-time Injection
- Makefile uses `git describe --tags` with fallback
- ldflags inject: version, buildDate, gitCommit
- `ox version` returns comprehensive metadata

### Release Notes
- Embedded via `//go:embed` directive
- `--latest` flag extracts most recent version section
- Dynamic parsing of markdown hierarchy

---

## Feature Flags

### Naming Convention
- `FEATURE_*` environment variable prefix
- Case-insensitive values: "true", "1", "yes"

### Implementation
- Centralized functions: `IsAuthRequired()`, `IsCloudEnabled()`
- Graceful degradation when disabled
- Visible in `ox status` output

---

## File/Path Handling

### XDG Compliance
- `os.UserConfigDir()` for cross-platform resolution
- Platform-specific subdirectories respected
- Fallback logic for IDE detection

### Path Construction
- Always use `filepath.Join()` (never string concatenation)
- Check with `os.IsNotExist()` before error handling
- Create directories on-demand with `os.MkdirAll()`

### Atomic Writes
- Temp file pattern: `path + ".tmp"` then `os.Rename()`
- Cleanup on failure
- Applied to sensitive files consistently

---

## TUI/Interactive Patterns

### Bubbletea Architecture
- Separate `tea.Model` for each step (not one complex model)
- Chain multiple programs sequentially
- Uniform cancellation handling with `m.quitting` state

### Styling
- Semantic color mapping: success (green), warning (gold), error (red), muted (gray)
- Status icons: ✓ (success), ✗ (error), ⚠ (warning), - (skip)
- JSON mode bypass for machine consumption

### Progress Tracking
- Spinner model for long-running operations
- Incremental output with status icons
- Summary aggregation after completion

---

## Git Integration

### Repository Detection
- `git rev-parse --show-toplevel` for root discovery
- Fallback to current directory if not in git repo
- `.git` directory check for lightweight verification

### Status Checking
- `git status --porcelain` for machine-readable parsing
- Check specific directories (e.g., `.sageox/`)

### File Staging
- Explicit file paths (no wildcards)
- Graceful warnings on failure (don't block initialization)

---

## Documentation Standards

### README Structure
- Problem-focused opening with value proposition
- Visual demo, quick start, installation
- Commands as markdown table

### CHANGELOG Format
- Keep a Changelog v1.1.0 specification
- Categorized subsections: Added, Changed, Fixed
- Version links to GitHub releases

### Code Comments
- Single-line sentence comments
- Lowercase start for inline comments
- Explain "why" not "what"

---

## Diagnostic Patterns (Doctor Command)

### Health Check Structure
- Three-state model: passed, warning, skipped
- Hierarchical organization by category
- Parent-child relationships for nested checks

### Auto-Fix Capability
- `--fix` flag for automatic remediation
- Report action taken, not just problem
- Preserve user data during fixes

### Reporting
- Brief status + optional detail string
- Tree decorators for visual hierarchy
- Summary statistics: passed, warnings, failures

---

*Generated by `ox learn` analyzing 40+ categories in parallel*
