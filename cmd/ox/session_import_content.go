package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/session"
	"github.com/sageox/ox/internal/session/nativeimport"
)

type importSourceSnapshot struct {
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	SHA256  string    `json:"-"` // local review pin; no browser or persisted wire field
}

type importPromptAnchor struct {
	EntryIndex int    `json:"entry_index"`
	Content    string `json:"content"`
}

type importContentPreview struct {
	NativeID       string               `json:"native_id"`
	OpeningRequest string               `json:"opening_request"`
	Prompts        []importPromptAnchor `json:"prompts"`
	LastReply      string               `json:"last_reply"`
	Entries        []session.Entry      `json:"entries"`
	Snapshot       importSourceSnapshot `json:"snapshot"`
}

type importPreviewLoader func(context.Context, string) (*importContentPreview, error)

type importReviewResult struct {
	IDs      []string
	Canceled bool
}

var errImportSourceChanged = errors.New("session changed since discovery; restart the preview")

func importSnapshot(c *importCandidate) importSourceSnapshot {
	if reviewed := c.reviewedSnapshot.Load(); reviewed != nil {
		return *reviewed
	}
	return importSourceSnapshot{Size: c.Session.Size, ModTime: c.Session.ModTime}
}

func snapshotMatches(ctx context.Context, path string, snapshot importSourceSnapshot) bool {
	info, err := os.Stat(path)
	if err != nil || info.Size() != snapshot.Size || !info.ModTime().Equal(snapshot.ModTime) {
		return false
	}
	if snapshot.SHA256 == "" {
		return ctx.Err() == nil
	}
	digest, err := importSourceDigest(ctx, path, snapshot)
	return err == nil && digest == snapshot.SHA256
}

