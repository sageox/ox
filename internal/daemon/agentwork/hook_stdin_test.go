package agentwork

import "github.com/sageox/ox/internal/session"

// hookStdin is the capture options of a Claude Code hook whose stdin names
// transcript (transcript_path) for native session sessionID (session_id).
func hookStdin(transcript, sessionID string) session.HookCaptureOptions {
	return session.HookCaptureOptions{Transcript: &session.HookTranscript{Path: transcript, SessionID: sessionID}}
}
