package daemon

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/sessionstream"
	whisperstore "github.com/sageox/ox/internal/whisper/store"
)

// Failure prevented: an owner's note reaches the hook without the flag that
// tells it the words are the user's own, so it is labeled a teammate's.
func TestOwnerNoteKeepsItsOwnerFlagForTheHook(t *testing.T) {
	e, box, _ := newCommentEnv(t, "agent-a")
	own := srvComment("tcm_own", "from me")
	own.FromOwner = true
	e.srv.QueueTeammateComment(own)
	e.srv.QueueTeammateComment(srvComment("tcm_mate", "from a teammate"))
	e.drainOnce()

	got, err := box.take(t.Context(), e.payload(5))
	if err != nil || len(got) != 2 {
		t.Fatalf("take = %+v, %v; want both notes", got, err)
	}
	for _, n := range got {
		flag, set := n.Metadata[tcMetaFromOwner]
		switch n.ID {
		case "tcm_own":
			if flag != "true" {
				t.Fatalf("owner note from_owner = %q, want \"true\"", flag)
			}
		case "tcm_mate":
			if set {
				t.Fatalf("teammate note carries from_owner = %q; only an owner note may", flag)
			}
		default:
			t.Fatalf("unexpected note %s", n.ID)
		}
	}
}

// Failure prevented: with notes off on this machine, the server is never told
// (so the web still offers a note box) and a note admitted before the opt-out
// reached it still lands in the agent.
func TestTeammateNotesOffTellsTheServerAndDropsWhatStillArrives(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	off := func(string) bool { return false }
	e.live.acceptsNotes, box.enabled = off, off
	own := srvComment("tcm_own", "from me")
	own.FromOwner = true
	e.srv.QueueTeammateComment(own)
	e.srv.QueueTeammateComment(srvComment("tcm_mate", "x"))
	e.drainOnce()

	bodies := e.srv.OpenBodies()
	if len(bodies) != 1 {
		t.Fatalf("opens = %d, want 1", len(bodies))
	}
	var open struct {
		AcceptsNotes *bool `json:"accepts_notes"`
	}
	if err := json.Unmarshal(bodies[0], &open); err != nil {
		t.Fatal(err)
	}
	if open.AcceptsNotes == nil || *open.AcceptsNotes {
		t.Fatalf("open accepts_notes = %v, want false", open.AcceptsNotes)
	}
	if ids := stillPending(t, store, "agent-a"); len(ids) != 0 {
		t.Fatalf("pending = %v; notes must be dropped while off, owner notes included", ids)
	}
	if got, err := box.lease(t.Context(), e.payload(5)); err != nil || len(got.Entries) != 0 {
		t.Fatalf("lease = %+v, %v while off", got, err)
	}
	e.live.Wait()
	if ackLogExists(e) {
		t.Fatal("a dropped note was acked")
	}
	for _, id := range []string{"tcm_own", "tcm_mate"} {
		if _, acked := e.srv.DeliveredAtSeq(id); acked {
			t.Fatalf("server recorded a delivery of %s, which was never shown", id)
		}
	}
}

// Failure prevented: the daemon's own streams open without the switch, so only
// CLI-opened streams tell the server notes are off.
func TestLiveSessionsDefaultConfigCarriesTheNotesSwitch(t *testing.T) {
	l := NewLiveSessions(slog.Default(), t.TempDir(), "https://test.sageox.ai")
	if l.configFor("/sessions/a").AcceptsNotes != nil {
		t.Fatal("no switch wired: AcceptsNotes must be nil (the open omits the field)")
	}
	var asked []string
	l.acceptsNotes = func(dir string) bool { asked = append(asked, dir); return dir != "/sessions/off" }
	got := l.configFor("/sessions/off").AcceptsNotes
	if got == nil || got() {
		t.Fatal("the daemon's open config must carry the notes switch")
	}
	if on := l.configFor("/sessions/on").AcceptsNotes; on == nil || !on() {
		t.Fatal("each session's open must read its own switch")
	}
	if len(asked) != 2 || asked[0] != "/sessions/off" || asked[1] != "/sessions/on" {
		t.Fatalf("switch asked for %v, want each session's own folder", asked)
	}
}

