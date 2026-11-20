package livesession

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
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

const (
	// teammateResendsFileName lists notes the server listed again after they
	// were shown ("id<TAB>unix seconds", the second being when the obligation
	// began): their ack never landed, so it must be sent again with the first
	// recorded seq. Persisted because the obligation must survive a 503 and a
	// restart; only a 2xx or a permanent refusal clears an id.
	teammateResendsFileName = "teammate-comment-resends"
	// maxAckLogBytes and maxAckLogLines bound how much of a receipt log is
	// parsed. The session cache is inside a pulled Ledger, so a log can come from
	// someone else: past the bound the log is refused, never partly trusted.
	maxAckLogBytes = 256 << 10
	maxAckLogLines = 2000
)

// ErrAckLogTooLarge means a receipt log passed its bound and was not parsed.
var ErrAckLogTooLarge = errors.New("teammate comment receipt log is larger than its bound")

// readBoundedLines reads a receipt log, refusing one past the byte or line
// bound. A missing file is an empty log; any other failure is an error, never an
// empty success.
func readBoundedLines(path string) ([]string, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	if info.Size() > maxAckLogBytes {
		return nil, fmt.Errorf("%w: %s holds %d bytes", ErrAckLogTooLarge, filepath.Base(path), info.Size())
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only
	var out []string
	sc := bufio.NewScanner(io.LimitReader(f, maxAckLogBytes+1))
	sc.Buffer(make([]byte, 0, 4096), maxAckLogBytes)
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			if len(out) == maxAckLogLines {
				return nil, fmt.Errorf("%w: %s holds more than %d lines", ErrAckLogTooLarge, filepath.Base(path), maxAckLogLines)
			}
			out = append(out, t)
		}
	}
	return out, sc.Err()
}

// ackLogLine is one receipt as logged. The first two fields are the wire ack;
// At is local, so a retry period can be measured from when the receipt began
// to be owed.
type ackLogLine struct {
	ID             string    `json:"id"`
	DeliveredAtSeq int64     `json:"delivered_at_seq"`
	At             time.Time `json:"at,omitzero"`
}

