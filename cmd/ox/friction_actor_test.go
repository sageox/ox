package main

import (
	"testing"

	"github.com/sageox/agentx"
	friction "github.com/sageox/frictionax"
	"github.com/stretchr/testify/assert"
)

// TestOxActorDetector_EnvSignalsOnly guards the actor sent with every
// `ox command run` and friction event.
//
// Failure prevented: a person running ox in a repository that has a .codex/
// directory (ox writes .codex/hooks.json there) filed as an AI coworker,
// because agentx's Codex detector falls back to that directory.
func TestOxActorDetector_EnvSignalsOnly(t *testing.T) {
	tests := []struct {
		name      string
		vars      map[string]string
		dirs      []string
		wantActor friction.Actor
		wantType  string
	}{
		{name: "person", wantActor: friction.ActorHuman},
		{
			name:      "person in a repo with .codex",
			vars:      map[string]string{"PWD": "/repo"},
			dirs:      []string{".codex", "/repo/.codex"},
			wantActor: friction.ActorHuman,
		},
		{name: "claude code", vars: map[string]string{"CLAUDECODE": "1"}, wantActor: friction.ActorAgent, wantType: "claude"},
		{name: "codex runtime", vars: map[string]string{"CODEX_THREAD_ID": "t1"}, wantActor: friction.ActorAgent, wantType: "codex"},
		{
			name:      "claude code in a repo with .codex",
			vars:      map[string]string{"CLAUDECODE": "1", "PWD": "/repo"},
			dirs:      []string{".codex", "/repo/.codex"},
			wantActor: friction.ActorAgent,
			wantType:  "claude",
		},
		{name: "explicit AGENT_ENV", vars: map[string]string{"AGENT_ENV": "codex"}, wantActor: friction.ActorAgent, wantType: "codex"},
		{name: "ci", vars: map[string]string{"CI": "true"}, wantActor: friction.ActorAgent, wantType: "ci"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := agentx.NewMockEnvironment(tt.vars)
			env.ExistingDirs = map[string]bool{}
			for _, d := range tt.dirs {
				env.ExistingDirs[d] = true
			}
			actor, agentType := oxActorDetector{env: env}.DetectActor()
			assert.Equal(t, tt.wantActor, actor)
			assert.Equal(t, tt.wantType, agentType)
		})
	}
}