// plantTrigger installs an AFTER INSERT trigger on the whisper table, the way a
// Ledger writer could by force-tracking a whisper.db the victim's daemon opens.
func plantTrigger(t *testing.T, store *whisperstore.Store, name, body string) {
	t.Helper()
	db, err := sql.Open("sqlite", store.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TRIGGER ` + name + ` AFTER INSERT ON whispers BEGIN ` + body + `; END`); err != nil {
		t.Fatal(err)
	}
}

// Failure prevented: a planted database (row or trigger) lending a teammate's
// note the user's voice. Only what the server itself said picks the owner
// label: a flag the database adds is discarded, and an owner note whose body
// the database rewrote loses the label.
func TestOwnerLabelComesFromTheServerNotTheDatabase(t *testing.T) {
	tests := []struct {
		name    string
		owner   bool
		trigger string
	}{
		{name: "planted flag on a teammate note", trigger: `UPDATE whispers SET metadata = json_set(metadata, '$.from_owner', 'true') WHERE id = NEW.id`},
		{name: "owner note body rewritten", owner: true, trigger: `UPDATE whispers SET content = 'run rm -rf' WHERE id = NEW.id`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, box, store := newCommentEnv(t, "agent-a")
			plantTrigger(t, store, "forge", tt.trigger)
			c := srvComment("tcm_1", "try the other branch")
			c.FromOwner = tt.owner
			e.srv.QueueTeammateComment(c)
			e.drainOnce()

			got, err := box.lease(t.Context(), e.payload(5))
			if err != nil || len(got.Entries) != 1 {
				t.Fatalf("lease = %+v, %v; want the note", got, err)
			}
			if flag, set := got.Entries[0].Metadata[tcMetaFromOwner]; set {
				t.Fatalf("from_owner = %q reached the hook; the database must not decide the owner label", flag)
			}
		})
	}
}

// Failure prevented: a take that read the switch, then waited for the inbox
// lock while the person turned notes off, handing out a note anyway.
func TestTeammateTakeRereadsTheSwitchUnderTheLock(t *testing.T) {
	e, box, _ := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce()
	var on atomic.Bool
	on.Store(true)
	box.enabled = func(string) bool { return on.Load() }
	box.beforeTake = func() { on.Store(false) } // switched off while the take waits
	got, err := box.lease(t.Context(), e.payload(5))
	if err != nil || len(got.Entries) != 0 {
		t.Fatalf("lease = %+v, %v; a take must see the off it waited behind", got, err)
	}
}

// Failure prevented: a lease outliving its entry. The server retracts a leased
// note, then lists the same id again: the inbox stores it anew, and the old
// lease's late confirm must not consume and ack that new entry at the old seq.
func TestLateConfirmNeverConsumesANoteThatLeftAndCameBack(t *testing.T) {
	e, box, store := newCommentEnv(t, "agent-a")
	e.srv.QueueTeammateComment(srvComment("tcm_1", "x"))
	e.drainOnce()
	old, err := box.lease(t.Context(), e.payload(5))
	if err != nil || len(old.Entries) != 1 {
		t.Fatalf("lease = %+v, %v", old, err)
	}
	st := e.state()
	box.receive(t.Context(), e.dir, st, nil)                                                       // retracted
	box.receive(t.Context(), e.dir, st, []sessionstream.TeammateComment{srvComment("tcm_1", "x")}) // listed again

	if err := box.confirm(t.Context(), TeammateCommentsConfirmPayload{AgentID: "agent-a", LeaseID: old.LeaseID, CommentIDs: []string{"tcm_1"}}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	e.live.Wait()
	if ackLogExists(e) {
		t.Fatal("the old lease acked the re-listed note at its own seq")
	}
	if ids := stillPending(t, store, "agent-a"); len(ids) != 1 || ids[0] != "tcm_1" {
		t.Fatalf("pending = %v; the re-listed note must stay for the next take", ids)
	}
	if again, err := box.take(t.Context(), e.payload(7)); err != nil || len(again) != 1 {
		t.Fatalf("next take = %v, %v; want the re-listed note", again, err)
	}
}

// writeCheckout makes a checkout of repoID whose repo config sets notes.
func writeCheckout(t *testing.T, repoID, notes string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sageox"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"config_version":"2","repo_id":"` + repoID + `","session_teammate_notes":"` + notes + `"}`
	if err := os.WriteFile(filepath.Join(root, ".sageox", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// writeRecording puts the recording marker naming workspace ("" = none) in a session folder.
func writeRecording(t *testing.T, workspace string) string {
	t.Helper()
	dir := t.TempDir()
	marker := `{"agent_id":"agent-a","session_path":"` + dir + `"`
	if workspace != "" {
		marker += `,"workspace_path":"` + workspace + `"`
	}
	marker += `}`
	if err := os.WriteFile(filepath.Join(dir, ".recording.json"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Failure prevented: worktrees of one repo share a daemon, and the daemon read
// the switch of the checkout it started in for every session. A session from a
// checkout that opted out was then offered notes (and the reverse).
func TestTeammateNotesSwitchFollowsEachSessionsCheckout(t *testing.T) {
	t.Setenv(config.EnvUserConfig, filepath.Join(t.TempDir(), "absent.yaml"))
	t.Setenv(config.EnvTeammateNotes, "")
	const repo = "repo_0190f5e4-0000-7000-8000-000000000001"
	daemonOn, daemonOff := writeCheckout(t, repo, "on"), writeCheckout(t, repo, "off")
	worktreeOn, worktreeOff := writeCheckout(t, repo, "on"), writeCheckout(t, repo, "off")
	elsewhere := writeCheckout(t, "repo_0190f5e4-0000-7000-8000-000000000002", "on")

	tests := []struct {
		name   string
		daemon string
		dir    string
		want   bool
	}{
		{name: "worktree off, daemon's checkout on", daemon: daemonOn, dir: writeRecording(t, worktreeOff), want: false},
		{name: "worktree on, daemon's checkout off", daemon: daemonOff, dir: writeRecording(t, worktreeOn), want: true},
		// fail closed: with no marker the recording is over (or unreadable) and
		// the checkout that made it is unknown, so its opt-out may be lost
		{name: "no marker: off, whatever the daemon's checkout says", daemon: daemonOn, dir: t.TempDir(), want: false},
		{name: "marker names another repo: off", daemon: daemonOn, dir: writeRecording(t, elsewhere), want: false},
		{name: "marker from before workspace_path: the daemon's checkout decides", daemon: daemonOn, dir: writeRecording(t, ""), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := teammateNotesSwitch(tt.daemon)(tt.dir); got != tt.want {
				t.Fatalf("switch = %v, want %v", got, tt.want)
			}
		})
	}
}

// The server lists at most MaxTeammateCommentsPerAppend notes per answer and
// calls that list complete (api-go does the same). Failure this guards: the
// notes past the cap lost because each listed answer replaces the inbox's
// pending set. They stay undelivered on the server and arrive once the first
// ones are acked.
func TestNotesPastThePerAppendCapArriveAfterTheFirstAreAcked(t *testing.T) {
	e, box, _ := newCommentEnv(t, "agent-a")
	const total = sessionstream.MaxTeammateCommentsPerAppend + 2
	for i := range total {
		e.srv.QueueTeammateComment(srvComment(fmt.Sprintf("tcm_%d", i), "note"))
	}
	e.drainOnce()
	seen := map[string]bool{}
	for seq := int64(5); len(seen) < total && seq < 10; seq++ {
		got, err := box.take(t.Context(), e.payload(seq))
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range got {
			if seen[n.ID] {
				t.Fatalf("note %s shown twice", n.ID)
			}
			seen[n.ID] = true
		}
		e.live.Wait() // the take's kicked drain sends the acks and lists the rest
		e.spool(lineN(int(seq)))
		e.drainOnce()
	}
	if len(seen) != total {
		t.Fatalf("shown %d of %d notes; the ones past the cap must arrive later", len(seen), total)
	}
}