// RecordTeammateCommentAck appends one delivery to the session's ack log. It is
// one O_APPEND write of a short line, so concurrent writers never interleave,
// and it takes no lock for the same reason recordLost does not.
func RecordTeammateCommentAck(dir, commentID string, deliveredAtSeq int64) error {
	if commentID == "" || deliveredAtSeq < 1 {
		return fmt.Errorf("record teammate comment ack: need an id and a seq of at least 1")
	}
	line, err := json.Marshal(ackLogLine{ID: commentID, DeliveredAtSeq: deliveredAtSeq, At: time.Now().UTC()})
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

// obligation is one ack still owed and when it began to be owed.
type obligation struct {
	ack   sessionstream.TeammateCommentAck
	since time.Time
}

func fileModTime(path string) time.Time {
	if info, err := os.Stat(path); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

// readAckLog is every logged receipt, first record per id (the originally
// recorded seq), in log order, with when it began. An unparsable line is
// skipped: it could never be sent.
func readAckLog(dir string) (byID map[string]obligation, order []string, err error) {
	path := filepath.Join(dir, teammateAcksFileName)
	lines, err := readBoundedLines(path)
	if err != nil {
		return nil, nil, err
	}
	mtime := fileModTime(path)
	byID = make(map[string]obligation, len(lines))
	for _, l := range lines {
		var a ackLogLine
		if json.Unmarshal([]byte(l), &a) != nil || a.ID == "" || a.DeliveredAtSeq < 1 {
			continue
		}
		if _, ok := byID[a.ID]; ok {
			continue
		}
		since := a.At
		if since.IsZero() {
			since = mtime
		}
		byID[a.ID] = obligation{ack: sessionstream.TeammateCommentAck{ID: a.ID, DeliveredAtSeq: a.DeliveredAtSeq}, since: since}
		order = append(order, a.ID)
	}
	return byID, order, nil
}

func readSettled(dir string) (map[string]bool, error) {
	ids, err := readBoundedLines(filepath.Join(dir, teammateAcksSentFileName))
	if err != nil {
		return nil, err
	}
	done := make(map[string]bool, len(ids))
	for _, id := range ids {
		done[id] = true
	}
	return done, nil
}

// readResends is the resend list with when each began; a line with no time
// (older format) counts from the file's last write.
func readResends(dir string) (since map[string]time.Time, order []string, err error) {
	path := filepath.Join(dir, teammateResendsFileName)
	lines, err := readBoundedLines(path)
	if err != nil {
		return nil, nil, err
	}
	mtime := fileModTime(path)
	since = make(map[string]time.Time, len(lines))
	for _, l := range lines {
		id, unix, _ := strings.Cut(l, "\t")
		if _, ok := since[id]; ok || id == "" {
			continue
		}
		t := mtime
		if n, perr := strconv.ParseInt(unix, 10, 64); perr == nil {
			t = time.Unix(n, 0)
		}
		since[id] = t
		order = append(order, id)
	}
	return since, order, nil
}

// ackObligations is every ack still owed, in order, once per id: logged and not
// settled, plus every resend. Reconciliation is by map, linear in the logs.
func ackObligations(dir string) ([]obligation, error) {
	logged, order, err := readAckLog(dir)
	if err != nil {
		return nil, err
	}
	settled, err := readSettled(dir)
	if err != nil {
		return nil, err
	}
	resendSince, resendOrder, err := readResends(dir)
	if err != nil {
		return nil, err
	}
	var out []obligation
	owed := make(map[string]bool, len(order))
	for _, id := range order {
		if !settled[id] {
			out = append(out, logged[id])
			owed[id] = true
		}
	}
	for _, id := range resendOrder {
		if a, ok := logged[id]; ok && !owed[id] {
			out = append(out, obligation{ack: a.ack, since: resendSince[id]})
			owed[id] = true
		}
	}
	return out, nil
}

// shownTeammateAcks is every logged delivery by comment id: the originally
// recorded delivered_at_seq.
func shownTeammateAcks(dir string) (map[string]sessionstream.TeammateCommentAck, error) {
	logged, _, err := readAckLog(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]sessionstream.TeammateCommentAck, len(logged))
	for id, o := range logged {
		out[id] = o.ack
	}
	return out, nil
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

func addTeammateResend(dir, id string, now time.Time) error {
	have, _, err := readResends(dir)
	if err != nil {
		return err
	}
	if _, ok := have[id]; ok {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, teammateResendsFileName), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintf(f, "%s\t%d\n", id, now.Unix())
	return errors.Join(werr, f.Close())
}

// clearTeammateResends removes settled ids from the resend list.
func clearTeammateResends(dir string, settled []sessionstream.TeammateCommentAck) error {
	path := filepath.Join(dir, teammateResendsFileName)
	since, order, err := readResends(dir)
	if err != nil || len(order) == 0 {
		return err
	}
	drop := make(map[string]bool, len(settled))
	for _, a := range settled {
		drop[a.ID] = true
	}
	var keep []string
	for _, id := range order {
		if !drop[id] {
			keep = append(keep, fmt.Sprintf("%s\t%d", id, since[id].Unix()))
		}
	}
	if len(keep) == len(order) {
		return nil
	}
	body := ""
	if len(keep) > 0 {
		body = strings.Join(keep, "\n") + "\n"
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
	obligations, err := ackObligations(s.dir)
	if err != nil {
		s.log().WarnContext(ctx, "live_teammate_acks_unreadable", "error", err)
		return
	}
	owed := make([]sessionstream.TeammateCommentAck, len(obligations))
	for i, o := range obligations {
		owed[i] = o.ack
	}
	s.sendAcks(ctx, owed)
}

func (s *session) sendAcks(ctx context.Context, owed []sessionstream.TeammateCommentAck) {
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

// teammateAcksGiveUpAfter bounds how long one receipt is retried after the
// stream closed: a day of failures is no longer a transient outage. It is
// measured from when THAT receipt began to be owed.
const teammateAcksGiveUpAfter = 24 * time.Hour

// closedStream loads a closed stream's state and authorizes it (the minted
// session binding) BEFORE any receipt log is read: the session cache sits inside
// a pulled Ledger, so a state and its logs can come from someone else.
// ok is false for a stream that is not closed.
func closedStream(ctx context.Context, cfg Config, dir string) (s *session, ok bool, err error) {
	s, err = loadSession(cfg, dir)
	if err != nil {
		return nil, false, err
	}
	if s.st.Status != StatusClosed {
		return nil, false, nil
	}
	if err := authorize(ctx, cfg, dir, s.st); err != nil {
		return nil, false, err
	}
	return s, true, nil
}

// TeammateAcksOwed reports whether a closed stream this machine minted still
// owes the server a delivery ack. A log that cannot be read is an error, never
// "nothing owed".
func TeammateAcksOwed(ctx context.Context, cfg Config, dir string) (bool, error) {
	cfg, err := cfg.withDefaults()
	if err != nil {
		return false, err
	}
	_, ok, err := closedStream(ctx, cfg, dir)
	if err != nil || !ok {
		return false, err
	}
	owed, err := ackObligations(dir)
	return len(owed) > 0, err
}

// FlushTeammateAcks sends the acks a stream owes after it ended (closed), for
// the daemon's sweep. attempted reports that receipts were owed (so the caller
// can bound how many sessions it works per sweep). Only a stream this machine
// minted is contacted, and authorization comes before any log is read. Each
// receipt is retried for 24 hours from when it began to be owed, then given up
// once, with one Warn line, so a dead server does not keep a finished session in
// the sweep forever.
func FlushTeammateAcks(ctx context.Context, cfg Config, dir string, budget time.Duration, now time.Time) (attempted bool, err error) {
	cfg, err = cfg.withDefaults()
	if err != nil {
		return false, err
	}
	// a stat, not a parse: nothing untrusted is read for a folder that never
	// logged a receipt
	if !hasReceiptLog(dir) {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	err = withLock(ctx, dir, budget, func() error {
		s, ok, err := closedStream(ctx, cfg, dir)
		if err != nil || !ok {
			return err
		}
		obligations, err := ackObligations(dir)
		if err != nil {
			return err
		}
		var owed, expired []sessionstream.TeammateCommentAck
		for _, o := range obligations {
			if now.Sub(o.since) > teammateAcksGiveUpAfter {
				expired = append(expired, o.ack)
			} else {
				owed = append(owed, o.ack)
			}
		}
		if len(expired) > 0 {
			s.log().WarnContext(ctx, "live_teammate_acks_given_up", "count", len(expired), "older_than", teammateAcksGiveUpAfter.String())
			s.settleTeammateAcks(ctx, expired)
		}
		if len(owed) == 0 {
			return nil
		}
		attempted = true
		c, err := cfg.newClient(ctx, cfg.Endpoint)
		if err != nil {
			return fmt.Errorf("live client: %w", err)
		}
		s.c = c
		s.sendAcks(ctx, owed)
		return nil
	})
	return attempted, err
}

func hasReceiptLog(dir string) bool {
	for _, name := range []string{teammateAcksFileName, teammateResendsFileName} {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && info.Size() > 0 {
			return true
		}
	}
	return false
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
