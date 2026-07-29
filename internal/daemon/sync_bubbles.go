package daemon

// Knowledge-bubble sync loop. Daemon-side reader: fetches the bubbles
// for the project's ambient scopes from /api/v1/kb (the list endpoint is
// scoped per sageox-mono ADR-073 — one scope per call, members-only) and
// pulls each one into its canonical XDG path (paths.KBDir(kb_id)). Per
// the daemon-CLI split (.claude/rules/daemon-git.md) this only ever
// pulls — never adds, commits, or pushes. Symlink management (D2) and
// GC (D3) live in separate beads and intentionally NOT here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/kb"
	"github.com/sageox/ox/internal/manifest"
	"github.com/sageox/ox/internal/paths"
)

// kbCloneInFlight is a same-process marker used by GC triage as a cheap
// pre-check before attempting the cross-process kb flock. The flock
// (AcquireKBLock) is what actually serializes concurrent reconcileBubble
// passes — both within one daemon and across the N per-repo daemons
// that share the canonical paths.KBDir(kb_id) working tree. The marker
// is kept because (a) it makes GC's "skip clones in flight" branch a
// constant-time map lookup before the syscall, and (b) several existing
// tests assert on it. Without the flock, two daemons can race into
// cloneBubble on the same path and corrupt the working tree; the marker
// alone is useless across processes. See bead ox-kdt2.
var kbCloneInFlight sync.Map // map[kb_id]struct{}

// markKBCloneStart records that a clone for kb_id is in progress.
func markKBCloneStart(kbID string) { kbCloneInFlight.Store(kbID, struct{}{}) }

// markKBCloneDone clears the in-flight marker for kb_id.
func markKBCloneDone(kbID string) { kbCloneInFlight.Delete(kbID) }

// kbCloneActive reports whether kb_id is currently being cloned.
func kbCloneActive(kbID string) bool {
	_, ok := kbCloneInFlight.Load(kbID)
	return ok
}

// envDisableKBDaemon mirrors internal/kb's OX_KB_DISABLE escape hatch on
// the daemon side. When set to a truthy value (anything but empty / "0" /
// "false" / "no" / "off") the kb sync loop is a no-op. Intentionally
// duplicated rather than exported from internal/kb to keep that package's
// dependency surface minimal.
const envDisableKBDaemon = "OX_KB_DISABLE"

func kbDaemonDisabledByEnv() bool {
	v := strings.TrimSpace(os.Getenv(envDisableKBDaemon))
	if v == "" {
		return false
	}
	switch strings.ToLower(v) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// KBBubbleLister is the minimal surface syncBubbles needs from the kb API
// client. The list endpoint requires an explicit scope per call (ADR-073),
// so the method signature carries one — it matches *api.KBClient exactly.
// Defining it locally (rather than importing *api.KBClient directly) keeps
// the daemon test-injectable without dragging in real HTTP plumbing —
// sync_bubbles_test.go swaps in a fake that returns canned rows or errors.
type KBBubbleLister interface {
	ListBubbles(ctx context.Context, scope api.KBScope) ([]api.KB, error)
}

// kbBubbleListerFactory returns a lister bound to the given endpoint and
// auth token. Production wires this to api.NewKBClientWithEndpoint(...);
// tests override via SetKBBubbleListerFactory to inject a fake. The
// factory shape (ep, token) -> lister mirrors how RepoClient is
// constructed elsewhere in the daemon and lets each invocation pick up
// freshly-rotated credentials.
type kbBubbleListerFactory func(endpointURL, authToken string) KBBubbleLister

// SetKBBubbleListerFactory overrides the kb API client constructor used
// by syncBubbles. Tests use this; production leaves it nil so the
// default factory (real HTTP) is used.
func (s *SyncScheduler) SetKBBubbleListerFactory(f kbBubbleListerFactory) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kbListerFactory = f
}