// Hash only a requested preview, with fixed memory and at most the discovered
// file size. Stat and identity checks reject growth/truncation/replacement
// during the stream; callers also compare the digest around adapter reads.
func importSourceDigest(ctx context.Context, path string, snapshot importSourceSnapshot) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errImportSourceChanged
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() != snapshot.Size || !before.ModTime().Equal(snapshot.ModTime) {
		return "", errImportSourceChanged
	}
	hash := sha256.New()
	buffer := make([]byte, 32*1024)
	remaining := snapshot.Size
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		amount := len(buffer)
		if remaining < int64(amount) {
			amount = int(remaining)
		}
		n, readErr := file.Read(buffer[:amount])
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
			remaining -= int64(n)
		}
		if readErr != nil && (readErr != io.EOF || remaining != 0) {
			return "", errImportSourceChanged
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || after.Size() != snapshot.Size || !after.ModTime().Equal(snapshot.ModTime) {
		return "", errImportSourceChanged
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// The cache lives only for this review. Its entries are immutable and each
// request checks the discovered source snapshot, including cached responses.
func newImportPreviewLoader(env *importEnv, cands []*importCandidate) importPreviewLoader {
	byID := make(map[string]*importCandidate, len(cands))
	for _, c := range cands {
		byID[c.Session.NativeID] = c
	}
	var mu sync.Mutex
	cache := make(map[string]*importContentPreview)
	var cacheOrder []string
	var cacheBytes int
	reads := make(chan struct{}, importPreviewConcurrency)
	return func(ctx context.Context, id string) (*importContentPreview, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c, ok := byID[id]
		if !ok {
			return nil, errors.New("unknown session")
		}
		snapshot := importSnapshot(c)
		if !snapshotMatches(ctx, c.Session.Path, importSourceSnapshot{Size: snapshot.Size, ModTime: snapshot.ModTime}) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, errImportSourceChanged
		}
		select {
		case reads <- struct{}{}:
			defer func() { <-reads }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		snapshot = importSnapshot(c)
		digest, err := importSourceDigest(ctx, c.Session.Path, snapshot)
		if err != nil {
			return nil, err
		}
		if snapshot.SHA256 != "" && digest != snapshot.SHA256 {
			return nil, errImportSourceChanged
		}
		snapshot.SHA256 = digest
		// A concurrent visible-row/full-detail request may have filled the cache
		// while this request waited for a reader slot.
		mu.Lock()
		cached := cache[id]
		mu.Unlock()
		if cached != nil {
			if cached.Snapshot != snapshot {
				return nil, errImportSourceChanged
			}
			return cached, nil
		}
		raw, err := env.deps.readNative(c.Session.Agent, c.Session.Path)
		if err != nil {
			return nil, errors.New("session content is unavailable")
		}
		entries, err := nativeimport.PreviewEntries(env.projectRoot, raw)
		if err != nil {
			return nil, errors.New("session preview could not be redacted safely")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !snapshotMatches(ctx, c.Session.Path, snapshot) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, errImportSourceChanged
		}
		if !c.reviewedSnapshot.CompareAndSwap(nil, &snapshot) && *c.reviewedSnapshot.Load() != snapshot {
			return nil, errImportSourceChanged
		}
		p := &importContentPreview{NativeID: id, Entries: entries, Snapshot: snapshot, Prompts: []importPromptAnchor{}}
		for i, e := range entries {
			switch e.Type {
			case session.EntryTypeUser:
				if isImportContextPrompt(e.Content) || strings.TrimSpace(e.Content) == "" {
					continue
				}
				if p.OpeningRequest == "" {
					p.OpeningRequest = e.Content
				}
				p.Prompts = append(p.Prompts, importPromptAnchor{EntryIndex: i, Content: e.Content})
			case session.EntryTypeAssistant:
				if strings.TrimSpace(e.Content) != "" {
					p.LastReply = e.Content
				}
			}
		}
		mu.Lock()
		const cacheBudget = 32 << 20
		cost := importPreviewBytes(p)
		if cache[id] == nil && cost <= cacheBudget {
			for cacheBytes+cost > cacheBudget && len(cacheOrder) > 0 {
				old := cacheOrder[0]
				cacheOrder = cacheOrder[1:]
				cacheBytes -= importPreviewBytes(cache[old])
				delete(cache, old)
			}
			cache[id] = p
			cacheOrder = append(cacheOrder, id)
			cacheBytes += cost
		}
		mu.Unlock()
		return p, nil
	}
}

func importPreviewBytes(p *importContentPreview) int {
	if p == nil {
		return 0
	}
	n := 256 + len(p.NativeID) + len(p.OpeningRequest) + len(p.LastReply)
	for _, e := range p.Entries {
		n += 256 + len(e.Content) + len(e.ToolInput) + len(e.ToolOutput) + len(e.ToolName) + len(e.CallID)
	}
	for _, prompt := range p.Prompts {
		n += 32 + len(prompt.Content)
	}
	return n
}

// Known vendor bootstrap messages remain readable in Entries, but do not
// become the session's label or its human prompt navigation anchors.
func isImportContextPrompt(content string) bool {
	s := strings.TrimSpace(content)
	for _, prefix := range []string{"<environment_context>", "<permissions instructions>", "<instructions>", "<system-reminder>", "<local-command-caveat>", "<command-name>", "# AGENTS.md instructions"} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func renderImportContent(out io.Writer, opts importOptions, dest importDestination, c *importCandidate, p *importContentPreview) error {
	if opts.jsonOut {
		return cli.PrintJSONTo(out, struct {
			Status      string                `json:"status"`
			Destination importDestination     `json:"destination"`
			Session     importJSONSession     `json:"session"`
			Preview     *importContentPreview `json:"preview"`
		}{Status: "content_preview", Destination: dest, Session: jsonImportSession(c), Preview: p})
	}
	fmt.Fprintf(out, "Session %s (%s) · %s\n", c.Session.NativeID, c.Session.Agent, c.State)
	fmt.Fprintln(out, "Read-only preview. Nothing was uploaded; no summary was generated.")
	fmt.Fprintln(out, "Import retains the redacted conversation and available tool activity, then generates a summary after confirmation.")
	fmt.Fprintln(out, "\nOpening request\n"+sanitizeImportText(p.OpeningRequest))
	fmt.Fprintln(out, "\nHuman prompts")
	for i, prompt := range p.Prompts {
		fmt.Fprintf(out, "%d. %s\n", i+1, sanitizeImportText(prompt.Content))
	}
	fmt.Fprintln(out, "\nLast AI reply\n"+sanitizeImportText(p.LastReply))
	return nil
}

func validateImportReview(ctx context.Context, cands []*importCandidate, result importReviewResult) (map[string]importSourceSnapshot, error) {
	ready := make(map[string]*importCandidate)
	for _, c := range cands {
		if c.State == stateReady {
			ready[c.Session.NativeID] = c
		}
	}
	snapshots := make(map[string]importSourceSnapshot, len(result.IDs))
	for _, id := range result.IDs {
		c, ok := ready[id]
		if !ok {
			return nil, errors.New("selection contains a session that is not ready")
		}
		if _, duplicate := snapshots[id]; duplicate {
			return nil, errors.New("selection contains a duplicate session")
		}
		snapshot := importSnapshot(c)
		if !snapshotMatches(ctx, c.Session.Path, snapshot) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, errImportSourceChanged
		}
		snapshots[id] = snapshot
	}
	return snapshots, nil
}
