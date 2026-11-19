package livesession

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"

	"github.com/sageox/ox/internal/fileutil"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/secretguard"
)

// Bytes sent to the live API are a publication, so they pass the same secret
// scan as Ledger and LFS publication (internal/lfs prepareUploads,
// internal/gitutil ValidateSessionPublication): the bundled catalog, then the
// rules current upstream adds. Capture-time redaction only knows the bundled
// catalog, so it cannot stand in for this check.
//
// Unlike Ledger publication, a stream never waits on the network for the
// current rules (F2). The daemon refreshes them in the background
// (RefreshSecretRules) and keeps the last catalog it validated in the ox data
// dir; every publishing process, the daemon's drains and the CLI's stop-time
// close alike, scans with the delta of that file (loadRules). A stream
// publishes on every tool call; waiting on raw.githubusercontent.com there
// meant a network that blocks it streamed nothing, and an upstream outage
// stalled every live session at once. The accepted cost: until a refresh has
// succeeded once on this machine, the scan is the bundled catalog alone. The
// server's own scan of every append is the second layer.

// scanName is the content name handed to the guard. Path-only rules match it,
// so it is a name no sensitive-file rule names: the one Ledger publication uses.
const scanName = "sessions/content"

// rulesFileName is the last upstream catalog a refresh validated, in the ox
// data dir beside the minted-conversation registry. It is the whole catalog,
// not the delta: a compiled rule set has no file form, and the delta depends
// on the bundled catalog of the binary that loads it.
const rulesFileName = "live-secret-rules.toml"

// maxRulesFileBytes matches the bound secretguard puts on a fetched catalog: a
// larger file is not one a refresh wrote.
const maxRulesFileBytes = 4 << 20

// currentCatalog fetches and validates the current upstream catalog; tests
// replace it.
var currentCatalog = secretguard.LatestCatalog

// rulesSnapshot is the delta compiled from one version of the persisted catalog.
type rulesSnapshot struct {
	path string
	// file is the catalog file the delta came from. Nil when this process
	// refreshed but could not persist: its delta is then newer than the file
	// as it stood then (unsaved), until the file changes.
	file os.FileInfo
	// unsaved is the file as it stood when this process could not persist its
	// refresh; nil when there was none. Only for a snapshot with no file.
	unsaved os.FileInfo
	delta   *secretguard.RuleSet // nil: the bundled catalog alone
}

// loadedRules caches the last snapshot, so the catalog is compiled again only
// when the file changed, not on every drain.
var loadedRules atomic.Pointer[rulesSnapshot]

func rulesPath(dataDir string) string { return SecretRulesPath(dataDir) }

// SecretRulesPath is the file holding the last upstream secret catalog a
// refresh validated, in dataDir (empty: the ox data dir).
func SecretRulesPath(dataDir string) string {
	if dataDir == "" {
		dataDir = paths.DataDir()
	}
	return filepath.Join(dataDir, rulesFileName)
}

// RefreshSecretRules fetches the current upstream catalog and keeps it in
// dataDir (empty: the ox data dir) for every later publication check on this
// machine, in this process and others. A failure keeps the last good catalog.
// It waits on the network, so only a background loop may call it, never a drain.
func RefreshSecretRules(ctx context.Context, dataDir string) error {
	catalog, err := currentCatalog(ctx)
	if err != nil {
		return fmt.Errorf("refresh secret rules: %w", err)
	}
	delta, err := catalogDelta(catalog)
	if err != nil {
		return fmt.Errorf("refresh secret rules: %w", err)
	}
	path := rulesPath(dataDir)
	file, err := saveCatalog(ctx, path, catalog)
	if err != nil {
		// this process scans with what it fetched even if no other process
		// can, until another process keeps a catalog of its own
		unsaved, _ := os.Stat(path)
		loadedRules.Store(&rulesSnapshot{path: path, unsaved: unsaved, delta: delta})
		return fmt.Errorf("refresh secret rules: keep them for other processes: %w", err)
	}
	loadedRules.Store(&rulesSnapshot{path: path, file: file, delta: delta})
	return nil
}

