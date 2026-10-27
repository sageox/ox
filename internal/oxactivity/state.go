package oxactivity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/fileutil"
)

// ErrInvalidAgentID is returned by SaveState for an agent ID that is not a
// well-formed ox agent ID, which also keeps it from naming a path outside the
// state directory.
var ErrInvalidAgentID = errors.New("invalid agent ID")

// State counts the team context that influenced one harness session of one
// AI coworker: how many distinct discussions, plans, docs, sessions and
// murmurs ox put to work in it. It is local-only derived data, bumped by
// hooks and use-evidence writers (RecordInfluence) and only ever read by
// `ox statusline`.
//
// Files written by older ox versions held an activity feed (last_action,
// last_whisper_*); those keys are ignored on decode, so an old file reads as
// a zero-count State.
type State struct {
	Discussions int `json:"discussions,omitempty"`
	Plans       int `json:"plans,omitempty"`
	Docs        int `json:"docs,omitempty"`
	Sessions    int `json:"sessions,omitempty"`
	Murmurs     int `json:"murmurs,omitempty"`
	// SeenRefs dedups bumps: one "<refType>\x00<ref>" per counted item, newest
	// last, capped at MaxSeenRefs. An item that falls off the cap can count
	// again; at that size the line is already saturated.
	SeenRefs []string `json:"seen_refs,omitempty"`
	// Used counts the distinct items the AI coworker reported using or cited
	// in a commit (RecordUse), whether or not a hook saw them fetched.
	Used int `json:"used,omitempty"`
	// UsedRefs dedups Used the way SeenRefs dedups the kind buckets, with the
	// same key format and cap.
	UsedRefs  []string  `json:"used_refs,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
}

// MaxSeenRefs caps State.SeenRefs and State.UsedRefs; the oldest entries are
// dropped first.
const MaxSeenRefs = 64

// TeamContextRef is the one item every "team-context" ref counts as: the
// distilled Team Context as a whole, read by `ox agent <id> team-ctx`. The
// retrieval hook and the use-evidence writers (which name it
// "team-ctx/<slug>") must agree on it, or one read plus one use would show as
// two docs.
const TeamContextRef = "agent team-ctx"

// NormalizeRef maps ref to the key it is counted under, so the retrieval hook
// and the use-evidence writers name the same item the same way: any
// "team-context" ref becomes TeamContextRef, and a ref holding a
// conversation, recording or discussion ID (a bare "cnv_…" or a
// sageox:// / sageox.ai link to one) becomes that ID. Anything else is
// returned trimmed.
func NormalizeRef(refType, ref string) string {
	refType = strings.TrimSpace(refType)
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if refType == "team-context" {
		return TeamContextRef
	}
	if id := conversationIDRe.FindString(ref); id != "" {
		return id
	}
	return ref
}

// Bump counts ref once under the bucket refType names and reports whether
// anything changed. refType is the context-trace ref_type vocabulary:
// "discussion" → Discussions; "plan" → Plans; "doc", "team-context", "kb",
// "skill" → Docs; "session" → Sessions; "murmur" → Murmurs. An unknown
// refType, an empty ref, or a ref already counted changes nothing.
func (s *State) Bump(refType, ref string) bool {
	refType = strings.TrimSpace(refType)
	ref = NormalizeRef(refType, ref)
	counter := s.counter(refType)
	if counter == nil || ref == "" {
		return false
	}
	key := refType + "\x00" + ref
	if slices.Contains(s.SeenRefs, key) {
		return false
	}
	*counter++
	s.SeenRefs = appendCapped(s.SeenRefs, key)
	return true
}

// MarkUsed counts ref once in Used and also Bumps it into its kind bucket, so
// an item used without ever being fetched through a hook (a whisper, a prime
// doc, a recall hit) still shows its kind. It reports whether anything
// changed. The same refType vocabulary, normalisation and no-op rules as Bump
// apply.
func (s *State) MarkUsed(refType, ref string) bool {
	refType = strings.TrimSpace(refType)
	ref = NormalizeRef(refType, ref)
	if s.counter(refType) == nil || ref == "" {
		return false
	}
	changed := s.Bump(refType, ref)
	key := refType + "\x00" + ref
	if slices.Contains(s.UsedRefs, key) {
		return changed
	}
	s.Used++
	s.UsedRefs = appendCapped(s.UsedRefs, key)
	return true
}

// appendCapped appends key and drops the oldest entries past MaxSeenRefs.
func appendCapped(refs []string, key string) []string {
	refs = append(refs, key)
	if over := len(refs) - MaxSeenRefs; over > 0 {
		refs = slices.Delete(refs, 0, over)
	}
	return refs
}

// counter returns the count refType bumps, or nil for an unknown refType.
func (s *State) counter(refType string) *int {
	switch refType {
	case "discussion":
		return &s.Discussions
	case "plan":
		return &s.Plans
	case "doc", "team-context", "kb", "skill":
		return &s.Docs
	case "session":
		return &s.Sessions
	case "murmur":
		return &s.Murmurs
	}
	return nil
}

// Counts converts the state into what Render draws.
func (s State) Counts() Counts {
	return Counts{
		Discussions: s.Discussions,
		Plans:       s.Plans,
		Docs:        s.Docs,
		Sessions:    s.Sessions,
		Murmurs:     s.Murmurs,
		Used:        s.Used,
	}
}

// RecordInfluence counts one item of team context (ref, of kind refType —
// see State.Bump) as having influenced a harness session, so `ox statusline`
// shows it. It is the hand-off API for every use-evidence writer: the
// PostToolUse hook's retrieval classifier, `ox agent <id> context used
// <ref>`, and post-commit detectors.
//
// harnessSessionID is the HARNESS's session ID — Claude Code's or Codex's
// hook/status-line session_id — not the ox recording ID: the status line is
// keyed by oxactivity.StateKey(agentID, harnessSessionID), so any other ID
// writes a file no status line reads. Empty falls back to agent-wide state,
// which no status line shows either; pass it whenever the caller has it.
//
// The state file and its directory are created when absent. An unknown
// refType or an empty ref is a no-op returning nil, as is a ref already
// counted. It waits at most the default file-lock timeout for a concurrent
// writer, then returns that error; callers in hooks should log it and move on.
func RecordInfluence(projectRoot, agentID, harnessSessionID, refType, ref string) error {
	return record(projectRoot, agentID, harnessSessionID, refType, ref, "record influence", (*State).Bump)
}

// RecordUse counts one item of team context as used — reported by the AI
// coworker (`ox agent <id> context used <ref>`) or cited in a commit message —
// in a harness session: Used goes up once per distinct item, and the item is
// also counted in its kind bucket if no hook saw it fetched (see
// State.MarkUsed). harnessSessionID, the no-op rules and the locking are as
// for RecordInfluence.
func RecordUse(projectRoot, agentID, harnessSessionID, refType, ref string) error {
	return record(projectRoot, agentID, harnessSessionID, refType, ref, "record use", (*State).MarkUsed)
}

// record applies apply(refType, ref) to the stored state under the lock.
func record(projectRoot, agentID, harnessSessionID, refType, ref, op string, apply func(*State, string, string) bool) error {
	var probe State
	if probe.counter(strings.TrimSpace(refType)) == nil || NormalizeRef(refType, ref) == "" {
		return nil
	}
	if projectRoot == "" {
		return fmt.Errorf("%s: empty project root", op)
	}
	err := UpdateState(projectRoot, StateKey(agentID, harnessSessionID), func(s *State) bool {
		if !apply(s, refType, ref) {
			return false
		}
		s.UpdatedAt = time.Now().UTC()
		return true
	})
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return nil
}

// StateKey names the state file for one harness session of one AI coworker:
// "<agentID>" alone when sessionID is empty, else "<agentID>.<sessionID>".
// An ox agent ID can outlive a Claude Code or Codex session and be shared by
// several windows in one repo, so state keyed by it alone shows one window's
// counts in another. The harness session ID is what makes the
// line belong to the session the coworker is looking at. A session ID with
// characters unsafe in a file name is replaced by a short hash of itself.
func StateKey(agentID, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return agentID
	}
	if !sessionIDPattern.MatchString(sessionID) {
		sum := sha256.Sum256([]byte(sessionID))
		sessionID = hex.EncodeToString(sum[:8])
	}
	return agentID + "." + sessionID
}

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// StatePath returns <projectRoot>/.sageox/cache/statusline/<key>.json for a
// key from StateKey (or a bare agent ID), or "" when the key is malformed.
func StatePath(projectRoot, key string) string {
	agentID, sessionID, dotted := strings.Cut(key, ".")
	if !agentinstance.IsValidAgentID(agentID) {
		return ""
	}
	if dotted && !sessionIDPattern.MatchString(sessionID) {
		return ""
	}
	return filepath.Join(stateDir(projectRoot), key+".json")
}

