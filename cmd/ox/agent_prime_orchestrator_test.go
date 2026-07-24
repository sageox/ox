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
// Uses DetectByType (not OrchestratorType) on purpose: it targets the Buzz
// agent directly, so the assertion stays deterministic even on a host that also
// sets another orchestrator's env vars (e.g. CONDUCTOR_WORKSPACE_NAME), where
// the global DetectOrchestrator winner would be registry-iteration-order
// dependent. DetectByType also returns an error if Buzz was never registered,
// so this catches a dropped setup.go registration too.
func TestBuzzOrchestratorRegistered(t *testing.T) {
	t.Setenv("ORCHESTRATOR_ENV", "buzz")

	detected, err := agentx.NewDetector().DetectByType(context.Background(), agentx.AgentTypeBuzz)
	if err != nil {
		t.Fatalf("DetectByType(buzz) errored — Buzz orchestrator not registered? %v", err)
	}
	if !detected {
		t.Fatal("Buzz orchestrator did not detect ORCHESTRATOR_ENV=buzz after agentx v0.1.11 bump")
	}
}
