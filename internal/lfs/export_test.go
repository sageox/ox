package lfs

import (
	"context"
	"log/slog"
	"testing"
)

// The external lfs_test package drives the push repair together with
// internal/plan, which imports lfs, so tests inside package lfs cannot.

// ReconcileWithClient runs the push repair against client instead of building
// one from stored credentials.
func ReconcileWithClient(ctx context.Context, ledgerPath string, client *Client, logger *slog.Logger) (*ReconcileResult, error) {
	return reconcileUnpushedPointers(ctx, ledgerPath, logger, func() (*Client, error) { return client, nil })
}

// UseBundledSessionRules pins the secret-guard catalog to the bundled rules.
func UseBundledSessionRules(t *testing.T) { useBundledSessionRules(t) }