func stateDir(projectRoot string) string {
	return filepath.Join(projectRoot, ".sageox", "cache", "statusline")
}

// LoadState reads the influence state for key (see StateKey). A missing,
// unreadable, or corrupt file, or a malformed key, yields a zero State: the
// status line is best-effort and must never fail the caller.
func LoadState(projectRoot, key string) State {
	var s State
	path := StatePath(projectRoot, key)
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is built from a validated key
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}
	}
	return s
}

// UpdateState applies update to the stored state under a file lock, so two
// writers (a tool-call hook and `ox agent <id> context used`) that overlap
// cannot each lose the other's bump from a stale snapshot. update reports whether
// anything changed; false skips the write. A missing or corrupt file starts
// from a zero State, as LoadState does.
func UpdateState(projectRoot, key string, update func(*State) bool) error {
	path := StatePath(projectRoot, key)
	if path == "" {
		return fmt.Errorf("update activity state: %w: %q", ErrInvalidAgentID, key)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("update activity state: %w", err)
	}
	return fileutil.WithFileLock(context.Background(), path, func() error {
		s := LoadState(projectRoot, key)
		if !update(&s) {
			return nil
		}
		return SaveState(projectRoot, key, s)
	})
}

// SaveState atomically writes the influence state for key (see StateKey),
// creating the cache directory if needed. State files of sessions idle for
// more than StateRetention are removed at the same time, so per-session
// files never pile up.
func SaveState(projectRoot, key string, s State) error {
	path := StatePath(projectRoot, key)
	if path == "" {
		return fmt.Errorf("save activity state: %w: %q", ErrInvalidAgentID, key)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save activity state: %w", err)
	}
	if err := fileutil.AtomicWriteJSON(path, s, 0o600); err != nil {
		return fmt.Errorf("save activity state: %w", err)
	}
	pruneStale(filepath.Dir(path), path, time.Now())
	return nil
}

// StateRetention is how long an untouched session state file is kept.
const StateRetention = 24 * time.Hour

// pruneStale removes per-session state files ("<agentID>.<sessionID>.json")
// not modified within StateRetention. Other files in the directory, such as
// agent-wide state or a legacy murmur cache, are left alone. It is best-effort: the
// state just written is never removed and errors are ignored, since this runs
// inside a harness hook.
func pruneStale(dir, keep string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() || p == keep || !isSessionStateFile(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= StateRetention {
			continue
		}
		_ = os.Remove(p)
	}
}

// isSessionStateFile reports whether name is "<agentID>.<sessionID>.json".
func isSessionStateFile(name string) bool {
	key, ok := strings.CutSuffix(name, ".json")
	if !ok {
		return false
	}
	agentID, sessionID, dotted := strings.Cut(key, ".")
	return dotted && agentinstance.IsValidAgentID(agentID) && sessionIDPattern.MatchString(sessionID)
}
