package livesession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/sageox/ox/internal/fileutil"
)

// Ordinary cache cleanup (PruneSessionCache after a summary publishes, or the
// daemon's finalize) deletes a closed stream's folder, state and spool with it.
// Reopening needs both, so StashReopen keeps a copy of exactly those two in the
// ox data dir (beside the minted-conversation registry, outside any Ledger
// checkout, so it is as trusted as the registry) with its own retention.

const reopenStashDirName = "live-reopen"

// reopenRetention is how long a stopped stream stays resumable once its cache
// folder is gone. A person comes back to a session within days; a month covers
// a long weekend plus a sprint boundary without keeping every spool forever
// (each is a whole transcript, up to DefaultSpoolCap).
const reopenRetention = 30 * 24 * time.Hour

type stashedStream struct {
	// Dir is the session folder the conversation is bound to in the registry,
	// resolved; the folder itself may be gone.
	Dir   string `json:"dir"`
	State State  `json:"state"`
}

func stashDir(cfg Config) string { return filepath.Join(cfg.DataDir, reopenStashDirName) }

func stashPaths(cfg Config, cnv string) (meta, spool string) {
	base := filepath.Join(stashDir(cfg), cnv)
	return base + ".json", base + ".spool"
}

// StashReopen copies the stopped stream in dir (state and spool) to the data
// dir so cache cleanup cannot take away the next prompt's resume. It is a
// no-op for a stream that could not be reopened. Older stashes past
// reopenRetention are removed in the same call.
func StashReopen(ctx context.Context, cfg Config, dir string) error {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return err
	}
	st, err := ReadState(dir)
	if err != nil {
		return nil // no stream state: nothing to keep
	}
	if st.ManualStop {
		// a stash an earlier recording of this conversation left says the
		// conversation may resume; the person has now ended it for good
		return removeStash(cfg, st.ConversationID)
	}
	if ok, err := reopenable(ctx, cfg, dir, st); !ok || err != nil {
		return err
	}
	spool, err := readWholeSpool(dir)
	if err != nil {
		return fmt.Errorf("stash reopen: %w", err)
	}
	if err := fileutil.EnsurePrivateDir(stashDir(cfg)); err != nil {
		return fmt.Errorf("stash reopen: %w", err)
	}
	metaPath, spoolPath := stashPaths(cfg, st.ConversationID)
	// the spool first: a stash whose metadata exists always has its spool
	if err := fileutil.AtomicWriteBytes(spoolPath, spool, 0o600); err != nil {
		return fmt.Errorf("stash reopen spool: %w", err)
	}
	if err := fileutil.AtomicWriteJSON(metaPath, stashedStream{Dir: canonicalDir(dir), State: st}, 0o600); err != nil {
		return fmt.Errorf("stash reopen state: %w", err)
	}
	sweepStash(cfg)
	return nil
}

func removeStash(cfg Config, cnv string) error {
	meta, spool := stashPaths(cfg, cnv)
	for _, p := range []string{meta, spool} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove reopen stash: %w", err)
		}
	}
	return nil
}

// stashedReopenSource finds the stopped stream of recording continuedFrom in
// the stash, applying the same conditions as a stream still in its folder.
func stashedReopenSource(ctx context.Context, cfg Config, continuedFrom string) (*ReopenSource, error) {
	entries, err := os.ReadDir(stashDir(cfg))
	if err != nil {
		return nil, nil // no stash yet
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		var s stashedStream
		raw, err := os.ReadFile(filepath.Join(stashDir(cfg), e.Name()))
		if err != nil || json.Unmarshal(raw, &s) != nil || s.State.SessionID != continuedFrom {
			continue
		}
		if ok, err := reopenable(ctx, cfg, s.Dir, s.State); !ok || err != nil {
			return nil, err
		}
		_, spoolPath := stashPaths(cfg, s.State.ConversationID)
		spool, err := os.ReadFile(spoolPath)
		if err != nil {
			cfg.Logger.WarnContext(ctx, "live_session_reopen_skipped", "session_id", s.State.SessionID, "reason", "stash_spool_unreadable", "error", err)
			return nil, nil
		}
		return &ReopenSource{Dir: s.Dir, State: s.State, Spool: spool}, nil
	}
	return nil, nil
}

// retargetStash points cnv's stash, if any, at the recording that now streams
// the conversation: the next prompt names that recording as the one it
// continues, and the conversation is bound to its folder. Best effort: a
// failure only means the next resume starts a new conversation.
func retargetStash(ctx context.Context, cfg Config, cnv, sessionID, dir string) {
	metaPath, _ := stashPaths(cfg, cnv)
	raw, err := os.ReadFile(metaPath)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	var s stashedStream
	if err == nil {
		err = json.Unmarshal(raw, &s)
	}
	if err == nil {
		s.State.SessionID, s.Dir = sessionID, canonicalDir(dir)
		err = fileutil.AtomicWriteJSON(metaPath, s, 0o600)
	}
	if err != nil {
		cfg.Logger.WarnContext(ctx, "live_session_reopen_stash_retarget_failed", "conversation_id", cnv, "error", err)
	}
}

// sweepStash removes stashes untouched for reopenRetention.
func sweepStash(cfg Config) {
	entries, err := os.ReadDir(stashDir(cfg))
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < reopenRetention {
			continue
		}
		_ = os.Remove(filepath.Join(stashDir(cfg), e.Name())) // best effort: retried by the next stash
	}
}
