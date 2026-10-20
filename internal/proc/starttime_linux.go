package proc

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

// startTime reads field 22 of /proc/<pid>/stat: clock ticks from boot to the
// process's start.
func startTime(pid int) (int64, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	// Field 2 is the command name in parentheses, which can itself contain
	// spaces and ')'. The fields after it start past the last ')'.
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(string(data[end+1:]))
	const startTimeIndex = 22 - 3 // fields[0] is field 3
	if len(fields) <= startTimeIndex {
		return 0, false
	}
	ticks, err := strconv.ParseInt(fields[startTimeIndex], 10, 64)
	if err != nil {
		return 0, false
	}
	return ticks, true
}
