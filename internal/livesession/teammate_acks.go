package livesession

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sageox/ox/internal/sessionstream"
)

const (
	// teammateAcksFileName logs deliveries of teammate comments to the agent,
	// one JSON line {"id","delivered_at_seq"} each. Append-only: the hook's
	// daemon writes it without live.lock (a drain may hold the lock for a long
	// time), and a drain only reads it.
	teammateAcksFileName = "teammate-comment-acks"
	// teammateAcksSentFileName lists, one id per line, the acks a 2xx answer
	// (or a permanent refusal) settled, so each is sent once.
	teammateAcksSentFileName = "teammate-comment-acks-sent"
)

// RecordTeammateCommentAck appends one delivery to the session's ack log. It is
// one O_APPEND write of a short line, so concurrent writers never interleave,
// and it takes no lock for the same reason recordLost does not.
func RecordTeammateCommentAck(dir, commentID string, deliveredAtSeq int64) error {
	if commentID == "" || deliveredAtSeq < 1 {
		return fmt.Errorf("record teammate comment ack: need an id and a seq of at least 1")
	}
	line, err := json.Marshal(sessionstream.TeammateCommentAck{ID: commentID, DeliveredAtSeq: deliveredAtSeq})
	if err != nil {
		return fmt.Errorf("record teammate comment ack: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, teammateAcksFileName), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("record teammate comment ack: %w", err)
	}
	_, werr := f.Write(append(line, '\n'))
	if err := errors.Join(werr, f.Close()); err != nil {
		return fmt.Errorf("record teammate comment ack: %w", err)
	}
	return nil
}

// pendingTeammateAcks is every logged ack not yet settled, first record per id,
// in log order. An unreadable line is skipped: it could never be sent.
func pendingTeammateAcks(dir string) ([]sessionstream.TeammateCommentAck, error) {
	sent, err := readLines(filepath.Join(dir, teammateAcksSentFileName))
	if err != nil {
		return nil, err
	}
	done := make(map[string]bool, len(sent))
	for _, id := range sent {
		done[id] = true
	}
	lines, err := readLines(filepath.Join(dir, teammateAcksFileName))
	if err != nil {
		return nil, err
	}
	var out []sessionstream.TeammateCommentAck
	for _, l := range lines {
		var a sessionstream.TeammateCommentAck
		if json.Unmarshal([]byte(l), &a) != nil || a.ID == "" || a.DeliveredAtSeq < 1 || done[a.ID] {
			continue
		}
		done[a.ID] = true
		out = append(out, a)
	}
	return out, nil
}

// shownTeammateAcks is every logged delivery by comment id, first record wins:
// the originally recorded delivered_at_seq.
func shownTeammateAcks(dir string) map[string]sessionstream.TeammateCommentAck {
	lines, err := readLines(filepath.Join(dir, teammateAcksFileName))
	if err != nil {
		return nil
	}
	out := map[string]sessionstream.TeammateCommentAck{}
	for _, l := range lines {
		var a sessionstream.TeammateCommentAck
		if json.Unmarshal([]byte(l), &a) != nil || a.ID == "" || a.DeliveredAtSeq < 1 {
			continue
		}
		if _, ok := out[a.ID]; !ok {
			out[a.ID] = a
		}
	}
	return out
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		if t := sc.Text(); t != "" {
			out = append(out, t)
		}
	}
	return out, sc.Err()
}

