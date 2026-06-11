# CLI Friction API Specification

**Endpoint:** `POST /api/v1/cli/friction`
**Status:** Proposed
**Beads:** ox-ff29

---

## Overview

Captures CLI usage friction events (typos, unknown commands, invalid flags) to:
1. Understand where users and AI agents struggle with the CLI
2. Build a catalog of learned corrections ("did you mean?")
3. Improve CLI UX based on real usage patterns

The `/api/v1/cli/` prefix scopes this to CLI-specific UX. Future endpoints like `/api/v1/web/friction` could capture web UI friction.

---

## Request

### Endpoint

```
POST /api/v1/cli/friction
Authorization: Bearer <token>
Content-Type: application/json
```

### Headers

| Header | Required | Description |
|--------|----------|-------------|
| `Authorization` | Yes | Bearer token from OAuth flow |
| `Content-Type` | Yes | Must be `application/json` |
| `X-Client-Version` | Yes | CLI version (e.g., `0.4.0`) |
| `X-Catalog-Version` | No | Current catalog version for cache check |

### Request Body

```json
{
  "events": [
    {
      "ts": "2026-01-16T10:30:00Z",
      "kind": "unknown-command",
      "command": "agent",
      "subcommand": "",
      "actor": "agent",
      "agent_type": "claude-code",
      "path_bucket": "repo",
      "input": "ox agent prine --review",
      "error_msg": "unknown command \"prine\" for \"ox agent\"",
      "suggestion": "ox agent prime"
    }
  ]
}
```

### Event Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `ts` | string | Yes | ISO8601 timestamp (UTC) |
| `kind` | string | Yes | Failure category (see enum below) |
| `command` | string | Yes | Parent command (e.g., `"agent"`) |
| `subcommand` | string | No | Subcommand if applicable |
| `actor` | string | Yes | `"human"`, `"agent"`, or `"unknown"` |
| `agent_type` | string | No | Agent identifier if actor is agent (e.g., `"claude-code"`, `"cursor"`, `"ci"`) |
| `path_bucket` | string | Yes | Working directory category: `"home"`, `"repo"`, `"other"` |
| `input` | string | Yes | Redacted command input (secrets stripped) |
| `error_msg` | string | Yes | Redacted, truncated error message (max 200 chars) |
| `suggestion` | string | No | Client-side suggestion if found |

### Failure Kinds

| Kind | Description | Example Error |
|------|-------------|---------------|
| `unknown-command` | Command doesn't exist | `unknown command "prine" for "ox agent"` |
| `unknown-flag` | Flag doesn't exist | `unknown flag: --verbos` |
| `missing-required` | Required flag not provided | `required flag(s) "config" not set` |
| `invalid-arg` | Argument value invalid | `invalid argument "abc" for "--count"` |
| `parse-error` | General parsing failure | `accepts 1 arg(s), received 2` |

### Batch Limits

| Limit | Value |
|-------|-------|
| Max events per request | 100 |
| Max input field length | 500 chars |
| Max error_msg length | 200 chars |

---

## Response

### Success Response (200 OK)

```json
{
  "accepted": 3,
  "catalog": {
    "version": "v2026-01-16-001",
    "commands": [
      {
        "pattern": "daemons list --every",
        "target": "daemons show --all",
        "count": 127,
        "confidence": 0.95,
        "description": "Command renamed in v0.3.0"
      }
    ],
    "tokens": [
      {
        "pattern": "prine",
        "target": "prime",
        "kind": "unknown-command",
        "count": 89,
        "confidence": 0.92
      },
      {
        "pattern": "--verbos",
        "target": "--verbose",
        "kind": "unknown-flag",
        "count": 45,
        "confidence": 0.88
      }
    ]
  }
}
```

### Response Fields

| Field | Type | Description |
|-------|------|-------------|
| `accepted` | int | Number of events successfully processed |
| `catalog` | object | Optional. Included if catalog has updates or client version is stale |
| `catalog.version` | string | Catalog version identifier |
| `catalog.commands` | array | Full command remapping rules |
| `catalog.tokens` | array | Single-token correction rules |

