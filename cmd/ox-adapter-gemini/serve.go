// serve.go handles serve mode for incremental session reading.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/sageox/ox/pkg/adapterprotocol"
	"github.com/sageox/ox/pkg/adapterruntime"
)

type geminiSessionState struct {
	sessionFile string
	entryCount  int64
}

// geminiReadFromOffset re-parses the JSON session file and returns entries past the offset.
// Gemini rewrites the entire file each turn, so byte offsets don't apply.
func geminiReadFromOffset(file string, offset int64) ([]adapterprotocol.RawEntry, int64, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, offset, fmt.Errorf("failed to read session file: %w", err)
	}

	allEntries, _, err := parseGeminiSession(data)
	if err != nil {
		return nil, offset, err
	}

	total := int64(len(allEntries))
	if offset < 0 {
		return nil, total, fmt.Errorf("invalid negative offset: %d", offset)
	}
	if offset >= total {
		return nil, total, nil
	}

	return allEntries[int(offset):], total, nil
}

func handleServe(srv *adapterruntime.Server) {
	store := adapterruntime.NewSessionStore[*geminiSessionState]()

	fw, err := adapterruntime.NewFileWatcher(srv.Writer(), geminiReadFromOffset)
	if err != nil {
		log.Printf("file watcher unavailable: %v", err)
	}
	if fw != nil {
		defer fw.Close()
	}

	srv.OnFindSession(func(ctx context.Context, p adapterprotocol.FindSessionParams) (*adapterprotocol.FindSessionResult, error) {
		sessionFile, err := findGeminiSession(p.RepoRoot, p.AgentID, p.Since, p.AgentSessionID)
		if err != nil {
			return nil, err
		}

		// determine initial entry count
		var offset int64
		if data, err := os.ReadFile(sessionFile); err == nil {
			if entries, _, err := parseGeminiSession(data); err == nil {
				offset = int64(len(entries))
			}
		}

		store.Set(p.AgentID, &geminiSessionState{sessionFile: sessionFile, entryCount: offset})

		if fw != nil {
			if werr := fw.Watch(p.AgentID, sessionFile, offset); werr != nil {
				log.Printf("file watcher: failed to watch %s: %v", sessionFile, werr)
			}
		}

		return &adapterprotocol.FindSessionResult{SessionFile: sessionFile, Offset: offset}, nil
	})

	srv.OnReadFromOffset(func(ctx context.Context, p adapterprotocol.ReadFromOffsetParams) (*adapterprotocol.ReadFromOffsetResult, error) {
		state, ok := store.Get(p.AgentID)
		if !ok {
			return nil, adapterruntime.ErrSessionNotFound
		}

		entries, total, err := geminiReadFromOffset(state.sessionFile, p.Offset)
		if err != nil {
			return nil, err
		}

		state.entryCount = total

		return &adapterprotocol.ReadFromOffsetResult{Entries: entries, NewOffset: total}, nil
	})

	srv.OnEndSession(func(ctx context.Context, p adapterprotocol.EndSessionParams) error {
		if fw != nil {
			fw.Unwatch(p.AgentID)
		}
		store.Delete(p.AgentID)
		return nil
	})

	srv.Serve()
}