// kbMeta is the on-disk shape for <bubble>/.sageox/meta.json. Holds the
// fields the daemon learns from the kb API plus a last_sync stamp so
// `ox doctor` and friends can detect stale clones without a roundtrip.
// Kept narrow on purpose — anything heavier (file index, token counts)
// belongs in the cache dir, not in the source-of-truth meta.
type kbMeta struct {
	Type        api.KBType `json:"type"`
	Slug        string     `json:"slug"`
	Name        string     `json:"name,omitempty"`
	ScopeType   string     `json:"scope_type,omitempty"`
	ScopeID     string     `json:"scope_id,omitempty"`
	Description string     `json:"description,omitempty"`
	Topics      []string   `json:"topics,omitempty"`
	OwnerUserID string     `json:"owner_user_id,omitempty"`
	ViewerRole  string     `json:"viewer_role,omitempty"`
	LastSync    time.Time  `json:"last_sync"`
}

// syncBubbles is the periodic reconciliation pass for knowledge bubbles.
// It is the daemon-side counterpart to `ox kb list`: fetch the project's
// ambient-scope bubbles and ensure each one is cloned + up-to-date in its
// canonical XDG location (paths.KBDir(kb_id)).
//
// Scope derivation: the list endpoint requires one scope per call
// (ADR-073), and the scopes a project implies come from
// kb.AmbientScopes(teamID) — v1 is the project's team only. The team id
// comes from the workspace registry (populated from the project's
// config.json); no team binding means there is nothing to list.
//
// Failure isolation: every per-bubble step is wrapped so one bad bubble
// (bad URL, fetch failure, permissions) does not abort the rest of the
// pass. Per-scope, a 403/404 (api.ErrKBAPIUnavailable) means "feature
// flag off / non-member" and is skipped silently; other errors are
// warned and the remaining scopes still run. The scoped fetch here
// mirrors kb.FetchBubbles (internal/kb/bubbles.go) so the daemon and
// the CLI surfaces can't disagree about union/dedup semantics.
//
// What this loop deliberately does NOT do:
//   - Symlink management (D2 / ox-gzp.5) — canonical XDG path only.
//   - GC of orphaned bubble dirs (D3 / ox-gzp.6) — never deletes.
//   - LFS hydration via shell-out — banned by .claude/rules/lfs-no-git-lfs-binary.md.
func (s *SyncScheduler) syncBubbles(ctx context.Context) {
	if s.config.ProjectRoot == "" {
		// no project root means we have no way to resolve the endpoint
		// or fetch credentials — same posture as pullTeamContexts.
		return
	}

	// Escape hatch from the kb plan: OX_KB_DISABLE=1 takes the daemon's
	// kb sync loop offline entirely. Mirrors the client-side short-circuit
	// in internal/kb/bubbles.go so operators have one consistent switch to
	// flip during a rollout incident.
	if kbDaemonDisabledByEnv() {
		s.logger.Debug("kb_sync skipped: OX_KB_DISABLE set")
		return
	}

	scopes := kb.AmbientScopes(s.projectTeamIDForKB())
	if len(scopes) == 0 {
		s.logger.Debug("kb_sync skipped: no team scope")
		return
	}

	lister := s.buildKBLister()
	if lister == nil {
		s.logger.Debug("kb_sync skipped: no lister configured")
		return
	}

	// bound the entire list fan-out; per-bubble git ops below get their
	// own bounds and we don't want a slow API to wedge the scheduler tick.
	listCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// fan out over the ambient scopes and union the rows, deduped by
	// kb_id (a bubble visible via two scopes must reconcile once).
	var bubbles []api.KB
	seen := make(map[string]bool)
	for _, scope := range scopes {
		rows, err := lister.ListBubbles(listCtx, scope)
		if err != nil {
			if errors.Is(err, api.ErrKBAPIUnavailable) {
				// 403/404 — flag-gated, non-member, or endpoint missing.
				// Silent skip per the sentinel doc on api.ErrKBAPIUnavailable.
				s.logger.Debug("kb_sync scope unavailable, skipping",
					"scope_type", scope.Type, "scope_id", scope.ID, "error", err)
				continue
			}
			s.logger.Warn("kb_sync list failed for scope",
				"scope_type", scope.Type, "scope_id", scope.ID, "error", err)
			continue
		}
		for _, r := range rows {
			if r.KBID != "" && seen[r.KBID] {
				continue
			}
			if r.KBID != "" {
				seen[r.KBID] = true
			}
			bubbles = append(bubbles, r)
		}
	}

	if len(bubbles) == 0 {
		s.logger.Debug("kb_sync: no bubbles for caller")
		return
	}

	s.logger.Debug("kb_sync starting", "count", len(bubbles))

	for _, b := range bubbles {
		// per-bubble error containment — never let one row abort the pass.
		s.reconcileBubble(ctx, b)
	}

	// after every bubble has been cloned/pulled into its canonical XDG
	// path, refresh this project's ergonomic symlinks so editors and AI
	// coworkers can see them at <project>/.sageox/kb/team/<slug>. The
	// reconciler is idempotent and safe to call when nothing changed.
	s.reconcileOwnProjectSymlinks(ctx, bubbles)
}