func markTeammateAcksSent(dir string, acks []sessionstream.TeammateCommentAck) error {
	f, err := os.OpenFile(filepath.Join(dir, teammateAcksSentFileName), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	var b []byte
	for _, a := range acks {
		b = append(append(b, a.ID...), '\n')
	}
	_, werr := f.Write(b)
	return errors.Join(werr, f.Close())
}

// teammateResendsFileName lists ids of notes the server listed again after
// they were shown: their ack never landed, so it must be sent again with the
// first recorded seq. It is persisted because the obligation must survive a 503
// and a restart; only a 2xx or a permanent refusal clears an id.
const teammateResendsFileName = "teammate-comment-resends"

func addTeammateResend(dir, id string) error {
	ids, err := readLines(filepath.Join(dir, teammateResendsFileName))
	if err != nil || slices.Contains(ids, id) {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, teammateResendsFileName), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(id + "\n")
	return errors.Join(werr, f.Close())
}

// clearTeammateResends removes settled ids from the resend list.
func clearTeammateResends(dir string, settled []sessionstream.TeammateCommentAck) error {
	path := filepath.Join(dir, teammateResendsFileName)
	ids, err := readLines(path)
	if err != nil || len(ids) == 0 {
		return err
	}
	var keep []string
	for _, id := range ids {
		if !slices.ContainsFunc(settled, func(a sessionstream.TeammateCommentAck) bool { return a.ID == id }) {
			keep = append(keep, id)
		}
	}
	if len(keep) == len(ids) {
		return nil
	}
	tmp := path + ".tmp"
	body := ""
	if len(keep) > 0 {
		body = strings.Join(keep, "\n") + "\n"
	}
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ackObligations is every ack still owed: logged and unsettled, plus every
// resend, in order, once per id.
func ackObligations(dir string) ([]sessionstream.TeammateCommentAck, error) {
	pending, err := pendingTeammateAcks(dir)
	if err != nil {
		return nil, err
	}
	resends, err := readLines(filepath.Join(dir, teammateResendsFileName))
	if err != nil {
		return nil, err
	}
	shown := shownTeammateAcks(dir)
	for _, id := range resends {
		a, ok := shown[id]
		if ok && !slices.ContainsFunc(pending, func(p sessionstream.TeammateCommentAck) bool { return p.ID == id }) {
			pending = append(pending, a)
		}
	}
	return pending, nil
}

// permanentAckRefusal: no later attempt can succeed, so the obligation ends.
func permanentAckRefusal(api *sessionstream.APIError) bool {
	switch classify(api) {
	case refusalStop, refusalDeleted:
		return true
	}
	return false
}

// sendTeammateAcks tells the server which comments reached the agent, at most
// MaxTeammateCommentAcks per request. It never fails the drain or the close: a
// retryable answer or a network error leaves every ack owed for a later
// attempt, and only a 2xx or a permanent refusal (a malformed ack, a deleted
// session) settles one.
func (s *session) sendTeammateAcks(ctx context.Context) {
	if s.c == nil {
		return
	}
	owed, err := ackObligations(s.dir)
	if err != nil {
		s.log().WarnContext(ctx, "live_teammate_acks_unreadable", "error", err)
		return
	}
	for len(owed) > 0 && ctx.Err() == nil {
		batch := owed[:min(len(owed), sessionstream.MaxTeammateCommentAcks)]
		owed = owed[len(batch):]
		_, err := s.c.AckTeammateComments(ctx, s.st.ConversationID, sessionstream.AckTeammateCommentsRequest{LayerID: s.st.LayerID, Acks: batch})
		if err != nil {
			var api *sessionstream.APIError
			if !errors.As(err, &api) || !permanentAckRefusal(api) {
				return // retryable or network: still owed
			}
			s.log().WarnContext(ctx, "live_teammate_acks_dropped", "count", len(batch), "reason", api.Body.Reason, "status", api.Status)
		}
		s.settleTeammateAcks(ctx, batch)
	}
}

func (s *session) settleTeammateAcks(ctx context.Context, batch []sessionstream.TeammateCommentAck) {
	if err := markTeammateAcksSent(s.dir, batch); err != nil {
		s.log().WarnContext(ctx, "live_teammate_acks_mark_failed", "error", err)
		return
	}
	if err := clearTeammateResends(s.dir, batch); err != nil {
		s.log().WarnContext(ctx, "live_teammate_resend_clear_failed", "error", err)
	}
}

// TeammateAcksOwed reports whether the folder still owes the server a delivery
// ack. The daemon uses it to keep retrying after the stream closed.
func TeammateAcksOwed(dir string) bool {
	owed, err := ackObligations(dir)
	return err == nil && len(owed) > 0
}

// teammateAcksGiveUpAfter bounds how long a closed session's owed acks are
// retried: a day of failures is no longer a transient outage.
const teammateAcksGiveUpAfter = 24 * time.Hour

// FlushTeammateAcks sends the acks a stream owes after it ended (closed), for
// the daemon's tick. It is bounded: acks older than 24 hours (by the ack log's
// last write) are given up once, with one Warn line, so a dead server does not
// keep a finished session in the sweep forever. Only a stream this machine
// minted is contacted (authorize).
func FlushTeammateAcks(ctx context.Context, cfg Config, dir string, budget time.Duration, now time.Time) error {
	if !TeammateAcksOwed(dir) {
		return nil
	}
	cfg, err := cfg.withDefaults()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	return withLock(ctx, dir, budget, func() error {
		s, err := loadSession(cfg, dir)
		if err != nil {
			return err
		}
		if s.st.Status != StatusClosed {
			return nil
		}
		if info, serr := os.Stat(filepath.Join(dir, teammateAcksFileName)); serr == nil && now.Sub(info.ModTime()) > teammateAcksGiveUpAfter {
			owed, _ := ackObligations(dir)
			s.log().WarnContext(ctx, "live_teammate_acks_given_up", "count", len(owed), "older_than", teammateAcksGiveUpAfter.String())
			s.settleTeammateAcks(ctx, owed)
			return nil
		}
		if err := authorize(ctx, cfg, dir, s.st); err != nil {
			return err
		}
		c, err := cfg.newClient(ctx, cfg.Endpoint)
		if err != nil {
			return fmt.Errorf("live client: %w", err)
		}
		s.c = c
		s.sendTeammateAcks(ctx)
		return nil
	})
}

// CheckTeammateAckLog reports whether a delivery ack could be logged in dir, by
// opening the ack log for appending (creating it) without writing. The daemon
// leases a note only when its ack can be written on confirm.
func CheckTeammateAckLog(dir string) error {
	f, err := os.OpenFile(filepath.Join(dir, teammateAcksFileName), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("teammate comment ack log: %w", err)
	}
	return f.Close()
}
