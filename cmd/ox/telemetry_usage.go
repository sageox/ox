package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/sageox/ox/internal/ledger"
	"github.com/sageox/ox/internal/planid"
	"github.com/sageox/ox/internal/session/contexttrace"
	"github.com/sageox/ox/internal/session/contextuse"
	"github.com/sageox/ox/internal/sessionid"
	"github.com/sageox/ox/internal/telemetry"
)

// oxOccurrenceNamespace is UUIDv5(URL, "https://sageox.ai/ox/telemetry").
// Never change this namespace or the occurrence key encodings after release.
const oxOccurrenceNamespace = "6d8961b7-070a-585f-aeae-ae2b06ad2ce1"

var capturePostHogUsage = telemetry.CapturePostHog

// usageTelemetryOff applies ox command run's gates before any payload is built:
// a process ox started for itself, a long-running command, or an opt-out.
func usageTelemetryOff() bool {
	return startedByOx || (cliCtx != nil && cliCtx.LongRunning) || !telemetry.Enabled()
}

// recoverUsageTelemetry keeps a telemetry bug from failing a successful write.
func recoverUsageTelemetry() {
	if r := recover(); r != nil {
		slog.Debug("usage telemetry failed", "panic", r)
	}
}

// captureUsage adds only the shared stamp and reuses command identity rules.
// A sender failure must never change a successful write's output or exit code.
func captureUsage(projectRoot, event, key string, at time.Time, props map[string]any) {
	c := &cli.Context{ProjectRoot: projectRoot}
	if cliCtx != nil {
		copyCtx := *cliCtx
		c = &copyCtx
		c.ProjectRoot = projectRoot
	}
	opts := postHogCaptureOpts(c)
	opts.UUID = uuid.NewSHA1(uuid.MustParse(oxOccurrenceNamespace), []byte(key)).String()
	opts.At = at
	actor, kind := oxActorDetector{}.DetectActor()
	spec, legacy, kind := postHogActors(actor, kind)
	props["actor"], props["cli_actor"] = spec, legacy
	props["surface"], props["event_version"] = "cli", 1
	props["env"] = "test"
	if endpoint.IsProduction(postHogCommandEndpoint(c)) {
		props["env"] = "production"
	}
	if event == "context.used" {
		props["agent_type"] = kind
	}
	showTelemetryNoticeOnce(os.Stderr)
	capturePostHogUsage(event, props, opts)
}

// captureContextUsed sends one context.used. recorded says ev.SessionID is a
// real recording: an unrecorded trace's minted scope keys the event's UUID but
// is never sent as session_id, so session counts and joins stay true.
func captureContextUsed(projectRoot string, ev contexttrace.Event, recorded bool) {
	defer recoverUsageTelemetry()
	if usageTelemetryOff() {
		return
	}
	if !sessionid.IsValidSessionID(ev.SessionID) || ev.OccurrenceSeq < 1 {
		return
	}
	at, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
	if err != nil {
		return
	} // Never substitute capture time on replay.
	props := map[string]any{"ref_type": postHogRefType(ev.RefType), "evidence": ev.Evidence}
	if recorded {
		props["session_id"] = ev.SessionID
	}
	if ref := postHogContextRef(ev.RefType, ev.Ref); ref != "" {
		props["ref"] = ref
	}
	if projectRoot != "" && config.IsInitialized(projectRoot) {
		if cfg, err := config.LoadProjectConfig(projectRoot); err == nil && cfg != nil {
			if cfg.RepoID != "" {
				props["repo_id"] = cfg.RepoID
			}
			if cfg.TeamID != "" {
				props["team_id"] = cfg.TeamID
			}
		}
	}
	captureUsage(projectRoot, "context.used", fmt.Sprintf("%s:%d", ev.SessionID, ev.OccurrenceSeq), at, props)
}

// CanonicalRef resolves aliases but also returns arbitrary input unchanged.
// Only exact conversation/recording and plan IDs cross the analytics boundary.
func postHogContextRef(refType, ref string) string {
	ref = contextuse.CanonicalRef(refType, ref)
	if planid.IsPlanID(ref) {
		return ref
	}
	const prefix = "cnv_" // CanonicalRef maps recording IDs to their conversation twin.
	if strings.HasPrefix(ref, prefix) {
		id, err := uuid.Parse(strings.TrimPrefix(ref, prefix))
		if err == nil && prefix+id.String() == ref {
			return ref
		}
	}
	return ""
}

func postHogRefType(kind string) string {
	switch kind {
	case "", "session", "discussion", "doc", "file", "murmur", "plan", "kb":
		return kind
	default:
		return "other"
	}
}

func captureMurmurPublished(projectRoot string, murmur ledger.MurmurFile) {
	defer recoverUsageTelemetry()
	if usageTelemetryOff() {
		return
	}
	topic := "other"
	switch murmur.Topic {
	case "general", "wip", "lint", "architecture", "conflict":
		topic = murmur.Topic
	}
	props := map[string]any{"murmur_id": murmur.ID, "importance": murmur.Importance, "scope": murmur.Scope, "topic": topic}
	if projectRoot != "" && config.IsInitialized(projectRoot) {
		if cfg, err := config.LoadProjectConfig(projectRoot); err == nil && cfg != nil && cfg.TeamID != "" {
			props["team_id"] = cfg.TeamID
		}
	}
	captureUsage(projectRoot, "murmur.published", murmur.ID, murmur.Timestamp, props)
}