### Catalog Inclusion Rules

Include `catalog` in response when:
- `X-Catalog-Version` header is missing
- `X-Catalog-Version` differs from current server version
- Catalog was updated since client's version

Omit `catalog` when:
- Client's catalog version matches server (saves bandwidth)

### Error Responses

| Status | Condition | Body |
|--------|-----------|------|
| 400 | Invalid JSON or schema | `{"error": "invalid_request", "message": "..."}` |
| 401 | Missing/invalid auth | `{"error": "unauthorized", "message": "..."}` |
| 413 | Too many events | `{"error": "payload_too_large", "message": "max 100 events"}` |
| 429 | Rate limited | `{"error": "rate_limited", "retry_after": 60}` |
| 500 | Server error | `{"error": "internal_error", "message": "..."}` |

---

## Catalog Data Model

### CommandMapping

Full command remapping for renamed/restructured commands:

```json
{
  "pattern": "daemons list --every",
  "target": "daemons show --all",
  "count": 127,
  "confidence": 0.95,
  "description": "Command renamed in v0.3.0"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `pattern` | string | Normalized input pattern (no `ox` prefix, flags sorted) |
| `target` | string | Correct command to suggest |
| `count` | int | Times this pattern was seen |
| `confidence` | float | 0.0-1.0, derived from count and consistency |
| `description` | string | Optional explanation for humans |

### TokenMapping

Single-token corrections (typos, aliases):

```json
{
  "pattern": "prine",
  "target": "prime",
  "kind": "unknown-command",
  "count": 89,
  "confidence": 0.92
}
```

| Field | Type | Description |
|-------|------|-------------|
| `pattern` | string | The typo/alias (lowercase) |
| `target` | string | Correct token |
| `kind` | string | Applies to this failure kind only |
| `count` | int | Times seen |
| `confidence` | float | 0.0-1.0 |

### Normalization Rules

For consistent catalog matching:

1. Strip leading `ox` from pattern
2. Lowercase all tokens
3. Sort flags alphabetically (`--b --a` → `--a --b`)
4. Collapse whitespace

Example:
```
Input:  "ox daemons list  --every --all"
Normal: "daemons list --all --every"
```

---

## Server-Side Processing

### Ingestion Pipeline

```
┌─────────────────────────────────────────────────────────────────┐
│                     Event Ingestion                             │
│  1. Validate schema                                             │
│  2. Check auth and rate limits                                  │
│  3. Write to friction_events table                              │
│  4. Enqueue for async analysis                                  │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                     Async Analysis (cron/worker)                │
│  1. Cluster similar events by normalized input                  │
│  2. Calculate confidence: count / (count + decay_factor)        │
│  3. Generate catalog entries when threshold met                 │
│  4. Publish new catalog version                                 │
└─────────────────────────────────────────────────────────────────┘
```

### Database Schema (suggested)

```sql
-- raw friction events
CREATE TABLE friction_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ts TIMESTAMPTZ NOT NULL,
    kind TEXT NOT NULL,
    command TEXT,
    subcommand TEXT,
    actor TEXT NOT NULL,
    agent_type TEXT,
    path_bucket TEXT NOT NULL,
    input_normalized TEXT NOT NULL,  -- for clustering
    input_redacted TEXT NOT NULL,    -- original redacted input
    error_msg TEXT,
    client_suggestion TEXT,
    client_version TEXT NOT NULL,
    org_id UUID,                     -- from auth token
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_friction_kind ON friction_events(kind);
CREATE INDEX idx_friction_input ON friction_events(input_normalized);
CREATE INDEX idx_friction_ts ON friction_events(ts);

-- catalog versions
CREATE TABLE friction_catalog_versions (
    version TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    commands JSONB NOT NULL,         -- []CommandMapping
    tokens JSONB NOT NULL            -- []TokenMapping
);

