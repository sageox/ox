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

// sendTeammateAcks tells the server which comments reached the agent, at most
// MaxTeammateCommentAcks per request. It never fails the drain: a retryable
// answer or a network error leaves the acks for the next drain, and a
// permanent refusal (fix, stop) settles them so a malformed ack cannot retry
// forever.
func (s *session) sendTeammateAcks(ctx context.Context) {
	if s.c == nil {
		return
	}
	pending, err := pendingTeammateAcks(s.dir)
	if err != nil {
		s.log().WarnContext(ctx, "live_teammate_acks_unreadable", "error", err)
		return
	}
	// a note the server listed again was shown but its ack never landed (its
	// slot stays taken until it does): send it again, with the first seq
	for _, a := range s.resendAcks {
		if !slices.ContainsFunc(pending, func(p sessionstream.TeammateCommentAck) bool { return p.ID == a.ID }) {
			pending = append(pending, a)
		}
	}
	s.resendAcks = nil
	for len(pending) > 0 && ctx.Err() == nil {
		batch := pending[:min(len(pending), sessionstream.MaxTeammateCommentAcks)]
		pending = pending[len(batch):]
		_, err := s.c.AckTeammateComments(ctx, s.st.ConversationID, sessionstream.AckTeammateCommentsRequest{LayerID: s.st.LayerID, Acks: batch})
		if err != nil {
			var api *sessionstream.APIError
			if !errors.As(err, &api) || classify(api) != refusalStop {
				return // retryable or network: next drain
			}
			s.log().WarnContext(ctx, "live_teammate_acks_dropped", "count", len(batch), "reason", api.Body.Reason, "status", api.Status)
		}
		if err := markTeammateAcksSent(s.dir, batch); err != nil {
			s.log().WarnContext(ctx, "live_teammate_acks_mark_failed", "error", err)
			return
		}
	}
}
