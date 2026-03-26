package main

import (
	"os"
	"strings"
	"testing"

	friction "github.com/sageox/frictionax"
)

func TestGetCatalogCachePath_ContainsSageox(t *testing.T) {
	t.Parallel()

	path := getCatalogCachePath()
	if path == "" {
		t.Error("expected non-empty cache path")
	}
	if !strings.Contains(path, "sageox") {
		t.Errorf("expected path to contain 'sageox', got %q", path)
	}
	if !strings.HasSuffix(path, "friction-catalog.json") {
		t.Errorf("expected path to end with 'friction-catalog.json', got %q", path)
	}
}

func TestOxActorDetector_ReturnsValidActor(t *testing.T) {
	// the detector's behavior depends on process environment (parent process, env vars)
	// so we verify it returns a valid actor type rather than a specific value
	detector := oxActorDetector{}
	actor, _ := detector.DetectActor()

	validActors := map[friction.Actor]bool{
		friction.ActorHuman: true,
		friction.ActorAgent: true,
	}
	if !validActors[actor] {
		t.Errorf("DetectActor returned unexpected actor type: %v", actor)
	}
}

func TestOxActorDetector_ImplementsInterface(t *testing.T) {
	t.Parallel()

	// verify oxActorDetector satisfies the friction.ActorDetector interface
	var _ interface {
		DetectActor() (friction.Actor, string)
	} = oxActorDetector{}
}

func TestSendFrictionEvent_DisabledViaSAGEOX_FRICTION_Env(t *testing.T) {
	t.Setenv("SAGEOX_FRICTION", "false")

	event := &friction.FrictionEvent{
		Kind:    "test",
		Command: "ox test",
	}
	// should return early without panic or error
	sendFrictionEvent(event)
}

func TestGetCatalogCachePath_WithFallback(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		t.Setenv("XDG_CACHE_HOME", "")
	}

	path := getCatalogCachePath()
	if path == "" {
		t.Error("expected non-empty cache path even with fallback")
	}
}
