package livesession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

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

// staleTempAge is how old an atomic-write temp file must be before sweepStash
// treats it as abandoned.
const staleTempAge = time.Hour

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

// ErrInvalidConversationID is returned for a conversation id that is not a
// canonical cnv_<lowercase uuid>.
var ErrInvalidConversationID = errors.New("invalid conversation id")

// safeStashPaths is stashPaths for ids that may come from untrusted state (a
// stream-state.json in a Ledger checkout or a stash file is user-writable). An
// id like "../live-conversations" would otherwise make the remove below delete
// files outside the stash dir, so only the canonical form is accepted, and the
// result is checked to stay directly inside the stash dir as defense in depth.
func safeStashPaths(cfg Config, cnv string) (meta, spool string, err error) {
	suffix, ok := strings.CutPrefix(cnv, "cnv_")
	if !ok {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidConversationID, cnv)
	}
	if u, perr := uuid.Parse(suffix); perr != nil || u.String() != suffix {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidConversationID, cnv)
	}
	meta, spool = stashPaths(cfg, cnv)
	dir := filepath.Clean(stashDir(cfg))
	for _, p := range []string{meta, spool} {
		if filepath.Dir(filepath.Clean(p)) != dir {
			return "", "", fmt.Errorf("%w: %q escapes the stash dir", ErrInvalidConversationID, cnv)
		}
	}
	return meta, spool, nil
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
	metaPath, spoolPath, err := safeStashPaths(cfg, st.ConversationID)
	if err != nil {
		return fmt.Errorf("stash reopen: %w", err)
	}
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
	meta, spool, err := safeStashPaths(cfg, cnv)
	if err != nil {
		return fmt.Errorf("remove reopen stash: %w", err)
	}
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
	// Several stashes can name the recording (a refused reopen leaves its
	// conversation's stash tagged with the replacement's id until it is
	// removed); the newest one is the conversation the person last worked in.
	var best *stashedStream
	var bestTime time.Time
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		var s stashedStream
		raw, err := os.ReadFile(filepath.Join(stashDir(cfg), e.Name()))
		if err != nil || json.Unmarshal(raw, &s) != nil || s.State.SessionID != continuedFrom {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if best == nil || info.ModTime().After(bestTime) {
			best, bestTime = &s, info.ModTime()
		}
	}
	if best == nil {
		return nil, nil
	}
	if ok, err := reopenable(ctx, cfg, best.Dir, best.State); !ok || err != nil {
		return nil, err
	}
	_, spoolPath, err := safeStashPaths(cfg, best.State.ConversationID)
	if err != nil {
		cfg.Logger.WarnContext(ctx, "live_session_reopen_skipped", "session_id", best.State.SessionID, "reason", "stash_conversation_id_invalid", "error", err)
		return nil, nil
	}
	spool, err := os.ReadFile(spoolPath)
	if err != nil {
		cfg.Logger.WarnContext(ctx, "live_session_reopen_skipped", "session_id", best.State.SessionID, "reason", "stash_spool_unreadable", "error", err)
		return nil, nil
	}
	return &ReopenSource{Dir: best.Dir, State: best.State, Spool: spool}, nil
}

// retargetStash points cnv's stash, if any, at the recording that now streams
// the conversation: the next prompt names that recording as the one it
// continues, and the conversation is bound to its folder. Best effort: a
// failure only means the next resume starts a new conversation.
func retargetStash(ctx context.Context, cfg Config, cnv, sessionID, dir string) {
	metaPath, _, err := safeStashPaths(cfg, cnv)
	if err != nil {
		cfg.Logger.WarnContext(ctx, "live_session_reopen_stash_retarget_failed", "conversation_id", cnv, "error", err)
		return
	}
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

// sweepStash removes stashes untouched for reopenRetention. A stash is a pair
// (metadata and spool) with one activity time, the metadata's: a resume that
// retargets it refreshes that time, and the spool is removed with it, never
// before. A spool whose metadata is gone is an orphan and goes by its own age.
func sweepStash(cfg Config) {
	entries, err := os.ReadDir(stashDir(cfg))
	if err != nil {
		return
	}
	fresh := map[string]bool{} // cnv -> metadata is within retention
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) < reopenRetention {
			fresh[strings.TrimSuffix(e.Name(), ".json")] = true
		}
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".tmp-") {
			// another writer's in-flight atomic write: removing it would make
			// that writer's rename fail. Only a temp file old enough that its
			// writer must have died (crash between create and rename) goes;
			// an atomic write takes milliseconds, so an hour is far past it.
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleTempAge {
				_ = os.Remove(filepath.Join(stashDir(cfg), name))
			}
			continue
		}
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".json"), ".spool")
		if fresh[base] {
			continue
		}
		if filepath.Ext(name) == ".spool" {
			if _, hasMeta := metaExists(cfg, base); hasMeta {
				// metadata past retention: both go
			} else if info, err := e.Info(); err != nil || time.Since(info.ModTime()) < reopenRetention {
				continue
			}
		}
		_ = os.Remove(filepath.Join(stashDir(cfg), name)) // best effort: retried by the next stash
	}
}

func metaExists(cfg Config, cnv string) (string, bool) {
	meta, _, err := safeStashPaths(cfg, cnv)
	if err != nil {
		return "", false
	}
	_, err = os.Stat(meta)
	return meta, err == nil
}

// DropReopenStash removes every stash that belongs to the discarded sessions
// sessionIDs (ses_ ids): one tagged with the recording, or whose conversation
// is the recording's twin. A discard promises no copy of the transcript stays
// behind, and the stash is one, so every discard path calls this even when the
// session's cache folder is already pruned.
func DropReopenStash(cfg Config, sessionIDs ...string) error {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, id := range sessionIDs {
		if id == "" {
			continue
		}
		want[id] = true
		if cnv, err := ConversationID(id); err == nil {
			want[cnv] = true
		}
	}
	entries, err := os.ReadDir(stashDir(cfg))
	if err != nil {
		return nil
	}
	var errs []error
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		var s stashedStream
		raw, err := os.ReadFile(filepath.Join(stashDir(cfg), e.Name()))
		if err != nil || json.Unmarshal(raw, &s) != nil {
			continue
		}
		if want[s.State.SessionID] || want[s.State.ConversationID] {
			errs = append(errs, removeStash(cfg, s.State.ConversationID))
		}
	}
	return errors.Join(errs...)
}
