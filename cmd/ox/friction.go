package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/agentx"
	friction "github.com/sageox/frictionax"
	frictioncobra "github.com/sageox/frictionax/adapters/cobra"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/daemon"
	"github.com/spf13/cobra"
)

//go:embed default_catalog.json
var defaultCatalogJSON []byte

var frictionEngine *friction.Friction

// initFriction initializes the friction handling system.
// Should be called after rootCmd is fully initialized.
//
// Loading order:
// 1. Embedded default catalog (bundled with CLI release)
// 2. Cache file overlay (user's cached catalog from server sync, via WithCachePath)
func initFriction(rootCmd *cobra.Command) {
	adapter := frictioncobra.NewCobraAdapter(rootCmd)

	frictionEngine = friction.New(adapter,
		friction.WithCatalog("ox"),
		friction.WithCachePath(getCatalogCachePath()),
		friction.WithActorDetector(&oxActorDetector{}),
	)

	// load embedded default catalog (baseline patterns for offline/first-run)
	if len(defaultCatalogJSON) > 0 {
		var data friction.CatalogData
		if err := json.Unmarshal(defaultCatalogJSON, &data); err == nil {
			_ = frictionEngine.UpdateCatalog(data)
		}
	}
}

// getCatalogCachePath returns the path to the friction catalog cache file.
// Uses os.UserCacheDir() for cross-platform support.
func getCatalogCachePath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		cacheDir = filepath.Join(homeDir, ".cache")
	}
	return filepath.Join(cacheDir, "sageox", "friction-catalog.json")
}

// oxActorDetector uses agentx for agent detection in friction events.
type oxActorDetector struct {
	env agentx.Environment // nil: the process environment
}

func (d oxActorDetector) DetectActor() (friction.Actor, string) {
	env := d.env
	if env == nil {
		env = agentx.NewSystemEnvironment()
	}
	if a, _ := agentx.NewDetectorWithEnv(envSignalsOnly{env}).Detect(context.Background()); a != nil {
		return friction.ActorAgent, string(a.Type())
	}
	if env.GetEnv("CI") != "" {
		return friction.ActorAgent, "ci"
	}
	return friction.ActorHuman, ""
}

// envSignalsOnly hides the filesystem from agent detection, so only
// environment variables an agent sets can name the actor. agentx's Codex
// detector otherwise falls back to a .codex/ directory in the working
// directory, which ox itself writes into repositories, and would file every
// person working there as an AI coworker.
type envSignalsOnly struct{ agentx.Environment }

func (envSignalsOnly) IsDir(string) bool      { return false }
func (envSignalsOnly) FileExists(string) bool { return false }

// sendFrictionEvent sends a friction event to the daemon via IPC for telemetry.
// Fire-and-forget with a 5ms timeout — must complete synchronously so os.Exit()
// after this call doesn't kill a background goroutine mid-flight.
func sendFrictionEvent(event *friction.FrictionEvent) {
	sendFrictionEventTo(event, "")
}

// sendFrictionEventTo sends a friction event via IPC. If socketPath is non-empty,
// it connects to that socket instead of the default daemon socket.
// Extracted for testability — tests inject a socket path to verify the real code path.
func sendFrictionEventTo(event *friction.FrictionEvent, socketPath string) {
	if event == nil {
		return
	}

	// respect telemetry opt-out at the CLI layer (defense in depth —
	// daemon also checks, but we skip the IPC entirely if disabled)
	if os.Getenv("DO_NOT_TRACK") == "1" {
		return
	}
	if strings.ToLower(os.Getenv("SAGEOX_FRICTION")) == "false" {
		return
	}
	if userCfg, err := config.LoadUserConfig(); err == nil && !userCfg.IsTelemetryEnabled() {
		return
	}

	var client *daemon.Client
	if socketPath != "" {
		client = daemon.NewClientWithSocket(socketPath)
	} else {
		client = daemon.NewClientForCurrentRepoWithTimeout(5 * time.Millisecond)
	}

	payload := daemon.FrictionPayload{
		Timestamp:  event.Timestamp,
		Kind:       string(event.Kind),
		Command:    event.Command,
		Subcommand: event.Subcommand,
		Actor:      event.Actor,
		AgentType:  event.AgentType,
		PathBucket: event.PathBucket,
		Input:      event.Input,
		ErrorMsg:   event.ErrorMsg,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	// fire-and-forget: ignore all errors
	_ = client.SendOneWay(daemon.Message{
		Type:    daemon.MsgTypeFriction,
		Payload: data,
	})
}
