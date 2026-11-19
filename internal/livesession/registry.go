package livesession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/fileutil"
)

// The session cache, and so every stream-state.json, lives inside the Ledger
// checkout: anyone who can push to the Ledger can plant one that names any
// conversation and any endpoint, and it arrives with an ordinary pull. A signed
// request carries this machine's key and login, so a stream state is never
// evidence enough to send one. Two checks stand in front of every signed
// request about a conversation (authorize):
//
//   - The endpoint is the caller's own configured one (Config.Endpoint), never
//     one a file names.
//   - The conversation was minted here, for this session folder: Prepare and
//     Open record each id they mint, bound to the folder it streams from, in a
//     registry outside the checkout, beside the pending-deletes queue and
//     written the same way, before any open request for it is sent. The folder
//     binding is what stops a forgery that copies one of this machine's own
//     ids (they appear in Ledger session metadata) into a folder of its own.
//     Only MoveStream, which retiring a stream uses, moves a binding.
//
// The pending-deletes queue needs no registry check of its own: only abort and
// an aborted open write it, and both act on a stream state authorize let through.

const mintedFileName = "live-conversations.json"

// ErrUntrustedState means a session's stream state names an endpoint other
// than the configured one, or a conversation this machine did not mint for
// that session folder. Nothing was sent for it, and nothing will be: the
// refusal is logged once at Error and retrying cannot change it.
var ErrUntrustedState = errors.New("live stream state was not minted on this machine for this folder")

// Refusal reasons Abort reports (AbortResult.Reason) for an untrusted state.
const (
	endpointMismatchReason    = "endpoint_mismatch"
	unknownConversationReason = "unknown_conversation"
)

// mintedConversation is one registry entry.
type mintedConversation struct {
	Endpoint       string `json:"endpoint"`
	ConversationID string `json:"conversation_id"`
	// Dir is the session folder the conversation streams from, resolved
	// (canonicalDir). Its stream state is trusted in this folder only.
	Dir      string    `json:"dir"`
	MintedAt time.Time `json:"minted_at"`
}

func mintedPath(cfg Config) string { return filepath.Join(cfg.DataDir, mintedFileName) }

func sameEndpoint(a, b string) bool {
	return endpoint.NormalizeEndpoint(a) == endpoint.NormalizeEndpoint(b)
}

// canonicalDir is the one spelling a session folder has in the registry,
// however a caller spelled its path: the daemon scans the cache folder as
// configured, a hook names it through symlinks. A path that cannot be resolved
// (a folder already gone) falls back to Clean.
func canonicalDir(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// withMinted runs fn on the registry under its lock. fn returns whether it
// changed the list; a changed list is written back atomically.
func withMinted(ctx context.Context, cfg Config, fn func(list []mintedConversation) ([]mintedConversation, bool, error)) error {
	return fileutil.WithFileLockTimeout(ctx, mintedPath(cfg), pendingLockWait, func() error {
		list, err := loadMinted(cfg)
		if err != nil {
			return err
		}
		list, changed, err := fn(list)
		if err != nil || !changed {
			return err
		}
		return saveMinted(cfg, list)
	})
}

// recordMinted binds cnv, minted here for cfg's endpoint, to the session folder
// dir. It must succeed before a stream state naming cnv is written or any
// request for it is sent: a stream whose id is not bound to its folder is
// refused everywhere. dir must exist, so the binding is its resolved path. An
// id already bound to another folder is never rebound here (MoveStream does
// that): fail closed.
func recordMinted(ctx context.Context, cfg Config, cnv, dir string) error {
	info, err := os.Stat(dir)
	if err == nil && !info.IsDir() {
		err = errors.New("not a directory")
	}
	if err != nil {
		return fmt.Errorf("record minted live conversation: session folder %s: %w", dir, err)
	}
	bound := canonicalDir(dir)
	err = withMinted(ctx, cfg, func(list []mintedConversation) ([]mintedConversation, bool, error) {
		if i := mintedIndex(list, cfg.Endpoint, cnv); i >= 0 {
			if list[i].Dir != bound {
				return nil, false, fmt.Errorf("conversation %s is already bound to session folder %s", cnv, list[i].Dir)
			}
			return list, false, nil
		}
		return append(list, mintedConversation{Endpoint: cfg.Endpoint, ConversationID: cnv, Dir: bound, MintedAt: time.Now().UTC()}), true, nil
	})
	if err != nil {
		return fmt.Errorf("record minted live conversation: %w", err)
	}
	return nil
}

// findMinted returns cnv's entry for cfg's endpoint.
func findMinted(ctx context.Context, cfg Config, cnv string) (mintedConversation, bool, error) {
	var m mintedConversation
	var found bool
	err := withMinted(ctx, cfg, func(list []mintedConversation) ([]mintedConversation, bool, error) {
		if i := mintedIndex(list, cfg.Endpoint, cnv); i >= 0 {
			m, found = list[i], true
		}
		return list, false, nil
	})
	if err != nil {
		return mintedConversation{}, false, fmt.Errorf("read live conversation registry: %w", err)
	}
	return m, found, nil
}

func mintedIndex(list []mintedConversation, ep, cnv string) int {
	return slices.IndexFunc(list, func(m mintedConversation) bool {
		return m.ConversationID == cnv && sameEndpoint(m.Endpoint, ep)
	})
}

func loadMinted(cfg Config) ([]mintedConversation, error) {
	raw, err := os.ReadFile(mintedPath(cfg))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read live conversation registry: %w", err)
	}
	var list []mintedConversation
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("parse live conversation registry: %w", err)
	}
	return list, nil
}

