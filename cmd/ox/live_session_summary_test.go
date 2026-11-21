package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sageox/ox/internal/livesession"
	"github.com/sageox/ox/internal/livesession/livesessiontest"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/stretchr/testify/require"
)

func TestLiveConversationEntriesCoverTheRestatedSession(t *testing.T) {
	srv := livesessiontest.New(t)
	cfg := srv.Config(t)
	sessions := t.TempDir()
	first := filepath.Join(sessions, "first")
	require.NoError(t, os.Mkdir(first, 0o755))
	firstID := sessionid.GenerateSessionID()
	_, err := livesession.Prepare(t.Context(), cfg, first, livesession.OpenParams{SessionID: firstID, SessionName: "first", RepoID: "repo_test", Agent: "claude-code", StartedAt: time.Now()})
	require.NoError(t, err)
	earlier := `{"type":"header","metadata":{"session_id":"first"}}` + "\n" +
		`{"type":"user","content":"implement authentication for the upload service"}` + "\n" +
		`{"type":"assistant","content":"authentication implemented"}` + "\n"
	_, err = livesession.Spool(t.Context(), first, []byte(earlier), livesession.SpoolOptions{})
	require.NoError(t, err)
	res, err := livesession.Close(t.Context(), cfg, first, "", 5*time.Second)
	require.NoError(t, err)
	require.True(t, res.Done)

	src, err := livesession.FindReopenSource(t.Context(), cfg, sessions, firstID)
	require.NoError(t, err)
	require.NotNil(t, src)
	second := filepath.Join(sessions, "second")
	require.NoError(t, os.Mkdir(second, 0o755))
	_, err = livesession.PrepareReopen(t.Context(), cfg, second, livesession.OpenParams{SessionID: sessionid.GenerateSessionID()}, src)
	require.NoError(t, err)
	_, err = livesession.Spool(t.Context(), second, []byte(`{"type":"user","content":"fix a typo"}`+"\n"), livesession.SpoolOptions{})
	require.NoError(t, err)

	ownLines := []map[string]any{{"type": "user", "content": "fix a typo"}}
	got := liveConversationEntries(second, ownLines)
	var text strings.Builder
	for _, e := range got {
		fmt.Fprint(&text, e["content"])
	}
	require.Contains(t, text.String(), "implement authentication", "the summary must cover the earlier recording")
	require.Contains(t, text.String(), "fix a typo")

	// a stream that was never reopened summarizes its own lines, as before
	require.Equal(t, ownLines, liveConversationEntries(first, ownLines))
}
