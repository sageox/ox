package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/sageox/ox/internal/gitutil"
	"github.com/sageox/ox/internal/lfs"
	"github.com/sageox/ox/pkg/sessionprovenance"
)

// convergeVerifiedImport runs under the Ledger lock after a fresh remote receipt
// is verified. It never rebases privacy records: an identical journal-owned race
// gets a merge with both histories and the already-verified canonical remote tree.
func convergeVerifiedImport(ctx context.Context, ledger, remote string, d importDestination, c *importCandidate, client *lfs.Client, resuming bool) (bool, error) {
	local, err := gitutil.RunGit(ctx, ledger, "rev-parse", "HEAD")
	if err != nil {
		return false, err
	}
	if local == remote {
		return false, nil
	}
	if _, err := gitutil.RunGit(ctx, ledger, "merge-base", "--is-ancestor", local, remote); err == nil {
		if _, err := gitutil.RunGit(ctx, ledger, "merge", "--ff-only", remote); err != nil {
			return false, err
		}
		return false, nil
	}
	if !resuming {
		return false, fmt.Errorf("concurrent import needs a matching local journal")
	}
	status, err := gitutil.RunGit(ctx, ledger, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	if status != "" {
		return false, fmt.Errorf("concurrent import has pending local changes; reconciliation remains pending")
	}
	sourcePath, err := sessionprovenance.Path(c.NativeID)
	if err != nil {
		return false, err
	}
	prefix := "sessions/" + c.SessionName + "/"
	allowed := map[string]bool{sourcePath: true, prefix + "meta.json": true, prefix + "raw.jsonl": true, "sessions/.gitignore": true}
	// An append-only replacement deletes stale root artifacts atomically. Two
	// identical replacements may converge only when those paths are absent in
	// both trees; a concurrent fresh summary must remain pending instead.
	for _, name := range append(append([]string{}, lfs.ContentFiles...), "summary.json") {
		if name == "raw.jsonl" {
			continue
		}
		path := prefix + name
		_, localExists, err := importReadBlob(ctx, ledger, local, path)
		if err != nil {
			return false, err
		}
		_, remoteExists, err := importReadBlob(ctx, ledger, remote, path)
		if err != nil {
			return false, err
		}
		if !localExists && !remoteExists {
			allowed[path] = true
		}
	}
	changed, err := gitutil.RunGit(ctx, ledger, "log", "--format=", "--name-only", remote+".."+local)
	if err != nil {
		return false, err
	}
	for _, path := range strings.Split(changed, "\n") {
		if path != "" && !allowed[path] {
			return false, fmt.Errorf("concurrent import includes unrelated local history: %s", path)
		}
	}
	verified, err := verifyImportReceipt(ctx, ledger, local, d, c, client)
	if err != nil {
		return false, err
	}
	if !verified {
		return false, fmt.Errorf("local import receipt cannot be verified")
	}
	// Merely sharing a source hash is insufficient: redaction policies can produce
	// different output. Compare the actual pointer, metadata, and privacy record;
	// only the two machines' independent publication clocks may differ.
	for _, path := range []string{prefix + "meta.json", prefix + "raw.jsonl", sourcePath, "sessions/.gitignore"} {
		left, leftExists, err := importReadBlob(ctx, ledger, local, path)
		if err != nil {
			return false, err
		}
		right, rightExists, err := importReadBlob(ctx, ledger, remote, path)
		if err != nil {
			return false, err
		}
		if leftExists != rightExists {
			return false, fmt.Errorf("concurrent import has different files")
		}
		if !leftExists {
			continue
		}
		if strings.HasSuffix(path, ".json") {
			same, err := sameImportPublicationJSON(left, right, path == sourcePath)
			if err != nil {
				return false, err
			}
			if !same {
				return false, fmt.Errorf("concurrent import metadata or exclusions differ")
			}
		} else if string(left) != string(right) {
			return false, fmt.Errorf("concurrent import content differs")
		}
	}
	tree, err := gitutil.RunGit(ctx, ledger, "rev-parse", remote+"^{tree}")
	if err != nil {
		return false, err
	}
	merge, err := gitutil.RunGit(ctx, ledger, "commit-tree", tree, "-p", local, "-p", remote, "-m", "session: converge verified identical import")
	if err != nil {
		return false, err
	}
	// Two-tree read-tree refuses uncommitted/untracked collisions and retains
	// sparse-checkout semantics. It is not a reset and never invokes a resolver.
	// Moving the ref last uses CAS, retaining a concurrent writer's branch tip.
	if _, err := gitutil.RunGit(ctx, ledger, "read-tree", "-m", "-u", local, merge); err != nil {
		return false, err
	}
	if _, err := gitutil.RunGit(ctx, ledger, "update-ref", "HEAD", merge, local); err != nil {
		return false, fmt.Errorf("convergence ref changed; worktree retained for reconciliation: %w", err)
	}
	return true, nil
}

func sameImportPublicationJSON(left, right []byte, sourceRecord bool) (bool, error) {
	decode := func(data []byte) (map[string]any, error) {
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		if sourceRecord {
			delete(value, "updated_at")
		} else {
			delete(value, "published_at")
			if source, ok := value["source"].(map[string]any); ok {
				delete(source, "imported_at")
			}
		}
		return value, nil
	}
	a, err := decode(left)
	if err != nil {
		return false, err
	}
	b, err := decode(right)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(a, b), nil
}

// finishVerifiedImportConvergence publishes the merge without retrying through a
// generic rebase. A second remote writer leaves the transaction pending safely.
func finishVerifiedImportConvergence(ctx context.Context, ledger, branch, remote string, d importDestination, c *importCandidate, client *lfs.Client, resuming bool) error {
	merged, err := convergeVerifiedImport(ctx, ledger, remote, d, c, client, resuming)
	if err != nil {
		return err
	}
	if !merged {
		return nil
	}
	if _, err := gitutil.RunGit(ctx, ledger, "push", "origin", "HEAD:"+branch); err != nil {
		return fmt.Errorf("converged import push remains pending: %w", err)
	}
	_, fresh, err := refreshImportLedger(ctx, ledger)
	if err != nil {
		return err
	}
	verified, err := verifyImportReceipt(ctx, ledger, fresh, d, c, client)
	if err != nil {
		return err
	}
	if !verified {
		return fmt.Errorf("converged import receipt unavailable")
	}
	return nil
}