func saveMinted(cfg Config, list []mintedConversation) error {
	if err := fileutil.EnsurePrivateDir(cfg.DataDir); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := fileutil.AtomicWriteJSON(mintedPath(cfg), list, 0o600); err != nil {
		return fmt.Errorf("write live conversation registry: %w", err)
	}
	return nil
}

// MoveStream moves a live stream from the session folder from to the folder
// to: move renames its files, and the stream's registry binding moves with
// them, both under the registry lock, so authorize never sees the files in one
// folder and the binding in the other. to must exist. move must rename the
// stream state last: when it fails, the state is still in from.
//
// The binding moves first. If that write fails nothing is moved, the stream
// stays in from where it is still trusted, and the error is returned. If move
// fails, the binding goes back to from. A stream with no binding in from (one
// authorize already refuses) moves without one and stays refused.
func MoveStream(ctx context.Context, cfg Config, st State, from, to string, move func() error) error {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return err
	}
	src, dst := canonicalDir(from), canonicalDir(to)
	log := cfg.Logger.With("session_id", st.SessionID, "conversation_id", st.ConversationID, "from", src, "to", dst)
	// rebind points cnv's entry bound to one folder at another; false when
	// none is bound there
	rebind := func(list []mintedConversation, oldDir, newDir string) bool {
		i := mintedIndex(list, cfg.Endpoint, st.ConversationID)
		if i < 0 || list[i].Dir != oldDir {
			return false
		}
		list[i].Dir = newDir
		return true
	}
	return fileutil.WithFileLockTimeout(ctx, mintedPath(cfg), pendingLockWait, func() error {
		list, err := loadMinted(cfg)
		if err != nil {
			log.ErrorContext(ctx, "live_session_rebind_failed", "error", err)
			return fmt.Errorf("move the live stream's registry binding: %w", err)
		}
		bound := rebind(list, src, dst)
		if bound {
			if err := saveMinted(cfg, list); err != nil {
				log.ErrorContext(ctx, "live_session_rebind_failed", "error", err)
				return fmt.Errorf("move the live stream's registry binding: %w", err)
			}
		}
		moveErr := move()
		if moveErr == nil || !bound {
			return moveErr
		}
		rebind(list, dst, src)
		if err := saveMinted(cfg, list); err != nil {
			// the stream state is still in from, bound to to: stranded until
			// a person moves it, so say exactly where it stands
			log.ErrorContext(ctx, "live_session_rebind_restore_failed", "move_error", moveErr, "error", err)
			return errors.Join(moveErr, fmt.Errorf("restore the live stream's registry binding: %w", err))
		}
		return moveErr
	})
}

// authorize lets a signed request about st's conversation go only when st names
// cfg's own endpoint and a conversation minted here for the session folder dir.
// A refusal is ErrUntrustedState, logged once at Error for dir. An unreadable
// registry is returned as is: nothing is sent, and the next attempt reads it
// again.
func authorize(ctx context.Context, cfg Config, dir string, st State) error {
	if !sameEndpoint(st.Endpoint, cfg.Endpoint) {
		logRefusalOnce(ctx, cfg, "live_session_endpoint_mismatch", dir, st, "state_endpoint", st.Endpoint, "endpoint", cfg.Endpoint)
		return fmt.Errorf("%w: it names endpoint %q, not the configured %q", ErrUntrustedState, st.Endpoint, cfg.Endpoint)
	}
	m, ok, err := findMinted(ctx, cfg, st.ConversationID)
	if err != nil {
		return err
	}
	if !ok || m.Dir != canonicalDir(dir) {
		args := []any{"endpoint", cfg.Endpoint}
		if ok {
			// a real id in the wrong folder: the likeliest forgery of all
			args = append(args, "minted_for_dir", m.Dir)
		}
		logRefusalOnce(ctx, cfg, "live_session_unknown_conversation", dir, st, args...)
		return fmt.Errorf("%w: conversation %q", ErrUntrustedState, st.ConversationID)
	}
	return nil
}

// refusalsLogged holds the refusals already logged in this process. A daemon
// meets a forged state on every tick; one Error line per forgery is the signal,
// the rest would be noise. A forgery that changes its ids is logged again.
var refusalsLogged sync.Map

func logRefusalOnce(ctx context.Context, cfg Config, event, dir string, st State, args ...any) {
	key := event + "\x00" + dir + "\x00" + st.ConversationID + "\x00" + st.Endpoint
	if _, seen := refusalsLogged.LoadOrStore(key, struct{}{}); seen {
		return
	}
	cfg.Logger.ErrorContext(ctx, event, append([]any{
		"session_dir", dir, "session_id", st.SessionID, "conversation_id", st.ConversationID, "layer_id", st.LayerID,
	}, args...)...)
}

// untrustedReason is the AbortResult.Reason for st, which authorize refused.
func untrustedReason(st State, cfg Config) string {
	if !sameEndpoint(st.Endpoint, cfg.Endpoint) {
		return endpointMismatchReason
	}
	return unknownConversationReason
}
