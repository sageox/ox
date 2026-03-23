// Package sessionsummary provides shared types, filtering, and prompt logic for
// AI coworker session summarization.
//
// This package is importable by external consumers (e.g., sageox-mono) and has
// zero dependencies outside the Go standard library. It contains:
//
//   - Response/request types for the /api/v1/session/summarize endpoint
//   - Entry filtering to reduce LLM noise (read-only tools, noise bash commands)
//   - Prompt guidelines for consistent LLM-based summarization
//   - Local fallback summary generation without LLM
//
// The ox CLI's internal/session package delegates to this package via type aliases
// and thin wrappers, keeping backward compatibility while enabling code sharing.
package sessionsummary
