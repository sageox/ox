package main

import (
	"context"
	"strings"
	"time"

	"github.com/sageox/ox/internal/flags"
)

const CheckSlugSessionTrace = "session-trace"

func syncTraceDoctorCheck() {
	delete(DoctorCheckRegistry, CheckSlugSessionTrace)
	if !flags.Get().TraceEnabled {
		return
	}
	cfg, _, err := traceConfig()
	if err == nil && (cfg.Trace == nil || !cfg.Trace.Enabled) {
		return
	}
	RegisterDoctorCheck(&DoctorCheck{Slug: CheckSlugSessionTrace, Name: "Session trace", Category: "Integrations",
		FixLevel: FixLevelAuto, Description: "Check the local trace receiver and exporter configuration", Run: checkSessionTrace})
}

func checkSessionTrace(fix bool) checkResult {
	const name = "Session trace"
	cfg, port, err := traceConfig()
	if err != nil {
		return WarningCheck(name, "cannot read opt-in", err.Error())
	}
	if !flags.Get().TraceEnabled || cfg.Trace == nil || !cfg.Trace.Enabled {
		return SkippedCheck(name, "not enabled", "")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if fix {
		if err := traceEnsureRunning(ctx, port, func() bool { return traceOptedIn(port) }); err != nil {
			return WarningCheck(name, "receiver could not start", err.Error())
		}
	}
	status, err := readSessionTraceStatus(ctx, findGitRoot(), false)
	if err != nil {
		return WarningCheck(name, "cannot inspect local traces", err.Error())
	}
	issues := append(status.Warnings, status.Exporter.Issues...)
	if status.Bytes > 1<<30 {
		issues = append(issues, "local trace spool exceeds 1 GiB; disable --purge deletes it")
	}
	if len(issues) > 0 {
		return WarningCheck(name, "trace capture needs attention", strings.Join(issues, "\n"))
	}
	return PassedCheck(name, "receiver listening; local exporter configuration ready")
}
