//go:build linux

package daemon

import (
	"context"
	"os"
)

const (
	procLoadAvgPath        = "/proc/loadavg"
	sysPowerSupplyRootPath = "/sys/class/power_supply"
)

// defaultHostProbes reads load from procfs and the power source from sysfs.
// User idle has no portable source on Linux (X11, Wayland and ttys differ), so
// it stays unknown and the pacer relies on load and battery alone.
func defaultHostProbes() hostProbes {
	return hostProbes{
		loadAverage: linuxLoadAverage,
		onBattery: func(context.Context) (bool, bool) {
			return readPowerSupplyOnBattery(sysPowerSupplyRootPath)
		},
	}
}

func linuxLoadAverage() (float64, bool) {
	data, err := os.ReadFile(procLoadAvgPath)
	if err != nil {
		return 0, false
	}
	return parseProcLoadAvg(string(data))
}