// projectTeamIDForKB resolves the project's primary team id from the
// workspace registry, refreshing the registry's config cache first so a
// team binding written after daemon startup is picked up. Returns ""
// when the daemon has no registry or the project has no team binding.
func (s *SyncScheduler) projectTeamIDForKB() string {
	if s.workspaceRegistry == nil {
		return ""
	}
	// best-effort refresh (30s-cached internally); a load failure just
	// means we fall back to whatever the registry already knows.
	_ = s.workspaceRegistry.LoadFromConfig()
	return s.workspaceRegistry.ProjectTeamID()
}

// kbEndpoint resolves the endpoint that scopes this daemon's KB store
// paths (paths.KBDir / trash / symlink targets). Project-bound: a
// test.sageox.ai project's bubbles must land under the test.sageox.ai
// slug, never the production default (see bead ox-651l).
func (s *SyncScheduler) kbEndpoint() string {
	return endpoint.GetForProject(s.config.ProjectRoot)
}

// buildKBLister resolves a KBBubbleLister using the test factory if set,
// otherwise constructs a real api.KBClient bound to the project's
// endpoint and the heartbeat-cached auth token.
func (s *SyncScheduler) buildKBLister() KBBubbleLister {
	s.mu.Lock()
	factory := s.kbListerFactory
	tokenGetter := s.getAuthToken
	s.mu.Unlock()

	ep := endpoint.GetForProject(s.config.ProjectRoot)
	var token string
	if tokenGetter != nil {
		token = tokenGetter()
	}

	if factory != nil {
		return factory(ep, token)
	}
	if ep == "" {
		return nil
	}
	client := api.NewKBClientWithEndpoint(ep)
	if token != "" {
		client = client.WithAuthToken(token)
	}
	return client
}