-- aggregated patterns for catalog generation
CREATE TABLE friction_patterns (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    pattern_normalized TEXT NOT NULL,
    kind TEXT NOT NULL,
    suggested_target TEXT,
    count INT DEFAULT 1,
    first_seen TIMESTAMPTZ DEFAULT NOW(),
    last_seen TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(pattern_normalized, kind)
);
```

### Catalog Generation Logic

```python
# pseudo-code for catalog generation
MIN_COUNT = 10          # minimum occurrences before generating entry
CONFIDENCE_DECAY = 50   # higher = slower confidence growth

def generate_catalog():
    patterns = query("""
        SELECT pattern_normalized, kind, suggested_target, count
        FROM friction_patterns
        WHERE count >= %s
        ORDER BY count DESC
    """, MIN_COUNT)

    commands = []
    tokens = []

    for p in patterns:
        confidence = p.count / (p.count + CONFIDENCE_DECAY)

        if is_multi_token(p.pattern_normalized):
            commands.append({
                "pattern": p.pattern_normalized,
                "target": p.suggested_target,
                "count": p.count,
                "confidence": round(confidence, 2)
            })
        else:
            tokens.append({
                "pattern": p.pattern_normalized,
                "target": p.suggested_target,
                "kind": p.kind,
                "count": p.count,
                "confidence": round(confidence, 2)
            })

    version = f"v{date.today().isoformat()}-{sequence}"
    store_catalog(version, commands, tokens)
    return version
```

---

## Rate Limiting

| Scope | Limit | Window |
|-------|-------|--------|
| Per org | 1000 events | 1 hour |
| Per token | 100 events | 1 minute |
| Global | 100k events | 1 hour |

Clients should batch events and send periodically (every 60s or 50 events).

---

## Privacy Considerations

### Client-Side Redaction (already implemented)

- Secrets stripped using 26+ regex patterns (API keys, tokens, passwords)
- File paths bucketed to categories (home/repo/other)
- Error messages truncated to 200 chars

### Server-Side Storage

- No PII stored
- Org ID from token for aggregate analytics only
- Events retained 90 days, then aggregated patterns only
- Individual events never exposed in UI

---

## Client Integration

### CLI Flow

```
User types: ox agent prine
                 │
                 ▼
        Cobra returns error
                 │
                 ▼
    uxfriction.Handle(args, err)
                 │
                 ▼
    Show suggestion to user
    Send event to daemon via IPC
                 │
                 ▼
    Daemon batches events
    Uploads every 60s or 50 events
                 │
                 ▼
    POST /api/v1/cli/friction
                 │
                 ▼
    If catalog updated, cache it
```

### Daemon Responsibilities

1. Receive friction events via IPC
2. Store in bounded ring buffer (max 100)
3. Upload periodically (60s) or when threshold (50) reached
4. Cache catalog response to `~/.cache/sageox/friction-catalog.json`
5. Respect rate limits (backoff on 429)

---

## Example Requests

### Minimal Request

```bash
curl -X POST https://api.sageox.ai/api/v1/cli/friction \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Client-Version: 0.4.0" \
  -d '{
    "events": [{
      "ts": "2026-01-16T10:30:00Z",
      "kind": "unknown-command",
      "command": "agent",
      "actor": "human",
      "path_bucket": "repo",
      "input": "ox agent prine",
      "error_msg": "unknown command \"prine\""
    }]
  }'
```

### With Catalog Version Check

```bash
curl -X POST https://api.sageox.ai/api/v1/cli/friction \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -H "X-Client-Version: 0.4.0" \
  -H "X-Catalog-Version: v2026-01-15-001" \
  -d '{"events": [...]}'
```

Response omits `catalog` if version matches.

---

## Migration Path

1. **Phase 1**: Server implements endpoint, returns empty catalog
2. **Phase 2**: Client integration (daemon upload, cache)
3. **Phase 3**: Server enables catalog generation from aggregated data
4. **Phase 4**: Monitoring dashboard for friction patterns

---

## Open Questions for Server Team

1. **Catalog generation frequency**: Real-time vs daily batch?
2. **Multi-tenancy**: Org-specific catalogs or global shared?
3. **Admin UI**: Dashboard for viewing friction patterns?
4. **Alerting**: Notify when new common friction pattern detected?
