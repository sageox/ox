package main

import (
	"context"
	"testing"

	"github.com/sageox/agentx"
)

// TestBuzzOrchestratorRegistered guards the agentx v0.1.11 bump: the Buzz
// orchestrator must be registered in the default registry (populated via the
// `_ "github.com/sageox/agentx/setup"` blank import in main.go) and must detect
// ORCHESTRATOR_ENV=buzz. This is what lets `ox agent prime` stamp
// X-Orchestrator: buzz (cmd/ox/agent_prime.go).
//
// Uses DetectByType (not OrchestratorType) on purpose: it targets a specific
// agent type, so the assertion stays deterministic even on a host that also
// sets another orchestrator's env vars (e.g. CONDUCTOR_WORKSPACE_NAME), where
// the global DetectOrchestrator winner would be registry-iteration-order
// dependent. DetectByType also returns an error for an unregistered type, so the
// unregistered-type row both documents that contract and proves the buzz row's
// no-error result is a meaningful registration signal (not a vacuous pass).
func TestBuzzOrchestratorRegistered(t *testing.T) {
	tests := []struct {
		name         string
		agentType    agentx.AgentType
		orchestrator string
		wantErr      bool
		wantDetected bool
	}{
		{
			name:         "buzz registered and detects ORCHESTRATOR_ENV=buzz",
			agentType:    agentx.AgentTypeBuzz,
			orchestrator: "buzz",
			wantDetected: true,
		},
		{
			name:         "unregistered type returns not-registered error",
			agentType:    agentx.AgentType("definitely-not-an-orchestrator"),
			orchestrator: "buzz",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ORCHESTRATOR_ENV", tt.orchestrator)

			detected, err := agentx.NewDetector().DetectByType(context.Background(), tt.agentType)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("DetectByType(%q) = nil error, want a not-registered error", tt.agentType)
				}
				return
			}
			if err != nil {
				t.Fatalf("DetectByType(%q) errored — Buzz orchestrator not registered? %v", tt.agentType, err)
			}
			if detected != tt.wantDetected {
				t.Fatalf("DetectByType(%q) detected = %v, want %v", tt.agentType, detected, tt.wantDetected)
			}
		})
	}
}