// reconcileBubble brings a single bubble to its expected on-disk state:
// clone if absent, pull if present, then write/update meta.json. All
// errors are logged with a type=<kb_type> tag so heartbeat dashboards
// can group token usage by bubble kind (D4).
func (s *SyncScheduler) reconcileBubble(ctx context.Context, b api.KB) {
	if b.KBID == "" {
		s.logger.Warn("kb_sync skipping bubble with empty kb_id", "slug", b.Slug, "type", string(b.KBType))
		return
	}

	target := paths.KBDir(s.kbEndpoint(), b.KBID)
	if target == "" {
		s.logger.Warn("kb_sync target path empty, skipping", "kb_id", b.KBID, "type", string(b.KBType))
		return
	}

	// Serialize concurrent reconciles for the same kb_id across BOTH the
	// in-process goroutine case AND the N-per-host daemon case. POSIX
	// flock semantics give us both for the price of one syscall — a
	// second LOCK_EX|LOCK_NB on the same file (different fd, same or
	// different process) returns EWOULDBLOCK. If another reconciler
	// already owns the lock we skip this pass entirely; the next tick
	// will retry. Lock auto-releases on process death via the kernel
	// (no stale-lock recovery code needed). See bead ox-kdt2.
	unlock, acquired, lockErr := AcquireKBLock(b.KBID)
	if lockErr != nil {
		s.logger.Warn("kb_sync lock acquire failed", "kb_id", b.KBID, "type", string(b.KBType), "error", lockErr)
		return
	}
	if !acquired {
		s.logger.Debug("kb_sync skip: another reconciler holds kb lock", "kb_id", b.KBID, "type", string(b.KBType))
		return
	}
	defer unlock()

	// existence is determined by .git/ — a bare directory without it is
	// either a partial clone we should redo, or noise. We keep it simple
	// and treat "no .git" as "clone needed" so a manually-deleted .git
	// will heal next pass.
	gitDir := filepath.Join(target, ".git")
	if _, statErr := os.Stat(gitDir); os.IsNotExist(statErr) {
		if err := s.cloneBubble(ctx, b, target); err != nil {
			s.logger.Warn("kb_sync clone failed", "kb_id", b.KBID, "type", string(b.KBType), "slug", b.Slug, "error", err)
			return
		}
		s.logger.Info("kb_sync cloned", "kb_id", b.KBID, "type", string(b.KBType), "slug", b.Slug, "path", target)
	} else if statErr != nil {
		s.logger.Warn("kb_sync stat failed", "kb_id", b.KBID, "type", string(b.KBType), "error", statErr)
		return
	} else {
		// read manifest before pull to get auto-resolve prefixes. The
		// manifest is already on disk from clone (TwoPhaseClone materializes
		// .sageox/ in phase 1). If missing/unparseable, ParseFile returns
		// FallbackConfig which already includes DefaultResolveRules.
		manifestPath := filepath.Join(target, ".sageox", "sync.manifest")
		mCfg := manifest.ParseFile(manifestPath)

		// existing clone — pull via the shared managed-repo pipeline so we
		// reuse fetch dedup, lock-file cleanup, kb merge=union pre-flight,
		// auto-resolve, and divergence detection.
		result := s.pullManagedRepo(ctx, ManagedRepoPullOpts{
			RepoPath:           target,
			RepoName:           fmt.Sprintf("kb/%s", b.Slug),
			ProjectRoot:        s.config.ProjectRoot,
			SyncInterval:       intervalForType(s.config, string(b.KBType)),
			ValidateIntegrity:  true,
			DetectDivergence:   true,
			ResolveRules:       mCfg.ResolveRules,
			EnsureKBMergeAttrs: true,
			Logger:             s.logger,
		})
		switch {
		case result.CorruptRepo:
			// move-aside so the next pass treats it as a fresh clone.
			backup := fmt.Sprintf("%s.bak.%d", target, time.Now().Unix())
			if renameErr := os.Rename(target, backup); renameErr != nil {
				s.logger.Warn("kb_sync corrupt repo rename failed", "kb_id", b.KBID, "type", string(b.KBType), "error", renameErr)
				return
			}
			s.logger.Info("kb_sync corrupt repo moved aside", "kb_id", b.KBID, "type", string(b.KBType), "backup", backup)
			return
		case result.Err != nil:
			s.logger.Warn("kb_sync pull failed", "kb_id", b.KBID, "type", string(b.KBType), "slug", b.Slug, "error", result.Err)
			return
		case result.Skipped:
			s.logger.Debug("kb_sync pull skipped", "kb_id", b.KBID, "type", string(b.KBType), "reason", result.SkipReason)
		default:
			s.logger.Info("kb_sync pulled", "kb_id", b.KBID, "type", string(b.KBType), "slug", b.Slug)
		}

		// Reapply sparse-checkout from the (possibly updated) manifest. We
		// re-parse here AFTER the pull because the pre-pull mCfg was the
		// pre-pull manifest; if the server pushed new include directives,
		// they're only on disk now. Without this re-parse + reapply, kb
		// would silently drift from team-context sync behavior, which
		// reapplies sparse on every pull.
		postPullCfg := manifest.ParseFile(manifestPath)
		_ = applySparseFromManifest(ctx, target, postPullCfg, s.logger)
	}

	// shared kb merge=union rules — idempotent, safe to call after both
	// clone and pull. cloneBubble may not have run pullManagedRepo, so we
	// always reapply here as a belt-and-suspenders.
	if _, err := kb.EnsureMergeAttributes(target); err != nil {
		s.logger.Warn("kb_sync ensure merge attrs failed", "kb_id", b.KBID, "type", string(b.KBType), "error", err)
		// non-fatal: we still write meta.json
	}

	if err := writeKBMeta(target, b); err != nil {
		s.logger.Warn("kb_sync meta write failed", "kb_id", b.KBID, "type", string(b.KBType), "error", err)
		return
	}
}

