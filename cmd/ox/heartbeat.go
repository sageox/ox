package main

import (
	"encoding/json"
	"time"

	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/gitserver"
	"github.com/sageox/ox/internal/version"
)

// Heartbeat Design: IPC over tmp files
//
// We use daemon IPC (unix socket) rather than a tmp log file because:
// - No cleanup: tmp files leak on crashes, daemon memory is self-cleaning
// - No polling: daemon accumulates in real-time, no file watchers needed
// - No contention: concurrent agents would need file locking; IPC is serialized by the daemon
// - Existing infra: daemon already has heartbeat processing, socket, and activity tracking
//
// TODO: consolidate Heartbeat, sendContextHeartbeat, and HeartbeatWithCreds into a single
// call. Currently sendContextHeartbeat fires a second IPC message because context tokens
// aren't known until after the command completes, while Heartbeat fires at command start.
// A post-command heartbeat that includes both credentials and token count would halve
// the IPC traffic per agent command.

// Heartbeat sends async context to daemon. Never blocks CLI.
// This is fire-and-forget: the goroutine handles the send in background,
// and the calling function returns immediately.
// agentID is optional - pass empty string if no agent session exists.
func Heartbeat(repoPath string, teamIDs []string, agentID string) {
	go func() {
		client := daemon.NewClientWithTimeout(50 * time.Millisecond)

		payload := daemon.HeartbeatPayload{
			RepoPath:   repoPath,
			TeamIDs:    teamIDs,
			AgentID:    agentID,
			Timestamp:  time.Now(),
			CLIVersion: version.Full(),
		}

		// include workspace ID if available
		if wsID := daemon.CurrentWorkspaceID(); wsID != "" {
			payload.WorkspaceID = wsID
		}

		// include credentials if available (refreshes daemon's copy)
		// use project endpoint to get the correct token for this repo
		projectEndpoint := endpoint.GetForProject(repoPath)
		if creds, err := gitserver.LoadCredentialsForEndpoint(projectEndpoint); err == nil && creds != nil {
			hbCreds := &daemon.HeartbeatCreds{
				Token:     creds.Token,
				ServerURL: creds.ServerURL,
				ExpiresAt: creds.ExpiresAt,
			}
			if token, err := auth.GetTokenForEndpoint(projectEndpoint); err == nil && token != nil {
				hbCreds.AuthToken = token.AccessToken
				hbCreds.UserEmail = token.UserInfo.Email
				hbCreds.UserID = token.UserInfo.UserID
			}
			payload.Credentials = hbCreds
		}

		data, err := json.Marshal(payload)
		if err != nil {
			return // shouldn't happen
		}

		// fire-and-forget: ignore all errors
		_ = client.SendOneWay(daemon.Message{
			Type:    daemon.MsgTypeHeartbeat,
			Payload: data,
		})
	}()
}

// sendContextHeartbeat fires a lightweight heartbeat with estimated token count.
// Takes raw bytes produced by the command, converts to tokens (~4 bytes/token),
// then sends to daemon. Fire-and-forget: never blocks CLI.
//
// TODO: fold into a unified post-command heartbeat (see consolidation note above).
func sendContextHeartbeat(agentID string, bytes int64, commandName string) {
	tokens := estimateTokens(bytes)
	if tokens <= 0 {
		return
	}
	go func() {
		client := daemon.NewClientWithTimeout(50 * time.Millisecond)
		payload := daemon.HeartbeatPayload{
			AgentID:       agentID,
			ContextTokens: int64(tokens),
			CommandName:   commandName,
			Timestamp:     time.Now(),
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return
		}
		_ = client.SendOneWay(daemon.Message{
			Type:    daemon.MsgTypeHeartbeat,
			Payload: data,
		})
	}()
}

// HeartbeatWithCreds is like Heartbeat but with pre-loaded credentials.
// Use this when credentials are already available to avoid re-loading.
// agentID is optional - pass empty string if no agent session exists.
func HeartbeatWithCreds(repoPath string, teamIDs []string, agentID string, creds *gitserver.GitCredentials) {
	go func() {
		client := daemon.NewClientWithTimeout(50 * time.Millisecond)

		payload := daemon.HeartbeatPayload{
			RepoPath:   repoPath,
			TeamIDs:    teamIDs,
			AgentID:    agentID,
			Timestamp:  time.Now(),
			CLIVersion: version.Full(),
		}

		// include workspace ID if available
		if wsID := daemon.CurrentWorkspaceID(); wsID != "" {
			payload.WorkspaceID = wsID
		}

		// include provided credentials
		if creds != nil {
			hbCreds := &daemon.HeartbeatCreds{
				Token:     creds.Token,
				ServerURL: creds.ServerURL,
				ExpiresAt: creds.ExpiresAt,
			}
			// include auth token and user info for API calls
			// use project endpoint to get the correct token for this repo
			projectEndpoint := endpoint.GetForProject(repoPath)
			if token, err := auth.GetTokenForEndpoint(projectEndpoint); err == nil && token != nil {
				hbCreds.AuthToken = token.AccessToken
				hbCreds.UserEmail = token.UserInfo.Email
				hbCreds.UserID = token.UserInfo.UserID
			}
			payload.Credentials = hbCreds
		}

		data, err := json.Marshal(payload)
		if err != nil {
			return
		}

		_ = client.SendOneWay(daemon.Message{
			Type:    daemon.MsgTypeHeartbeat,
			Payload: data,
		})
	}()
}
