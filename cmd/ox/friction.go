package main

import (
	_ "embed"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sageox/agentx"
	friction "github.com/sageox/frictionax"
	frictioncobra "github.com/sageox/frictionax/adapters/cobra"
	"github.com/sageox/frictionax/redactors/secrets"
	"github.com/sageox/ox/internal/cli"
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
		// credential patterns run when the event is built, before truncation can
		// cut a token in half; sendFrictionEventTo then reduces the rest of the
		// text to command structure (friction_redact.go)
		friction.WithRedactor(secrets.New()),
	)

	// load embedded default catalog (baseline patterns for offline/first-run)
	if len(defaultCatalogJSON) > 0 {
		var data friction.CatalogData
		if err := json.Unmarshal(defaultCatalogJSON, &data); err == nil {
			_ = frictionEngine.UpdateCatalog(data)
		}
	}
}

// handleFriction asks frictionax for a suggestion on a failed invocation.
// Suggestions are best-effort: a panic inside frictionax must not replace the
// coworker's real error with a stack trace, so it is recovered and treated as
// no suggestion. frictionax v0.1.2 panicked this way in Windows directories.
func handleFriction(engine *friction.Friction, args []string, err error) (result *friction.Result) {
	defer func() {
		if r := recover(); r != nil {
			slog.Debug("friction handle panicked", "panic", r)
			result = nil
		}
	}()
	return engine.Handle(args, err)
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
	actor, agentType := cli.DetectActor(d.env)
	return friction.Actor(actor), agentType
}

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

	// the event carries what the coworker typed (paths, names, free text), so
	// only its command structure is sent. frictionax's default redactor is a
	// no-op, and the daemon forwards these strings to the cloud verbatim.
	redacted, ok := redactFrictionEvent(rootCmd, event)
	if !ok {
		return
	}

	payload := daemon.FrictionPayload{
		Timestamp:  event.Timestamp,
		Kind:       string(event.Kind),
		Command:    redacted.Command,
		Subcommand: redacted.Subcommand,
		Actor:      event.Actor,
		AgentType:  event.AgentType,
		PathBucket: event.PathBucket,
		Input:      redacted.Input,
		ErrorMsg:   redacted.ErrorMsg,
		Suggestion: redacted.Suggestion,
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