// cloneBubble performs the initial clone of a bubble into the canonical
// XDG path via gitserver.TwoPhaseClone — the same shallow + blob:none +
// sparse-checkout pipeline team-contexts use. This unlocks manifest-driven
// sparse sets, depth-1 history, and EnsureCheckoutGitignore as part of the
// clone itself. NEVER shells out to git-lfs (per
// .claude/rules/lfs-no-git-lfs-binary.md); TwoPhaseClone calls
// gitutil.StripLFSConfig to disable any smudge filter git-lfs may have
// injected during the clone.
func (s *SyncScheduler) cloneBubble(ctx context.Context, b api.KB, target string) error {
	// prefer the server-supplied full URL (older response shapes); the
	// current server sends git_path instead, from which we derive
	// https://git.<endpoint-host>/<path>.git.
	cloneURL := b.RepoURL
	if cloneURL == "" && b.GitPath != "" {
		cloneURL = kb.GitCloneURL(endpoint.GetForProject(s.config.ProjectRoot), b.GitPath)
	}
	if cloneURL == "" {
		return fmt.Errorf("bubble %s (%s) has no repo url/git path; server may not have provisioned it yet", b.KBID, b.Slug)
	}

	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create kb parent dir: %w", err)
	}

	// Mark this kb_id as having an active clone so GC triage skips it
	// (otherwise a transient API-list hiccup mid-clone could rename the
	// half-populated target into .trash/ — silent corruption).
	markKBCloneStart(b.KBID)
	defer markKBCloneDone(b.KBID)

	cloneCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if _, err := gitserver.TwoPhaseClone(cloneCtx, cloneURL, target); err != nil {
		return fmt.Errorf("kb two-phase clone failed: %w", err)
	}

	// install kb merge=union rules immediately so the very first pull
	// (next reconcile pass) doesn't wedge on a concurrent root-metadata
	// edit. EnsureMergeAttributes is idempotent.
	if _, err := kb.EnsureMergeAttributes(target); err != nil {
		// non-fatal — degraded mode, like the team-context path.
		s.logger.Warn("kb_sync post-clone ensure merge attrs failed", "kb_id", b.KBID, "type", string(b.KBType), "error", err)
	}

	// belt-and-suspenders: TwoPhaseClone already calls EnsureCheckoutGitignoreCtx
	// internally, but we re-call it explicitly to mirror the explicit-is-better
	// posture from sync_gc.go. Idempotent — no-ops when entries are already
	// present.
	if err := gitserver.EnsureCheckoutGitignoreCtx(ctx, target); err != nil {
		s.logger.Warn("kb_sync post-clone ensure checkout gitignore failed", "kb_id", b.KBID, "type", string(b.KBType), "error", err)
	}
	return nil
}

// writeKBMeta serializes the bubble's metadata to <target>/.sageox/meta.json.
// Atomic write (tmp + rename) so a crash mid-write never leaves a
// truncated file that downstream readers (status, doctor) would fail on.
func writeKBMeta(target string, b api.KB) error {
	metaDir := filepath.Join(target, ".sageox")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return fmt.Errorf("create .sageox dir: %w", err)
	}
	meta := kbMeta{
		Type:        b.KBType,
		Slug:        b.Slug,
		Name:        b.Name,
		ScopeType:   b.ScopeType,
		ScopeID:     b.ScopeID,
		Description: b.Description,
		Topics:      b.Topics,
		OwnerUserID: b.OwnerUserID,
		ViewerRole:  b.ViewerRole,
		LastSync:    time.Now().UTC(),
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal kb meta: %w", err)
	}
	finalPath := filepath.Join(metaDir, "meta.json")
	tmp, err := os.CreateTemp(metaDir, "meta.json.tmp-*")
	if err != nil {
		return fmt.Errorf("create temp meta: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp meta: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp meta: %w", err)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		return fmt.Errorf("rename meta: %w", err)
	}
	return nil
}