// catalogDelta compiles a validated catalog into what it adds to the bundled one.
func catalogDelta(catalog []byte) (*secretguard.RuleSet, error) {
	rules, err := secretguard.NewRules(catalog)
	if err != nil {
		return nil, err
	}
	return rules.Delta(secretguard.Bundled()), nil
}

// saveCatalog writes catalog to path atomically, under the file's lock: one
// daemon runs per repository, and they share the data dir.
func saveCatalog(ctx context.Context, path string, catalog []byte) (os.FileInfo, error) {
	if err := fileutil.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	var info os.FileInfo
	err := fileutil.WithFileLockTimeout(ctx, path, pendingLockWait, func() error {
		if err := fileutil.AtomicWriteBytes(path, catalog, 0o600); err != nil {
			return err
		}
		var err error
		info, err = os.Stat(path)
		return err
	})
	return info, err
}

// loadRules returns the delta of the persisted catalog for a publication
// check. It never touches the network. No file means no refresh has succeeded
// on this machine yet; a file that cannot be read or compiled is logged once
// per version, and the scan is the bundled catalog alone until the next
// refresh replaces it.
func loadRules(ctx context.Context, dataDir string, log *slog.Logger) *secretguard.RuleSet {
	path := rulesPath(dataDir)
	snap := loadedRules.Load()
	if snap != nil && snap.path != path {
		snap = nil
	}
	info, err := os.Stat(path)
	if snap != nil && snap.file == nil && (err != nil || snap.unsaved != nil && sameVersion(snap.unsaved, info)) {
		// this process refreshed but could not keep it, and no other process
		// has kept a catalog since: what it fetched is the newest there is
		return snap.delta
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		log.WarnContext(ctx, "live_secret_rules_unreadable", "impact", "this scan uses the bundled catalog alone", "error", err)
		return nil
	}
	if snap != nil && snap.file != nil && sameVersion(snap.file, info) {
		return snap.delta
	}
	delta, err := readCatalogDelta(path)
	if err != nil {
		log.WarnContext(ctx, "live_secret_rules_unreadable", "impact", "live streams scan with the bundled catalog alone until the next refresh", "error", err)
	}
	loadedRules.Store(&rulesSnapshot{path: path, file: info, delta: delta})
	return delta
}

