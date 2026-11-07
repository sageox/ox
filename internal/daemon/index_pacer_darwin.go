//go:build darwin

package daemon

import (
	"context"
	"time"

	"golang.org/x/sys/unix"
)

// absolute paths: the daemon can be spawned from a hook with a minimal PATH
// that lacks /usr/sbin, and a bare name would also let a PATH entry shadow
// the system tool.
const (
	pmsetPath = "/usr/bin/pmset"
	ioregPath = "/usr/sbin/ioreg"
)

// defaultHostProbes reads load from the kernel, and battery and user idle from
// the system tools (no cgo, so no IOKit). Battery and idle are cached by the
// sampler; load is a cheap syscall.
func defaultHostProbes() hostProbes {
	return hostProbes{
		loadAverage: darwinLoadAverage,
		onBattery:   darwinOnBattery,
		userIdle:    darwinUserIdle,
	}
}

func darwinLoadAverage() (float64, bool) {
	raw, err := unix.SysctlRaw("vm.loadavg")
	if err != nil {
		return 0, false
	}
	return parseDarwinLoadAvg(raw)
}

func darwinOnBattery(ctx context.Context) (bool, bool) {
	out, err := runProbeCommand(ctx, paceProbeTimeout, pmsetPath, "-g", "batt")
	if err != nil {
		return false, false
	}
	return parsePmsetOnBattery(out)
}

func darwinUserIdle(ctx context.Context) (time.Duration, bool) {
	out, err := runProbeCommand(ctx, paceProbeTimeout, ioregPath, "-c", "IOHIDSystem", "-d", "4")
	if err != nil {
		return 0, false
	}
	return parseHIDIdleTime(out)
}