// sameVersion reports whether two stats name one version of a file. A refresh
// renames a new file into place, so the inode alone tells versions apart; size
// and time guard filesystems that reuse inodes.
func sameVersion(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func readCatalogDelta(path string) (*secretguard.RuleSet, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only: nothing to flush
	catalog, err := io.ReadAll(io.LimitReader(f, maxRulesFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(catalog) > maxRulesFileBytes {
		return nil, fmt.Errorf("%s is over %d bytes", rulesFileName, maxRulesFileBytes)
	}
	return catalogDelta(catalog)
}

// checkPublication refuses content that publication would refuse: the bundled
// catalog, then delta. It does no I/O. A *secretguard.RefusedError means these
// bytes are refused every time under these rules.
func checkPublication(delta *secretguard.RuleSet, content []byte) error {
	if err := secretguard.ValidateContent(scanName, content); err != nil {
		return fmt.Errorf("live upload refused: %w", err)
	}
	if delta != nil {
		if err := delta.ValidateContent(scanName, content); err != nil {
			return fmt.Errorf("live upload refused by current rules: %w", err)
		}
	}
	return nil
}

// checkPublication scans content with the rules persisted when this call
// first needed them; one Drain or Close scans every batch with the same rules.
func (s *session) checkPublication(ctx context.Context, content []byte) error {
	if !s.rulesLoaded {
		s.rules, s.rulesLoaded = loadRules(ctx, s.cfg.DataDir, s.log()), true
	}
	return checkPublication(s.rules, content)
}

// secretWithheldGap stands for n withheld bytes, the newline it keeps not
// counted, as the spool's oversized_line and invalid_line gaps count. The
// viewer keys its "Withheld by SageOx" marker off the reason and the byte
// count, so the line says nothing about what matched.
func secretWithheldGap(n int) []byte {
	return fmt.Appendf(nil, `{"type":"gap","reason":"secret_withheld","bytes":%d}`+"\n", n)
}

// withheldBatch is a batch the publication scan refused and what replaces it.
type withheldBatch struct {
	size  int64  // bytes of the refused batch
	next  []byte // the batch's gap lines, and the one new gap line
	bytes int64  // bytes the new gap line stands for
	// by is the scan that refused it: local_scan, or server (F11). line is the
	// server's first flagged line, a number only; the whole batch is withheld.
	by   string
	line *int
}

// declaredGapLine is exactly a gap line this package writes into the spool: a
// reason and at most a byte count, nothing else. Nothing looser may match: a
// captured line dressed as a gap could otherwise carry content past the scan.
var declaredGapLine = regexp.MustCompile(`^\{"type":"gap","reason":"(?:secret_withheld|spool_write_failed|torn_tail|oversized_line|invalid_line|spool_full)"(?:,"bytes":[0-9]+)?\}\n$`)

// withholdRefused replaces a refused batch with one secret_withheld gap line
// standing for all of its content (F1). Withholding only the lines refused on
// their own lost the context a match spans: a Helm extraEnv name and its value
// on separate lines, a match split across lines, or a gap line longer than the
// line it replaced pushing a line into the next batch, which nothing then scans
// with the rest. A later scan of what is left cannot recover deleted context,
// so no content in a refused batch streams.
//
// Gap lines the batch already holds stay, in order: they carry no content, and
// each declares a loss (an earlier withhold, a spool write that failed, a torn
// tail) that is settled locally and would never be declared again. The new gap
// line takes the place of the batch's first content line.
func withholdRefused(batch []byte) (withheldBatch, error) {
	var next, content []byte
	at := -1 // where in next the new gap line goes
	for line := range bytes.Lines(batch) {
		if declaredGapLine.Match(line) {
			next = append(next, line...)
			continue
		}
		if at < 0 {
			at = len(next)
		}
		content = append(content, line...)
	}
	if at < 0 {
		// only gap lines: replacing them with another one could go on forever
		return withheldBatch{}, errors.New("the secret scan refused a batch of gap lines")
	}
	n := len(bytes.TrimSuffix(content, []byte("\n")))
	gap := secretWithheldGap(n)
	out := make([]byte, 0, len(next)+len(gap))
	out = append(append(append(out, next[:at]...), gap...), next[at:]...)
	return withheldBatch{size: int64(len(batch)), next: out, bytes: int64(n)}, nil
}

// withhold writes s.withheld into the spool and the stream goes on (F1). The
// same bytes are refused every time, and the stream cannot skip them while they
// sit in the spool, so the spool itself changes: a gap line streams in their
// place and declares their size. raw.jsonl keeps them on this machine.
func (s *session) withhold(ctx context.Context) error {
	w := s.withheld
	s.withheld = withheldBatch{}
	if !s.st.SecretWithheld {
		// before the spool changes: a crash in between must still leave stop a
		// reason to tell the person, and an extra notice is harmless
		s.st.SecretWithheld = true
		if err := s.persist(); err != nil {
			return err
		}
	}
	if err := replaceSpoolRange(ctx, s.dir, s.st.Acked, w.size, w.next); err != nil {
		return fmt.Errorf("withhold %d refused bytes at offset %d: %w", w.bytes, s.st.Acked, err)
	}
	// ids and sizes only: the refusal's text and the bytes stay out of every log
	args := []any{"offset", s.st.Acked, "bytes", w.bytes, "by", w.by}
	if w.line != nil {
		args = append(args, "line", *w.line)
	}
	s.log().WarnContext(ctx, "live_session_secret_withheld", args...)
	return nil
}

// publishableSummary returns summary if publication would send it, else "".
// A refused summary is dropped, and the session then has no summary: nothing
// on the server writes one.
func (s *session) publishableSummary(ctx context.Context, summary string) string {
	if summary == "" {
		return ""
	}
	if err := s.checkPublication(ctx, []byte(summary)); err != nil {
		s.log().WarnContext(ctx, "live_session_summary_withheld", "error", err)
		return ""
	}
	return summary
}
